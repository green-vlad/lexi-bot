package seeds

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"

	"lexi-bot/internal/domain/lexicon"
)

// Колонки файла колоды. Порядок фиксирован, но заголовок проверяется
// по именам: перепутанные местами слово и перевод дали бы словарь наизнанку,
// и заметили бы это не скоро.
var headers = [3][]string{
	{"слово", "word", "term"},
	{"перевод", "translation", "meaning"},
	{"пример", "пример из главы", "example"},
}

// byteOrderMark — то, что редакторы таблиц дописывают в начало файла.
const byteOrderMark = "\uFEFF"

// variantSeparators — чем в файле разделяют значения одного слова.
// Косая черта пришла из словарей («профессия / работа»), точка с запятой —
// из выгрузок таблиц.
var variantSeparators = []string{"/", ";"}

// parseWords читает CSV с колодой.
func parseWords(r io.Reader, name string, lang, translationLang lexicon.Language) ([]Word, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = len(headers)
	reader.TrimLeadingSpace = true

	header, err := reader.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: файл пуст", name)
		}
		return nil, fmt.Errorf("%s: заголовок: %w", name, explain(err))
	}
	if err := checkHeader(header, name); err != nil {
		return nil, err
	}

	var (
		words = make([]Word, 0, 64)
		seen  = make(map[string]int)
		line  = 1
	)
	for {
		record, err := reader.Read()
		line++

		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s, строка %d: %w", name, line, explain(err))
		}

		word, err := parseWord(record, line, lang, translationLang)
		if err != nil {
			return nil, fmt.Errorf("%s, строка %d: %w", name, line, err)
		}
		if word == nil {
			// Пустая строка в конце файла — обычное дело для выгрузок.
			continue
		}

		if before, duplicate := seen[word.Lexeme.Term]; duplicate {
			return nil, fmt.Errorf("%s, строка %d: слово %q уже встречалось в строке %d",
				name, line, word.Lexeme.Term, before)
		}
		seen[word.Lexeme.Term] = line

		words = append(words, *word)
		if len(words) > MaxDeckSize {
			return nil, fmt.Errorf("%s: в колоде больше %d слов", name, MaxDeckSize)
		}
	}

	if len(words) == 0 {
		return nil, fmt.Errorf("%s: в колоде нет слов", name)
	}
	return words, nil
}

// explain переводит жалобы разбора CSV на человеческий.
//
// «wrong number of fields» ничего не говорит тому, кто правит словарь
// в редакторе таблиц: ему нужно знать, что колонок должно быть три,
// и какие именно.
func explain(err error) error {
	var parseErr *csv.ParseError
	if errors.As(err, &parseErr) && errors.Is(parseErr.Err, csv.ErrFieldCount) {
		return fmt.Errorf("в строке не %d колонки: ожидались %s", len(headers), columnNames())
	}
	return err
}

// columnNames перечисляет ожидаемые колонки — по первому имени каждой.
func columnNames() string {
	names := make([]string, 0, len(headers))
	for _, known := range headers {
		names = append(names, known[0])
	}
	return strings.Join(names, ", ")
}

// checkHeader убеждается, что колонки на своих местах.
func checkHeader(header []string, name string) error {
	for i, known := range headers {
		// Метка порядка байтов приезжает из редакторов таблиц и портит
		// первое имя колонки, оставаясь невидимой глазу.
		got := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(header[i], byteOrderMark)))
		if !matches(got, known) {
			return fmt.Errorf("%s: колонка %d называется %q, ожидалось одно из %v",
				name, i+1, header[i], known)
		}
	}
	return nil
}

func matches(got string, known []string) bool {
	for _, want := range known {
		if got == want || strings.HasPrefix(got, want) {
			return true
		}
	}
	return false
}

// parseWord превращает строку файла в слово с переводами.
//
// Возвращает nil для пустой строки: обрывать на ней загрузку словаря —
// значит требовать от переводчика аккуратности, которая ни на что не влияет.
func parseWord(record []string, line int, lang, translationLang lexicon.Language) (*Word, error) {
	term := strings.TrimSpace(record[0])
	rawTranslation := strings.TrimSpace(record[1])
	example := strings.TrimSpace(record[2])

	if term == "" && rawTranslation == "" {
		return nil, nil
	}
	if rawTranslation == "" {
		return nil, fmt.Errorf("у слова %q нет перевода", term)
	}

	lexeme, err := lexicon.NewLexeme(lexicon.LexemeParams{
		Lang:    lang,
		Term:    term,
		Example: example,
		// Порядок в файле и есть порядок изучения: в учебнике слова идут
		// по темам, и переставлять их по частотности значило бы ломать урок.
		FreqRank: line - 1,
	})
	if err != nil {
		return nil, err
	}

	translations, err := parseTranslations(rawTranslation, translationLang)
	if err != nil {
		return nil, fmt.Errorf("слово %q: %w", term, err)
	}

	return &Word{Lexeme: lexeme, Translations: translations, Line: line}, nil
}

// parseTranslations разбирает перевод на значения.
//
// «профессия / работа» — это два допустимых ответа, а не один длинный:
// человек, написавший «работа», ответил правильно. Уточнение в скобках
// («офис (место работы)») — примечание, а не часть ответа: печатать
// «место работы» никто не должен.
func parseTranslations(raw string, lang lexicon.Language) ([]lexicon.Translation, error) {
	parts := []string{raw}
	for _, separator := range variantSeparators {
		var next []string
		for _, part := range parts {
			next = append(next, strings.Split(part, separator)...)
		}
		parts = next
	}

	translations := make([]lexicon.Translation, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		text, note := splitNote(part)
		if text == "" {
			continue
		}
		if seen[strings.ToLower(text)] {
			continue
		}
		seen[strings.ToLower(text)] = true

		translation, err := lexicon.NewTranslation(lexicon.TranslationParams{
			// Идентификатор лексемы проставит сидер, когда сохранит её:
			// на этапе разбора его ещё нет.
			LexemeID:  lexicon.LexemeID(1),
			Lang:      lang,
			Text:      text,
			Note:      note,
			IsPrimary: len(translations) == 0,
		})
		if err != nil {
			return nil, err
		}
		translations = append(translations, translation)
	}

	if len(translations) == 0 {
		return nil, errors.New("перевод пуст")
	}
	if len(translations) > MaxTranslations {
		return nil, fmt.Errorf("значений %d при пределе %d: похоже, в перевод попало толкование",
			len(translations), MaxTranslations)
	}
	return translations, nil
}

// splitNote отделяет уточнение в скобках от самого перевода.
func splitNote(part string) (text, note string) {
	text = strings.TrimSpace(part)

	open := strings.LastIndex(text, "(")
	if open < 0 || !strings.HasSuffix(text, ")") {
		return text, ""
	}

	note = strings.TrimSpace(text[open+1 : len(text)-1])
	return strings.TrimSpace(text[:open]), note
}
