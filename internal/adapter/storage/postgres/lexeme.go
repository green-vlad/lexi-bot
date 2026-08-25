package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/usecase/port"
)

// LexemeRepo хранит слова и переводы в PostgreSQL.
type LexemeRepo struct {
	base
}

// NewLexemeRepo создаёт репозиторий словаря.
func NewLexemeRepo(pool *pgxpool.Pool) *LexemeRepo {
	return &LexemeRepo{base: base{pool: pool}}
}

var _ port.LexemeRepo = (*LexemeRepo)(nil)

// lexemeColumns: владелец наружу отдаётся нулём вместо NULL — в домене
// «встроенное слово» это ноль, и переводить одно в другое приходится
// на границе, а не в каждом сценарии.
const lexemeColumns = "id, lang_code, term, reading, example, pos, freq_rank, COALESCE(owner_user_id, 0)"

// Upsert вставляет или обновляет лексемы одним запросом.
//
// Возвращаются они в том же порядке, в каком пришли: RETURNING отдаёт строки
// в порядке, удобном базе, и полагаться на него нельзя, а вызывающий (сидер
// или импорт) сопоставляет результат со своим списком по позиции.
func (r *LexemeRepo) Upsert(ctx context.Context, lexemes []lexicon.Lexeme) ([]port.Upserted, error) {
	const op = "сохранить лексемы"

	if len(lexemes) == 0 {
		return nil, nil
	}
	for _, lex := range lexemes {
		if err := lex.Validate(); err != nil {
			return nil, err
		}
	}

	// Дубликаты внутри одной пачки Postgres не прощает: ON CONFLICT DO UPDATE
	// не может задеть одну строку дважды за запрос. Схлопываем их сами,
	// оставляя последнее вхождение, — так же, как повёл бы себя ряд
	// последовательных вставок.
	unique := dedupe(lexemes, lexemeKey)

	langs := make([]string, len(unique))
	terms := make([]string, len(unique))
	readings := make([]string, len(unique))
	examples := make([]string, len(unique))
	parts := make([]string, len(unique))
	ranks := make([]int32, len(unique))
	owners := make([]int64, len(unique))
	for i, lex := range unique {
		langs[i] = lex.Lang.String()
		terms[i] = lex.Term
		readings[i] = lex.Reading
		examples[i] = lex.Example
		parts[i] = string(lex.POS)
		ranks[i] = int32(lex.FreqRank)
		owners[i] = lex.OwnerID
	}

	// Условие в DO UPDATE отсекает строки, которые не изменились: они
	// не переписываются и не попадают в RETURNING. Так сидер видит, что
	// именно поменялось, а база не переписывает две тысячи строк ради
	// того, чтобы записать в них ровно то же самое.
	const query = `
		INSERT INTO lexemes (lang_code, term, reading, example, pos, freq_rank, owner_user_id)
		SELECT t.lang_code, t.term, t.reading, t.example, t.pos, t.freq_rank, NULLIF(t.owner_user_id, 0)
		FROM unnest($1::TEXT[], $2::TEXT[], $3::TEXT[], $4::TEXT[], $5::TEXT[], $6::INT[], $7::BIGINT[])
			AS t(lang_code, term, reading, example, pos, freq_rank, owner_user_id)
		ON CONFLICT (lang_code, term, pos, COALESCE(owner_user_id, 0)) DO UPDATE
		SET reading   = EXCLUDED.reading,
		    example   = EXCLUDED.example,
		    freq_rank = EXCLUDED.freq_rank
		WHERE (lexemes.reading, lexemes.example, lexemes.freq_rank)
		      IS DISTINCT FROM (EXCLUDED.reading, EXCLUDED.example, EXCLUDED.freq_rank)
		RETURNING ` + lexemeColumns + `, (xmax = 0) AS created`

	rows, err := r.db(ctx).Query(ctx, query, langs, terms, readings, examples, parts, ranks, owners)
	if err != nil {
		return nil, wrap(op, err)
	}

	touched, err := collectUpserted(rows)
	if err != nil {
		return nil, wrap(op, err)
	}

	// Строки, которых нет в ответе, остались как были — их идентификаторы
	// всё равно нужны: на них ссылаются переводы и состав колоды.
	unchanged, err := r.byKeys(ctx, unique, touched)
	if err != nil {
		return nil, err
	}

	return orderUpserted(unique, append(touched, unchanged...)), nil
}

// byKeys дочитывает слова, которых не оказалось в ответе на запись.
func (r *LexemeRepo) byKeys(ctx context.Context, wanted []lexicon.Lexeme, touched []port.Upserted) ([]port.Upserted, error) {
	const op = "получить неизменившиеся слова"

	known := make(map[string]bool, len(touched))
	for i := range touched {
		known[lexemeKey(&touched[i].Lexeme)] = true
	}

	var (
		langs  []string
		terms  []string
		parts  []string
		owners []int64
	)
	for i := range wanted {
		if known[lexemeKey(&wanted[i])] {
			continue
		}
		langs = append(langs, wanted[i].Lang.String())
		terms = append(terms, wanted[i].Term)
		parts = append(parts, string(wanted[i].POS))
		owners = append(owners, wanted[i].OwnerID)
	}
	if len(langs) == 0 {
		return nil, nil
	}

	const query = "SELECT " + lexemeColumns + ` FROM lexemes
		WHERE (lang_code, term, pos, COALESCE(owner_user_id, 0)) IN (
			SELECT * FROM unnest($1::TEXT[], $2::TEXT[], $3::TEXT[], $4::BIGINT[])
		)`

	rows, err := r.db(ctx).Query(ctx, query, langs, terms, parts, owners)
	if err != nil {
		return nil, wrap(op, err)
	}
	lexemes, err := collectLexemes(rows)
	if err != nil {
		return nil, wrap(op, err)
	}

	out := make([]port.Upserted, 0, len(lexemes))
	for i := range lexemes {
		out = append(out, port.Upserted{Lexeme: lexemes[i]})
	}
	return out, nil
}

// ByTerm ищет слово по языку и написанию: среди встроенных при ownerID = 0,
// среди личных слов пользователя иначе.
func (r *LexemeRepo) ByTerm(ctx context.Context, lang lexicon.Language, term string, ownerID int64) (lexicon.Lexeme, error) {
	const op = "найти слово"
	const query = "SELECT " + lexemeColumns + ` FROM lexemes
		WHERE lang_code = $1 AND term = $2 AND COALESCE(owner_user_id, 0) = $3
		ORDER BY id
		LIMIT 1`

	var lex lexicon.Lexeme
	row := r.db(ctx).QueryRow(ctx, query, lang.String(), term, ownerID)
	if err := scanLexeme(row, &lex); err != nil {
		return lexicon.Lexeme{}, wrap(op, err)
	}
	return lex, nil
}

// ByIDs возвращает лексемы в порядке запрошенных идентификаторов; те, чего
// в базе нет, просто отсутствуют в ответе.
func (r *LexemeRepo) ByIDs(ctx context.Context, ids []lexicon.LexemeID) ([]lexicon.Lexeme, error) {
	const op = "получить слова"

	if len(ids) == 0 {
		return nil, nil
	}

	raw := make([]int64, len(ids))
	for i, id := range ids {
		raw[i] = int64(id)
	}

	const query = "SELECT " + lexemeColumns + " FROM lexemes WHERE id = ANY($1::BIGINT[])"

	rows, err := r.db(ctx).Query(ctx, query, raw)
	if err != nil {
		return nil, wrap(op, err)
	}
	found, err := collectLexemes(rows)
	if err != nil {
		return nil, wrap(op, err)
	}

	byID := make(map[lexicon.LexemeID]lexicon.Lexeme, len(found))
	for _, lex := range found {
		byID[lex.ID] = lex
	}

	ordered := make([]lexicon.Lexeme, 0, len(ids))
	for _, id := range ids {
		if lex, ok := byID[id]; ok {
			ordered = append(ordered, lex)
		}
	}
	return ordered, nil
}

// SaveTranslations сохраняет переводы пачкой.
//
// Повторный перевод того же текста обновляется, а не задваивается: сидер
// должен быть идемпотентным, иначе второй прогон удвоит словарь.
func (r *LexemeRepo) SaveTranslations(ctx context.Context, translations []lexicon.Translation) error {
	const op = "сохранить переводы"

	if len(translations) == 0 {
		return nil
	}
	for _, tr := range translations {
		if err := tr.Validate(); err != nil {
			return err
		}
	}

	unique := dedupe(translations, translationKey)

	lexemes := make([]int64, len(unique))
	langs := make([]string, len(unique))
	texts := make([]string, len(unique))
	primary := make([]bool, len(unique))
	notes := make([]string, len(unique))
	for i, tr := range unique {
		lexemes[i] = int64(tr.LexemeID)
		langs[i] = tr.Lang.String()
		texts[i] = tr.Text
		primary[i] = tr.IsPrimary
		notes[i] = tr.Note
	}

	const query = `
		INSERT INTO translations (lexeme_id, lang_code, text, is_primary, note)
		SELECT * FROM unnest($1::BIGINT[], $2::TEXT[], $3::TEXT[], $4::BOOL[], $5::TEXT[])
		ON CONFLICT (lexeme_id, lang_code, text) DO UPDATE
		SET is_primary = EXCLUDED.is_primary,
		    note       = EXCLUDED.note`

	_, err := r.db(ctx).Exec(ctx, query, lexemes, langs, texts, primary, notes)
	return wrap(op, err)
}

// Translations возвращает переводы на нужный язык, разложенные по лексемам.
// Основное значение идёт первым: его показывают в карточке, остальные
// принимаются как допустимые ответы.
func (r *LexemeRepo) Translations(ctx context.Context, ids []lexicon.LexemeID, lang lexicon.Language) (map[lexicon.LexemeID][]lexicon.Translation, error) {
	const op = "получить переводы"

	if len(ids) == 0 {
		return map[lexicon.LexemeID][]lexicon.Translation{}, nil
	}

	raw := make([]int64, len(ids))
	for i, id := range ids {
		raw[i] = int64(id)
	}

	const query = `
		SELECT id, lexeme_id, lang_code, text, is_primary, note
		FROM translations
		WHERE lexeme_id = ANY($1::BIGINT[]) AND lang_code = $2
		ORDER BY lexeme_id, is_primary DESC, id`

	rows, err := r.db(ctx).Query(ctx, query, raw, lang.String())
	if err != nil {
		return nil, wrap(op, err)
	}
	defer rows.Close()

	out := make(map[lexicon.LexemeID][]lexicon.Translation, len(ids))
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

		parsed, err := lexicon.ParseLanguage(langCode)
		if err != nil {
			return nil, wrap(op, err)
		}
		tr.ID = lexicon.TranslationID(id)
		tr.LexemeID = lexicon.LexemeID(lexemeID)
		tr.Lang = parsed

		out[tr.LexemeID] = append(out[tr.LexemeID], tr)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap(op, err)
	}
	return out, nil
}

func scanLexeme(r row, lex *lexicon.Lexeme) error {
	return scanLexemeWith(r, lex, nil)
}

// scanLexemeWith читает лексему, а при непустом created — ещё и признак
// того, что строка только что создана.
func scanLexemeWith(r row, lex *lexicon.Lexeme, created *bool) error {
	var (
		id       int64
		langCode string
		pos      string
	)

	dest := []any{&id, &langCode, &lex.Term, &lex.Reading, &lex.Example, &pos, &lex.FreqRank, &lex.OwnerID}
	if created != nil {
		dest = append(dest, created)
	}
	if err := r.Scan(dest...); err != nil {
		return err
	}

	lang, err := lexicon.ParseLanguage(langCode)
	if err != nil {
		return fmt.Errorf("код языка %q из базы: %w", langCode, err)
	}
	lex.ID = lexicon.LexemeID(id)
	lex.Lang = lang
	lex.POS = lexicon.PartOfSpeech(pos)
	return nil
}

// collectUpserted читает строки записи вместе с признаком «создана».
func collectUpserted(rows pgx.Rows) ([]port.Upserted, error) {
	defer rows.Close()

	var out []port.Upserted
	for rows.Next() {
		var (
			lex     lexicon.Lexeme
			created bool
		)
		if err := scanLexemeWith(rows, &lex, &created); err != nil {
			return nil, err
		}
		out = append(out, port.Upserted{Lexeme: lex, Created: created, Changed: !created})
	}
	return out, rows.Err()
}

// orderUpserted раскладывает результат записи в порядке исходного списка.
func orderUpserted(want []lexicon.Lexeme, got []port.Upserted) []port.Upserted {
	byKey := make(map[string]port.Upserted, len(got))
	for i := range got {
		byKey[lexemeKey(&got[i].Lexeme)] = got[i]
	}

	out := make([]port.Upserted, 0, len(want))
	for i := range want {
		if saved, ok := byKey[lexemeKey(&want[i])]; ok {
			out = append(out, saved)
		}
	}
	return out
}

func collectLexemes(rows pgx.Rows) ([]lexicon.Lexeme, error) {
	defer rows.Close()

	var out []lexicon.Lexeme
	for rows.Next() {
		var lex lexicon.Lexeme
		if err := scanLexeme(rows, &lex); err != nil {
			return nil, err
		}
		out = append(out, lex)
	}
	return out, rows.Err()
}

// lexemeKey и translationKey повторяют уникальные индексы схемы: по ним
// схлопываются дубликаты внутри пачки и восстанавливается исходный порядок.
//
// Ключи берут указатель: сидер прогоняет через них тысячи слов, и копия
// структуры на каждое — единственное место в адаптере, где это заметно.
func lexemeKey(lex *lexicon.Lexeme) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%d", lex.Lang, lex.Term, lex.POS, lex.OwnerID)
}

func translationKey(tr *lexicon.Translation) string {
	return fmt.Sprintf("%d\x00%s\x00%s", tr.LexemeID, tr.Lang, tr.Text)
}

// dedupe оставляет по одному элементу на ключ, сохраняя последнее вхождение
// и порядок первого.
func dedupe[T any](items []T, key func(*T) string) []T {
	positions := make(map[string]int, len(items))
	out := make([]T, 0, len(items))

	for i := range items {
		k := key(&items[i])
		if pos, seen := positions[k]; seen {
			out[pos] = items[i]
			continue
		}
		positions[k] = len(out)
		out = append(out, items[i])
	}
	return out
}
