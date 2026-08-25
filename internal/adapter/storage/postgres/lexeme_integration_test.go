//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"lexi-bot/internal/adapter/storage/postgres"
	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/test/pgtest"
)

var (
	langKO = lexicon.MustParseLanguage("ko")
	langRU = lexicon.MustParseLanguage("ru")
	langEN = lexicon.MustParseLanguage("en")
	langES = lexicon.MustParseLanguage("es")
)

func newLexeme(t *testing.T, term string, opts ...func(*lexicon.LexemeParams)) lexicon.Lexeme {
	t.Helper()

	p := lexicon.LexemeParams{Lang: langKO, Term: term, POS: lexicon.POSNoun}
	for _, opt := range opts {
		opt(&p)
	}

	lex, err := lexicon.NewLexeme(p)
	if err != nil {
		t.Fatalf("NewLexeme() вернул ошибку: %v", err)
	}
	return lex
}

func saveLexemes(t *testing.T, pool *pgxpool.Pool, lexemes ...lexicon.Lexeme) []lexicon.Lexeme {
	t.Helper()

	saved, err := postgres.NewLexemeRepo(pool).Upsert(context.Background(), lexemes)
	if err != nil {
		t.Fatalf("Upsert() вернул ошибку: %v", err)
	}

	out := make([]lexicon.Lexeme, 0, len(saved))
	for i := range saved {
		out = append(out, saved[i].Lexeme)
	}
	return out
}

func TestLexemeUpsertAssignsIDsInOrder(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewLexemeRepo(pool)

	want := []lexicon.Lexeme{
		newLexeme(t, "집", func(p *lexicon.LexemeParams) { p.FreqRank = 12; p.Reading = "jip" }),
		newLexeme(t, "개"),
		newLexeme(t, "사람"),
	}

	saved, err := repo.Upsert(ctx, want)
	if err != nil {
		t.Fatalf("Upsert() вернул ошибку: %v", err)
	}
	if len(saved) != len(want) {
		t.Fatalf("сохранено %d лексем, ожидалось %d", len(saved), len(want))
	}

	// Порядок ответа повторяет порядок запроса: сидер сопоставляет строки
	// со своим списком по позиции.
	for i := range want {
		if saved[i].Lexeme.Term != want[i].Term {
			t.Errorf("позиция %d: term = %q, ожидалось %q", i, saved[i].Lexeme.Term, want[i].Term)
		}
		if saved[i].Lexeme.ID == 0 {
			t.Errorf("позиция %d: идентификатор не присвоен", i)
		}
		if !saved[i].Lexeme.IsBuiltin() {
			t.Errorf("позиция %d: слово без владельца должно быть встроенным", i)
		}
		if !saved[i].Created {
			t.Errorf("позиция %d: слово должно считаться новым", i)
		}
	}
	if saved[0].Lexeme.Reading != "jip" || saved[0].Lexeme.FreqRank != 12 {
		t.Errorf("поля не сохранились: %+v", saved[0].Lexeme)
	}
}

func TestLexemeUpsertIsIdempotent(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewLexemeRepo(pool)

	first := saveLexemes(t, pool, newLexeme(t, "집", func(p *lexicon.LexemeParams) { p.FreqRank = 12 }))

	// Второй прогон сидера обязан дать то же состояние базы, а не удвоить
	// словарь: конфликт по уникальному индексу обновляет строку.
	second, err := repo.Upsert(ctx, []lexicon.Lexeme{
		newLexeme(t, "집", func(p *lexicon.LexemeParams) { p.FreqRank = 7; p.Reading = "jip" }),
	})
	if err != nil {
		t.Fatalf("повторный Upsert() вернул ошибку: %v", err)
	}
	if second[0].Lexeme.ID != first[0].ID {
		t.Errorf("идентификатор сменился: %d против %d", second[0].Lexeme.ID, first[0].ID)
	}
	if second[0].Lexeme.FreqRank != 7 || second[0].Lexeme.Reading != "jip" {
		t.Errorf("обновляемые поля не обновились: %+v", second[0].Lexeme)
	}
	// Слово было и изменилось: сидер должен увидеть это в отчёте.
	if second[0].Created || !second[0].Changed {
		t.Errorf("признаки записи = %+v, ожидалось «изменено»", second[0])
	}

	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM lexemes").Scan(&count); err != nil {
		t.Fatalf("подсчёт лексем не прошёл: %v", err)
	}
	if count != 1 {
		t.Errorf("в базе %d лексем, ожидалась одна", count)
	}
}

func TestLexemeUpsertHandlesDuplicatesInBatch(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewLexemeRepo(pool)

	// Дубликаты внутри одной пачки Postgres не прощает — репозиторий обязан
	// схлопнуть их сам, оставив последнее вхождение.
	saved, err := repo.Upsert(ctx, []lexicon.Lexeme{
		newLexeme(t, "집", func(p *lexicon.LexemeParams) { p.FreqRank = 12 }),
		newLexeme(t, "개"),
		newLexeme(t, "집", func(p *lexicon.LexemeParams) { p.FreqRank = 3 }),
	})
	if err != nil {
		t.Fatalf("Upsert() вернул ошибку: %v", err)
	}
	if len(saved) != 2 {
		t.Fatalf("сохранено %d лексем, ожидалось две", len(saved))
	}
	if saved[0].Lexeme.Term != "집" || saved[0].Lexeme.FreqRank != 3 {
		t.Errorf("осталось не последнее вхождение: %+v", saved[0].Lexeme)
	}
}

func TestLexemeBuiltinAndPersonalCoexist(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewLexemeRepo(pool)

	owner := ensureUser(t, pool, 777)

	// Одно и то же слово может быть и встроенным, и личным: уникальность
	// считает владельца, а COALESCE в индексе не даёт двум NULL разойтись.
	saved, err := repo.Upsert(ctx, []lexicon.Lexeme{
		newLexeme(t, "집"),
		newLexeme(t, "집", func(p *lexicon.LexemeParams) { p.OwnerID = int64(owner.ID) }),
	})
	if err != nil {
		t.Fatalf("Upsert() вернул ошибку: %v", err)
	}
	if len(saved) != 2 || saved[0].Lexeme.ID == saved[1].Lexeme.ID {
		t.Fatalf("встроенное и личное слово должны быть разными строками: %+v", saved)
	}

	builtin, err := repo.ByTerm(ctx, langKO, "집", 0)
	if err != nil {
		t.Fatalf("ByTerm() вернул ошибку: %v", err)
	}
	if !builtin.IsBuiltin() {
		t.Error("ByTerm() с нулевым владельцем должен находить встроенное слово")
	}

	personal, err := repo.ByTerm(ctx, langKO, "집", int64(owner.ID))
	if err != nil {
		t.Fatalf("ByTerm() вернул ошибку: %v", err)
	}
	if personal.OwnerID != int64(owner.ID) {
		t.Errorf("OwnerID = %d, ожидалось %d", personal.OwnerID, owner.ID)
	}

	if _, err := repo.ByTerm(ctx, langKO, "없다", 0); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("ByTerm() для несуществующего = %v, ожидалась ErrNotFound", err)
	}
}

func TestLexemeByIDsKeepsRequestedOrder(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewLexemeRepo(pool)

	saved := saveLexemes(t, pool, newLexeme(t, "집"), newLexeme(t, "개"), newLexeme(t, "사람"))

	// Очередь карточек превращается в слова одним запросом, и порядок в ней
	// важен: он задаёт порядок показа.
	want := []lexicon.LexemeID{saved[2].ID, saved[0].ID, saved[1].ID}
	got, err := repo.ByIDs(ctx, want)
	if err != nil {
		t.Fatalf("ByIDs() вернул ошибку: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("получено %d лексем, ожидалось %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Errorf("позиция %d: id = %d, ожидался %d", i, got[i].ID, want[i])
		}
	}

	// Несуществующие идентификаторы просто отсутствуют в ответе.
	got, err = repo.ByIDs(ctx, []lexicon.LexemeID{saved[0].ID, 99999})
	if err != nil {
		t.Fatalf("ByIDs() вернул ошибку: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("получено %d лексем, ожидалась одна", len(got))
	}

	if got, err := repo.ByIDs(ctx, nil); err != nil || got != nil {
		t.Errorf("ByIDs(nil) = %v, %v; ожидался пустой ответ без запроса", got, err)
	}
}

func TestTranslationsGroupedByLexeme(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewLexemeRepo(pool)

	saved := saveLexemes(t, pool, newLexeme(t, "집"), newLexeme(t, "개"))

	err := repo.SaveTranslations(ctx, []lexicon.Translation{
		{LexemeID: saved[0].ID, Lang: langRU, Text: "здание"},
		{LexemeID: saved[0].ID, Lang: langRU, Text: "дом", IsPrimary: true, Note: "разг."},
		{LexemeID: saved[0].ID, Lang: langEN, Text: "house", IsPrimary: true},
		{LexemeID: saved[1].ID, Lang: langRU, Text: "собака", IsPrimary: true},
	})
	if err != nil {
		t.Fatalf("SaveTranslations() вернул ошибку: %v", err)
	}

	got, err := repo.Translations(ctx, []lexicon.LexemeID{saved[0].ID, saved[1].ID}, langRU)
	if err != nil {
		t.Fatalf("Translations() вернул ошибку: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("переводы получены для %d лексем, ожидалось две", len(got))
	}

	house := got[saved[0].ID]
	if len(house) != 2 {
		t.Fatalf("у слова %d переводов %d, ожидалось два (английский не в счёт)", saved[0].ID, len(house))
	}
	// Основное значение идёт первым: его показывают в карточке.
	if !house[0].IsPrimary || house[0].Text != "дом" {
		t.Errorf("первым идёт %+v, ожидалось основное значение «дом»", house[0])
	}
	if house[0].Note != "разг." {
		t.Errorf("Note = %q, ожидалось «разг.»", house[0].Note)
	}
	if house[1].Text != "здание" {
		t.Errorf("вторым идёт %q, ожидалось «здание»", house[1].Text)
	}

	if got, err := repo.Translations(ctx, nil, langRU); err != nil || len(got) != 0 {
		t.Errorf("Translations(nil) = %v, %v; ожидалась пустая карта", got, err)
	}
}

func TestSaveTranslationsIsIdempotent(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewLexemeRepo(pool)

	saved := saveLexemes(t, pool, newLexeme(t, "집"))

	first := []lexicon.Translation{{LexemeID: saved[0].ID, Lang: langRU, Text: "дом", IsPrimary: true}}
	if err := repo.SaveTranslations(ctx, first); err != nil {
		t.Fatalf("SaveTranslations() вернул ошибку: %v", err)
	}
	// Тот же перевод с уточнением — обновление, а не вторая строка.
	second := []lexicon.Translation{{LexemeID: saved[0].ID, Lang: langRU, Text: "дом", IsPrimary: true, Note: "жильё"}}
	if err := repo.SaveTranslations(ctx, second); err != nil {
		t.Fatalf("повторный SaveTranslations() вернул ошибку: %v", err)
	}

	got, err := repo.Translations(ctx, []lexicon.LexemeID{saved[0].ID}, langRU)
	if err != nil {
		t.Fatalf("Translations() вернул ошибку: %v", err)
	}
	if len(got[saved[0].ID]) != 1 {
		t.Fatalf("переводов %d, ожидался один", len(got[saved[0].ID]))
	}
	if got[saved[0].ID][0].Note != "жильё" {
		t.Errorf("Note = %q, ожидалось «жильё»", got[saved[0].ID][0].Note)
	}
}

func TestSaveTranslationsRejectsSecondPrimary(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewLexemeRepo(pool)

	saved := saveLexemes(t, pool, newLexeme(t, "집"))

	if err := repo.SaveTranslations(ctx, []lexicon.Translation{
		{LexemeID: saved[0].ID, Lang: langRU, Text: "дом", IsPrimary: true},
	}); err != nil {
		t.Fatalf("SaveTranslations() вернул ошибку: %v", err)
	}

	// Основное значение на язык может быть только одно — это ограничение
	// схемы, и наружу оно выходит понятной ошибкой, а не кодом Postgres.
	err := repo.SaveTranslations(ctx, []lexicon.Translation{
		{LexemeID: saved[0].ID, Lang: langRU, Text: "здание", IsPrimary: true},
	})
	if !errors.Is(err, port.ErrConflict) {
		t.Errorf("SaveTranslations() = %v, ожидалась ErrConflict", err)
	}
}

func TestSaveTranslationsRejectsUnknownLexeme(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()

	err := postgres.NewLexemeRepo(pool).SaveTranslations(ctx, []lexicon.Translation{
		{LexemeID: 99999, Lang: langRU, Text: "дом"},
	})
	if !errors.Is(err, port.ErrNotFound) {
		t.Errorf("SaveTranslations() = %v, ожидалась ErrNotFound", err)
	}
}
