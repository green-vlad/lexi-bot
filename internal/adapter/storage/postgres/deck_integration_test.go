//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"lexi-bot/internal/adapter/storage/postgres"
	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/test/pgtest"
)

// builtinDeck заводит встроенную колоду напрямую: конструктора для неё
// в репозитории нет — встроенные колоды приходят из сидов (T-036).
func builtinDeck(t *testing.T, pool *pgxpool.Pool, code string, lang lexicon.Language) lexicon.DeckID {
	t.Helper()

	var id int64
	err := pool.QueryRow(context.Background(),
		"INSERT INTO decks (code, lang_code, title) VALUES ($1, $2, $3) RETURNING id",
		code, lang.String(), "Колода "+code).Scan(&id)
	if err != nil {
		t.Fatalf("вставка встроенной колоды не прошла: %v", err)
	}
	return lexicon.DeckID(id)
}

func TestDeckBuiltinLookup(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewDeckRepo(pool)

	korean := builtinDeck(t, pool, "ko-top-2000", langKO)
	builtinDeck(t, pool, "ko-verbs", langKO)
	builtinDeck(t, pool, "en-top-1000", langEN)

	decks, err := repo.Builtin(ctx, langKO)
	if err != nil {
		t.Fatalf("Builtin() вернул ошибку: %v", err)
	}
	if len(decks) != 2 {
		t.Fatalf("корейских колод %d, ожидалось две", len(decks))
	}
	if decks[0].Code != "ko-top-2000" || decks[1].Code != "ko-verbs" {
		t.Errorf("порядок колод = %q, %q", decks[0].Code, decks[1].Code)
	}
	for _, deck := range decks {
		if !deck.IsBuiltin() {
			t.Errorf("колода %q должна быть встроенной", deck.Code)
		}
	}

	byCode, err := repo.ByCode(ctx, "ko-top-2000")
	if err != nil {
		t.Fatalf("ByCode() вернул ошибку: %v", err)
	}
	if byCode.ID != korean {
		t.Errorf("ByCode() вернул колоду %d, ожидалась %d", byCode.ID, korean)
	}

	byID, err := repo.ByID(ctx, korean)
	if err != nil {
		t.Fatalf("ByID() вернул ошибку: %v", err)
	}
	if byID.Code != "ko-top-2000" {
		t.Errorf("ByID() вернул колоду %q", byID.Code)
	}

	if _, err := repo.ByCode(ctx, "нет-такой"); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("ByCode() для несуществующей = %v, ожидалась ErrNotFound", err)
	}
	if _, err := repo.ByID(ctx, 99999); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("ByID() для несуществующей = %v, ожидалась ErrNotFound", err)
	}
}

func TestDeckEnsurePersonalIsIdempotent(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewDeckRepo(pool)

	owner := ensureUser(t, pool, 777)

	first, err := repo.EnsurePersonal(ctx, int64(owner.ID), langKO, "Мои слова")
	if err != nil {
		t.Fatalf("EnsurePersonal() вернул ошибку: %v", err)
	}
	if first.IsBuiltin() {
		t.Error("личная колода не должна считаться встроенной")
	}
	if first.Code != "" {
		t.Errorf("Code = %q, у личной колоды слага быть не должно", first.Code)
	}

	// Второе своё слово попадает в ту же колоду, а не заводит новую.
	second, err := repo.EnsurePersonal(ctx, int64(owner.ID), langKO, "Другое название")
	if err != nil {
		t.Fatalf("повторный EnsurePersonal() вернул ошибку: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("создана вторая колода: %d против %d", second.ID, first.ID)
	}
	// Название существующей колоды сохраняется: пользователь мог сменить его сам.
	if second.Title != "Мои слова" {
		t.Errorf("Title = %q, ожидалось «Мои слова»", second.Title)
	}

	// А для другого языка изучения заводится своя колода.
	other, err := repo.EnsurePersonal(ctx, int64(owner.ID), langEN, "My words")
	if err != nil {
		t.Fatalf("EnsurePersonal() вернул ошибку: %v", err)
	}
	if other.ID == first.ID {
		t.Error("колода для другого языка должна быть отдельной")
	}
}

func TestDeckItemsOrderedByPosition(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewDeckRepo(pool)

	deck := builtinDeck(t, pool, "ko-top-2000", langKO)
	lexemes := saveLexemes(t, pool, newLexeme(t, "집"), newLexeme(t, "개"), newLexeme(t, "사람"))

	// Кладём вперемешку — выдача обязана идти по позиции, а не по вставке.
	err := repo.AddItems(ctx, []lexicon.DeckItem{
		{DeckID: deck, LexemeID: lexemes[2].ID, Position: 2},
		{DeckID: deck, LexemeID: lexemes[0].ID, Position: 0},
		{DeckID: deck, LexemeID: lexemes[1].ID, Position: 1},
	})
	if err != nil {
		t.Fatalf("AddItems() вернул ошибку: %v", err)
	}

	items, err := repo.Items(ctx, deck, 0, 0)
	if err != nil {
		t.Fatalf("Items() вернул ошибку: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("слов в колоде %d, ожидалось три", len(items))
	}
	for i, item := range items {
		if item.Position != i || item.LexemeID != lexemes[i].ID {
			t.Errorf("позиция %d: %+v, ожидалось слово %d", i, item, lexemes[i].ID)
		}
	}

	// Постраничная выдача: сидер и выдача новых слов ходят кусками.
	page, err := repo.Items(ctx, deck, 1, 1)
	if err != nil {
		t.Fatalf("Items() вернул ошибку: %v", err)
	}
	if len(page) != 1 || page[0].LexemeID != lexemes[1].ID {
		t.Errorf("страница = %+v, ожидалось второе слово", page)
	}
}

func TestDeckAddItemsUpdatesSize(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewDeckRepo(pool)

	deck := builtinDeck(t, pool, "ko-top-2000", langKO)
	lexemes := saveLexemes(t, pool, newLexeme(t, "집"), newLexeme(t, "개"))

	if err := repo.AddItems(ctx, []lexicon.DeckItem{
		{DeckID: deck, LexemeID: lexemes[0].ID, Position: 0},
	}); err != nil {
		t.Fatalf("AddItems() вернул ошибку: %v", err)
	}

	saved, err := repo.ByID(ctx, deck)
	if err != nil {
		t.Fatalf("ByID() вернул ошибку: %v", err)
	}
	if saved.Size != 1 {
		t.Errorf("Size = %d, ожидалась единица", saved.Size)
	}

	// Повторная вставка того же слова меняет позицию, но не размер.
	if err := repo.AddItems(ctx, []lexicon.DeckItem{
		{DeckID: deck, LexemeID: lexemes[0].ID, Position: 5},
		{DeckID: deck, LexemeID: lexemes[1].ID, Position: 1},
	}); err != nil {
		t.Fatalf("повторный AddItems() вернул ошибку: %v", err)
	}

	saved, err = repo.ByID(ctx, deck)
	if err != nil {
		t.Fatalf("ByID() вернул ошибку: %v", err)
	}
	if saved.Size != 2 {
		t.Errorf("Size = %d, ожидалась двойка", saved.Size)
	}

	items, err := repo.Items(ctx, deck, 0, 0)
	if err != nil {
		t.Fatalf("Items() вернул ошибку: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("слов в колоде %d, ожидалось два", len(items))
	}
	if items[0].LexemeID != lexemes[1].ID || items[1].Position != 5 {
		t.Errorf("позиция не обновилась: %+v", items)
	}
}

func TestDeckAddItemsRejectsUnknownDeck(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewDeckRepo(pool)

	lexemes := saveLexemes(t, pool, newLexeme(t, "집"))

	err := repo.AddItems(ctx, []lexicon.DeckItem{{DeckID: 99999, LexemeID: lexemes[0].ID, Position: 0}})
	if !errors.Is(err, port.ErrNotFound) {
		t.Errorf("AddItems() = %v, ожидалась ErrNotFound", err)
	}
}

func TestDeckAddItemsIsAtomic(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewDeckRepo(pool)

	deck := builtinDeck(t, pool, "ko-top-2000", langKO)
	lexemes := saveLexemes(t, pool, newLexeme(t, "집"))

	// Одно слово существует, второго нет: не должно примениться ничего,
	// иначе размер колоды разойдётся с её составом.
	err := repo.AddItems(ctx, []lexicon.DeckItem{
		{DeckID: deck, LexemeID: lexemes[0].ID, Position: 0},
		{DeckID: deck, LexemeID: 99999, Position: 1},
	})
	if !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("AddItems() = %v, ожидалась ErrNotFound", err)
	}

	items, err := repo.Items(ctx, deck, 0, 0)
	if err != nil {
		t.Fatalf("Items() вернул ошибку: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("в колоде %d слов, ожидалась пустая колода", len(items))
	}

	saved, err := repo.ByID(ctx, deck)
	if err != nil {
		t.Fatalf("ByID() вернул ошибку: %v", err)
	}
	if saved.Size != 0 {
		t.Errorf("Size = %d, ожидался ноль", saved.Size)
	}
}

func TestDeckLanguages(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewDeckRepo(pool)

	korean := builtinDeck(t, pool, "ko-top-2000", langKO)
	english := builtinDeck(t, pool, "en-top-1000", langEN)
	builtinDeck(t, pool, "es-top-500", langES) // колода без слов

	lexemes := saveLexemes(t, pool, newLexeme(t, "집"), newLexeme(t, "개"))
	if err := repo.AddItems(ctx, []lexicon.DeckItem{
		{DeckID: korean, LexemeID: lexemes[0].ID, Position: 0},
		{DeckID: english, LexemeID: lexemes[1].ID, Position: 0},
	}); err != nil {
		t.Fatalf("AddItems() вернул ошибку: %v", err)
	}

	langs, err := repo.Languages(ctx)
	if err != nil {
		t.Fatalf("Languages() вернул ошибку: %v", err)
	}

	// Пустая колода в список не попадает: предлагать язык, учить который
	// пока нечем, значит завести пользователю курс из ничего.
	if len(langs) != 2 {
		t.Fatalf("языков %d (%v), ожидалось два: пустая колода не в счёт", len(langs), langs)
	}
	if langs[0] != langEN || langs[1] != langKO {
		t.Errorf("языки = %v, ожидались en и ko по алфавиту", langs)
	}
}

func TestDeckTranslationLanguages(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewDeckRepo(pool)
	lexemeRepo := postgres.NewLexemeRepo(pool)

	deck := builtinDeck(t, pool, "ko-top-2000", langKO)
	lexemes := saveLexemes(t, pool, newLexeme(t, "집"), newLexeme(t, "개"))
	if err := repo.AddItems(ctx, []lexicon.DeckItem{
		{DeckID: deck, LexemeID: lexemes[0].ID, Position: 0},
		{DeckID: deck, LexemeID: lexemes[1].ID, Position: 1},
	}); err != nil {
		t.Fatalf("AddItems() вернул ошибку: %v", err)
	}

	// Первое слово переведено на русский и английский, второе только
	// на русский: язык предлагается, если перевод есть хотя бы у одного.
	err := lexemeRepo.SaveTranslations(ctx, []lexicon.Translation{
		{LexemeID: lexemes[0].ID, Lang: langRU, Text: "дом", IsPrimary: true},
		{LexemeID: lexemes[0].ID, Lang: langEN, Text: "house", IsPrimary: true},
		{LexemeID: lexemes[1].ID, Lang: langRU, Text: "собака", IsPrimary: true},
	})
	if err != nil {
		t.Fatalf("SaveTranslations() вернул ошибку: %v", err)
	}

	langs, err := repo.TranslationLanguages(ctx, deck)
	if err != nil {
		t.Fatalf("TranslationLanguages() вернул ошибку: %v", err)
	}
	if len(langs) != 2 {
		t.Fatalf("языков перевода %d (%v), ожидалось два", len(langs), langs)
	}
	if langs[0] != langEN || langs[1] != langRU {
		t.Errorf("языки = %v, ожидались en и ru по алфавиту", langs)
	}

	// У колоды без переводов выбирать нечего, и это не ошибка.
	other := builtinDeck(t, pool, "en-top-1000", langEN)
	langs, err = repo.TranslationLanguages(ctx, other)
	if err != nil {
		t.Fatalf("TranslationLanguages() вернул ошибку: %v", err)
	}
	if len(langs) != 0 {
		t.Errorf("языки = %v, ожидался пустой список", langs)
	}
}

func TestDeckDistractors(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewDeckRepo(pool)
	lexemeRepo := postgres.NewLexemeRepo(pool)

	deck := builtinDeck(t, pool, "ko-top-2000", langKO)
	other := builtinDeck(t, pool, "en-top-1000", langEN)

	nouns := saveLexemes(t, pool,
		newLexeme(t, "집"), newLexeme(t, "개"), newLexeme(t, "물"))
	verbs := saveLexemes(t, pool,
		newLexeme(t, "가다", func(p *lexicon.LexemeParams) { p.POS = lexicon.POSVerb }),
		newLexeme(t, "먹다", func(p *lexicon.LexemeParams) { p.POS = lexicon.POSVerb }))

	items := make([]lexicon.DeckItem, 0, len(nouns)+len(verbs))
	translations := make([]lexicon.Translation, 0, len(nouns)+len(verbs))
	for i, lex := range append(append([]lexicon.Lexeme{}, nouns...), verbs...) {
		items = append(items, lexicon.DeckItem{DeckID: deck, LexemeID: lex.ID, Position: i})
		translations = append(translations, lexicon.Translation{
			LexemeID: lex.ID, Lang: langRU, Text: "перевод " + lex.Term, IsPrimary: true,
		})
		// Второй, неосновной перевод: в варианты он попадать не должен.
		translations = append(translations, lexicon.Translation{
			LexemeID: lex.ID, Lang: langRU, Text: "синоним " + lex.Term,
		})
	}
	if err := repo.AddItems(ctx, items); err != nil {
		t.Fatalf("AddItems() вернул ошибку: %v", err)
	}
	if err := lexemeRepo.SaveTranslations(ctx, translations); err != nil {
		t.Fatalf("SaveTranslations() вернул ошибку: %v", err)
	}

	got, err := repo.Distractors(ctx, port.DistractorQuery{
		DeckID: deck, Lang: langRU, POS: lexicon.POSNoun, Exclude: nouns[0].ID, Limit: 3,
	})
	if err != nil {
		t.Fatalf("Distractors() вернул ошибку: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("вариантов %d, ожидалось три", len(got))
	}

	for _, tr := range got {
		if tr.LexemeID == nouns[0].ID {
			t.Error("в вариантах оказался перевод самого спрашиваемого слова")
		}
		if !tr.IsPrimary {
			t.Errorf("вариант %q не основной перевод: спрашивать надо то, что учили", tr.Text)
		}
		if strings.HasPrefix(tr.Text, "синоним") {
			t.Errorf("в вариантах неосновной перевод %q", tr.Text)
		}
	}

	// Существительные предпочтительнее: выбор между словами одной части речи
	// честнее, чем между существительным и тремя глаголами.
	sameKind := 0
	for _, tr := range got {
		for _, noun := range nouns[1:] {
			if tr.LexemeID == noun.ID {
				sameKind++
			}
		}
	}
	if sameKind != 2 {
		t.Errorf("существительных среди вариантов %d, ожидалось два (все, что есть)", sameKind)
	}

	// Слова чужой колоды в варианты не попадают.
	if _, err := repo.Distractors(ctx, port.DistractorQuery{
		DeckID: other, Lang: langRU, Exclude: nouns[0].ID, Limit: 3,
	}); err != nil {
		t.Fatalf("Distractors() вернул ошибку: %v", err)
	}

	empty, err := repo.Distractors(ctx, port.DistractorQuery{
		DeckID: other, Lang: langRU, Exclude: nouns[0].ID, Limit: 3,
	})
	if err != nil {
		t.Fatalf("Distractors() вернул ошибку: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("из пустой колоды получено %d вариантов", len(empty))
	}
}
