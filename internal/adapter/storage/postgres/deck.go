package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/usecase/port"
)

// DeckRepo хранит колоды и их состав.
type DeckRepo struct {
	base
}

// NewDeckRepo создаёт репозиторий колод.
func NewDeckRepo(pool *pgxpool.Pool) *DeckRepo {
	return &DeckRepo{base: base{pool: pool}}
}

var _ port.DeckRepo = (*DeckRepo)(nil)

const deckColumns = "id, code, COALESCE(owner_user_id, 0), lang_code, title, description, size"

// Distractors возвращает переводы других слов колоды.
//
// Сортировка сначала по совпадению части речи, потом случайная: варианты
// одной части речи делают выбор честным, но если их не набралось, лучше
// показать любые, чем меньше четырёх кнопок.
//
// Берутся только основные значения: показывать в вариантах «жилище» там,
// где основной перевод «дом», значит спрашивать не то, что учили.
func (r *DeckRepo) Distractors(ctx context.Context, q port.DistractorQuery) ([]lexicon.Translation, error) {
	const op = "подобрать ложные варианты"

	if q.Limit <= 0 {
		return nil, nil
	}

	const query = `
		SELECT t.id, t.lexeme_id, t.lang_code, t.text, t.is_primary, t.note
		FROM deck_items di
		JOIN lexemes l ON l.id = di.lexeme_id
		JOIN translations t ON t.lexeme_id = l.id AND t.lang_code = $2 AND t.is_primary
		WHERE di.deck_id = $1 AND di.lexeme_id <> $3
		ORDER BY (l.pos = $4) DESC, random()
		LIMIT $5`

	rows, err := r.db(ctx).Query(ctx, query,
		int64(q.DeckID), q.Lang.String(), int64(q.Exclude), string(q.POS), q.Limit)
	if err != nil {
		return nil, wrap(op, err)
	}
	defer rows.Close()

	var out []lexicon.Translation
	for rows.Next() {
		var (
			tr       lexicon.Translation
			id       int64
			lexemeID int64
			langCode string
		)
		if err := rows.Scan(&id, &lexemeID, &langCode, &tr.Text, &tr.IsPrimary, &tr.Note); err != nil {
			return nil, wrap(op, err)
		}

		lang, err := lexicon.ParseLanguage(langCode)
		if err != nil {
			return nil, wrap(op, err)
		}
		tr.ID = lexicon.TranslationID(id)
		tr.LexemeID = lexicon.LexemeID(lexemeID)
		tr.Lang = lang
		out = append(out, tr)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap(op, err)
	}
	return out, nil
}

// DistractorTerms возвращает слова колоды — ложные варианты для проверки
// в сторону изучаемого языка.
//
// Переводы здесь не нужны вовсе: выбирать человек будет из написаний,
// и слово без перевода на язык курса ложным вариантом быть вполне может.
func (r *DeckRepo) DistractorTerms(ctx context.Context, q port.DistractorQuery) ([]lexicon.Lexeme, error) {
	const op = "подобрать ложные варианты"

	if q.Limit <= 0 {
		return nil, nil
	}

	const query = `
		SELECT ` + lexemeColumns + `
		FROM lexemes l
		JOIN deck_items di ON di.lexeme_id = l.id
		WHERE di.deck_id = $1 AND di.lexeme_id <> $2
		ORDER BY (l.pos = $3) DESC, random()
		LIMIT $4`

	rows, err := r.db(ctx).Query(ctx, query, int64(q.DeckID), int64(q.Exclude), string(q.POS), q.Limit)
	if err != nil {
		return nil, wrap(op, err)
	}
	lexemes, err := collectLexemes(rows)
	if err != nil {
		return nil, wrap(op, err)
	}
	return lexemes, nil
}

// Languages возвращает языки, для которых есть встроенные колоды.
func (r *DeckRepo) Languages(ctx context.Context) ([]lexicon.Language, error) {
	const op = "получить языки изучения"
	const query = `
		SELECT DISTINCT lang_code
		FROM decks
		WHERE owner_user_id IS NULL AND size > 0
		ORDER BY lang_code`

	return r.languages(ctx, op, query)
}

// TranslationLanguages возвращает языки, на которые переведены слова колоды.
//
// EXISTS вместо DISTINCT по переводам: нас интересует наличие хотя бы одного
// перевода на язык, а не их число, и база может остановиться на первом.
func (r *DeckRepo) TranslationLanguages(ctx context.Context, deckID lexicon.DeckID) ([]lexicon.Language, error) {
	const op = "получить языки перевода колоды"
	const query = `
		SELECT code
		FROM languages l
		WHERE EXISTS (
			SELECT 1
			FROM deck_items di
			JOIN translations t ON t.lexeme_id = di.lexeme_id
			WHERE di.deck_id = $1 AND t.lang_code = l.code
		)
		ORDER BY code`

	return r.languages(ctx, op, query, int64(deckID))
}

// languages — общая часть запросов, возвращающих список языков.
func (r *DeckRepo) languages(ctx context.Context, op, query string, args ...any) ([]lexicon.Language, error) {
	rows, err := r.db(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, wrap(op, err)
	}
	defer rows.Close()

	var out []lexicon.Language
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, wrap(op, err)
		}
		lang, err := lexicon.ParseLanguage(code)
		if err != nil {
			return nil, wrap(op, err)
		}
		out = append(out, lang)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap(op, err)
	}
	return out, nil
}

// Builtin возвращает встроенные колоды языка изучения, по слагу.
func (r *DeckRepo) Builtin(ctx context.Context, lang lexicon.Language) ([]lexicon.Deck, error) {
	const op = "получить встроенные колоды"
	const query = "SELECT " + deckColumns + ` FROM decks
		WHERE owner_user_id IS NULL AND lang_code = $1
		ORDER BY code`

	rows, err := r.db(ctx).Query(ctx, query, lang.String())
	if err != nil {
		return nil, wrap(op, err)
	}
	decks, err := collectDecks(rows)
	if err != nil {
		return nil, wrap(op, err)
	}
	return decks, nil
}

// ByID возвращает колоду по идентификатору.
func (r *DeckRepo) ByID(ctx context.Context, id lexicon.DeckID) (lexicon.Deck, error) {
	const op = "найти колоду"
	const query = "SELECT " + deckColumns + " FROM decks WHERE id = $1"

	var deck lexicon.Deck
	if err := scanDeck(r.db(ctx).QueryRow(ctx, query, int64(id)), &deck); err != nil {
		return lexicon.Deck{}, wrap(op, err)
	}
	return deck, nil
}

// ByCode возвращает встроенную колоду по слагу — так на неё ссылаются сиды.
func (r *DeckRepo) ByCode(ctx context.Context, code string) (lexicon.Deck, error) {
	const op = "найти колоду по слагу"
	const query = "SELECT " + deckColumns + " FROM decks WHERE code = $1 AND owner_user_id IS NULL"

	var deck lexicon.Deck
	if err := scanDeck(r.db(ctx).QueryRow(ctx, query, code), &deck); err != nil {
		return lexicon.Deck{}, wrap(op, err)
	}
	return deck, nil
}

// EnsureBuiltin заводит встроенную колоду или обновляет её описание.
//
// Слаг — ключ: по нему сидер узнаёт колоду при повторной загрузке.
// Название и описание при этом перезаписываются, а размер не трогается —
// его считает добавление слов, и обнулять его на секунду означало бы
// показать пользователю пустую колоду ровно в этот момент.
func (r *DeckRepo) EnsureBuiltin(ctx context.Context, deck *lexicon.Deck) (lexicon.Deck, error) {
	const op = "создать или обновить встроенную колоду"

	if err := deck.Validate(); err != nil {
		return lexicon.Deck{}, err
	}
	if !deck.IsBuiltin() {
		return lexicon.Deck{}, fmt.Errorf("%s: %w (колода принадлежит пользователю %d)",
			op, port.ErrInvalidData, deck.OwnerID)
	}

	const query = `
		INSERT INTO decks (code, lang_code, title, description)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (code) WHERE owner_user_id IS NULL
		DO UPDATE SET lang_code   = EXCLUDED.lang_code,
		              title       = EXCLUDED.title,
		              description = EXCLUDED.description
		RETURNING ` + deckColumns

	var saved lexicon.Deck
	row := r.db(ctx).QueryRow(ctx, query, deck.Code, deck.Lang.String(), deck.Title, deck.Description)
	if err := scanDeck(row, &saved); err != nil {
		return lexicon.Deck{}, wrap(op, err)
	}
	return saved, nil
}

// EnsurePersonal возвращает личную колоду пользователя для языка изучения,
// создавая её при первом добавлении своего слова.
func (r *DeckRepo) EnsurePersonal(ctx context.Context, ownerID int64, lang lexicon.Language, title string) (lexicon.Deck, error) {
	const op = "создать или найти личную колоду"

	deck, err := lexicon.NewPersonalDeck(ownerID, lang, title)
	if err != nil {
		return lexicon.Deck{}, err
	}

	// DO UPDATE, ничего не меняющий, — способ получить RETURNING и для
	// существующей строки. DO NOTHING вернул бы пусто, и понадобился бы
	// второй запрос, гоняющийся с параллельной вставкой.
	// Название существующей колоды при этом сохраняется: пользователь мог
	// переименовать её сам.
	const query = `
		INSERT INTO decks (owner_user_id, lang_code, title)
		VALUES ($1, $2, $3)
		ON CONFLICT (owner_user_id, lang_code) WHERE owner_user_id IS NOT NULL
		DO UPDATE SET title = decks.title
		RETURNING ` + deckColumns

	var saved lexicon.Deck
	row := r.db(ctx).QueryRow(ctx, query, ownerID, lang.String(), deck.Title)
	if err := scanDeck(row, &saved); err != nil {
		return lexicon.Deck{}, wrap(op, err)
	}
	return saved, nil
}

// AddItems добавляет слова в колоду и пересчитывает её размер.
//
// Размер лежит колонкой, чтобы список колод показывался одним запросом
// без подсчёта состава; пересчёт идёт в той же транзакции, что и вставка,
// иначе он разошёлся бы с составом при первой же ошибке.
func (r *DeckRepo) AddItems(ctx context.Context, items []lexicon.DeckItem) error {
	const op = "добавить слова в колоду"

	if len(items) == 0 {
		return nil
	}
	for _, item := range items {
		if err := item.Validate(); err != nil {
			return err
		}
	}

	unique := dedupe(items, deckItemKey)

	decks := make([]int64, len(unique))
	lexemes := make([]int64, len(unique))
	positions := make([]int32, len(unique))
	for i, item := range unique {
		decks[i] = int64(item.DeckID)
		lexemes[i] = int64(item.LexemeID)
		positions[i] = int32(item.Position)
	}

	const insert = `
		INSERT INTO deck_items (deck_id, lexeme_id, position)
		SELECT * FROM unnest($1::BIGINT[], $2::BIGINT[], $3::INT[])
		ON CONFLICT (deck_id, lexeme_id) DO UPDATE
		SET position = EXCLUDED.position`

	const resize = `
		UPDATE decks d
		SET size = (SELECT count(*) FROM deck_items WHERE deck_id = d.id)
		WHERE d.id = ANY($1::BIGINT[])`

	return r.inTx(ctx, func(q queryer) error {
		if _, err := q.Exec(ctx, insert, decks, lexemes, positions); err != nil {
			return wrap(op, err)
		}
		if _, err := q.Exec(ctx, resize, decks); err != nil {
			return wrap("пересчитать размер колоды", err)
		}
		return nil
	})
}

// Contains сообщает, есть ли слово в колоде.
func (r *DeckRepo) Contains(ctx context.Context, deckID lexicon.DeckID, lexemeID lexicon.LexemeID) (bool, error) {
	const query = `SELECT EXISTS (SELECT 1 FROM deck_items WHERE deck_id = $1 AND lexeme_id = $2)`

	var exists bool
	if err := r.db(ctx).QueryRow(ctx, query, int64(deckID), int64(lexemeID)).Scan(&exists); err != nil {
		return false, wrap("проверить состав колоды", err)
	}
	return exists, nil
}

// Items возвращает состав колоды по возрастанию position — в этом порядке
// слова и вводятся в курс. Нулевой limit означает «без ограничения».
func (r *DeckRepo) Items(ctx context.Context, deckID lexicon.DeckID, offset, limit int) ([]lexicon.DeckItem, error) {
	const op = "получить состав колоды"

	// Порядок дополнен lexeme_id: у двух слов может оказаться одинаковая
	// позиция, и без второго ключа выдача поехала бы от запроса к запросу.
	const query = `
		SELECT deck_id, lexeme_id, position
		FROM deck_items
		WHERE deck_id = $1
		ORDER BY position, lexeme_id
		OFFSET $2
		LIMIT NULLIF($3, 0)`

	rows, err := r.db(ctx).Query(ctx, query, int64(deckID), offset, limit)
	if err != nil {
		return nil, wrap(op, err)
	}
	defer rows.Close()

	var out []lexicon.DeckItem
	for rows.Next() {
		var (
			item     lexicon.DeckItem
			deck     int64
			lexemeID int64
		)
		if err := rows.Scan(&deck, &lexemeID, &item.Position); err != nil {
			return nil, wrap(op, err)
		}
		item.DeckID = lexicon.DeckID(deck)
		item.LexemeID = lexicon.LexemeID(lexemeID)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap(op, err)
	}
	return out, nil
}

func scanDeck(r row, deck *lexicon.Deck) error {
	var (
		id       int64
		langCode string
	)
	if err := r.Scan(&id, &deck.Code, &deck.OwnerID, &langCode, &deck.Title, &deck.Description, &deck.Size); err != nil {
		return err
	}

	lang, err := lexicon.ParseLanguage(langCode)
	if err != nil {
		return fmt.Errorf("код языка %q из базы: %w", langCode, err)
	}
	deck.ID = lexicon.DeckID(id)
	deck.Lang = lang
	return nil
}

func collectDecks(rows pgx.Rows) ([]lexicon.Deck, error) {
	defer rows.Close()

	var out []lexicon.Deck
	for rows.Next() {
		var deck lexicon.Deck
		if err := scanDeck(rows, &deck); err != nil {
			return nil, err
		}
		out = append(out, deck)
	}
	return out, rows.Err()
}

func deckItemKey(item *lexicon.DeckItem) string {
	return fmt.Sprintf("%d\x00%d", item.DeckID, item.LexemeID)
}
