package importcsv_test

import (
	"errors"
	"strings"
	"testing"

	"lexi-bot/internal/adapter/importcsv"
	"lexi-bot/internal/domain/lexicon"
)

func parse(t *testing.T, text string) importcsv.Result {
	t.Helper()

	result, err := importcsv.Parse(strings.NewReader(text))
	if err != nil {
		t.Fatalf("Parse() вернул ошибку: %v", err)
	}
	return result
}

func TestParseReadsWholeRow(t *testing.T) {
	t.Parallel()

	result := parse(t, "term,translation,reading,pos,example\n"+
		"사람,человек;люди,saram,noun,좋은 사람입니다\n")

	if len(result.Errors) != 0 {
		t.Fatalf("ошибки = %+v", result.Errors)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("строк %d, ожидалась одна", len(result.Rows))
	}

	row := result.Rows[0]
	if row.Term != "사람" || row.Reading != "saram" || row.Example != "좋은 사람입니다" {
		t.Errorf("строка = %+v", row)
	}
	if row.POS != lexicon.POSNoun {
		t.Errorf("часть речи = %q, ожидалось noun", row.POS)
	}
	if len(row.Translations) != 2 || row.Translations[0] != "человек" || row.Translations[1] != "люди" {
		t.Errorf("переводы = %v, ожидались оба значения", row.Translations)
	}
	// Номер строки считается вместе с заголовком: по нему человек будет
	// искать ошибку глазами.
	if row.Line != 2 {
		t.Errorf("номер строки = %d, ожидался 2", row.Line)
	}
}

func TestParseAcceptsBothSeparators(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		text string
	}{
		{"запятая", "term,translation\n사람,человек\n"},
		{"точка с запятой", "term;translation\n사람;человек\n"},
		// Разделитель определяется по знакам вне кавычек: внутри значения
		// точка с запятой делит переводы, а не колонки.
		{"точка с запятой внутри значения", "term,translation\n사람,\"человек;люди\"\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := parse(t, tt.text)
			if len(result.Rows) != 1 || result.Rows[0].Term != "사람" {
				t.Fatalf("строки = %+v, ошибки = %+v", result.Rows, result.Errors)
			}
			if result.Rows[0].Translations[0] != "человек" {
				t.Errorf("перевод = %q", result.Rows[0].Translations[0])
			}
		})
	}
}

func TestParseStripsByteOrderMark(t *testing.T) {
	t.Parallel()

	// Редакторы таблиц дописывают метку порядка байтов в начало файла.
	// Глазу она невидима, а первое имя колонки портит.
	result := parse(t, "\uFEFFterm,translation\n사람,человек\n")

	if len(result.Rows) != 1 {
		t.Fatalf("строк %d, ошибки = %+v", len(result.Rows), result.Errors)
	}
	if result.Rows[0].Term != "사람" {
		t.Errorf("слово = %q", result.Rows[0].Term)
	}
}

func TestParseFindsColumnsByName(t *testing.T) {
	t.Parallel()

	// Порядок колонок не фиксирован, имена бывают русские, а лишние
	// колонки просто не нужны.
	result := parse(t, "перевод,заметка,слово\nчеловек,что угодно,사람\n")

	if len(result.Rows) != 1 {
		t.Fatalf("строк %d, ошибки = %+v", len(result.Rows), result.Errors)
	}
	if result.Rows[0].Term != "사람" || result.Rows[0].Translations[0] != "человек" {
		t.Errorf("строка = %+v", result.Rows[0])
	}
}

func TestParseKeepsGoodRowsAmongBad(t *testing.T) {
	t.Parallel()

	result := parse(t, "term,translation\n"+
		"사람,человек\n"+ // строка 2 — годная
		",забыли слово\n"+ // строка 3
		"물,\n"+ // строка 4
		"\n"+ // пустая строка ошибкой не считается
		"불,огонь\n") // строка 6 — годная

	if len(result.Rows) != 2 {
		t.Fatalf("годных строк %d, ожидалось две: %+v", len(result.Rows), result.Rows)
	}
	if result.Rows[0].Term != "사람" || result.Rows[1].Term != "불" {
		t.Errorf("строки = %+v", result.Rows)
	}
	if len(result.Errors) != 2 {
		t.Fatalf("ошибок %d, ожидалось две: %+v", len(result.Errors), result.Errors)
	}
	// В отчёте — номера строк файла, а не порядковые номера ошибок.
	if result.Errors[0].Line != 3 || result.Errors[1].Line != 4 {
		t.Errorf("номера строк = %d и %d, ожидались 3 и 4", result.Errors[0].Line, result.Errors[1].Line)
	}
	// Пустая строка не попала ни в годные, ни в отчёт, ни в счётчик.
	if result.Total != 4 {
		t.Errorf("строк с данными = %d, ожидалось четыре", result.Total)
	}
}

func TestParseReportsShortRow(t *testing.T) {
	t.Parallel()

	// Колонок в строке меньше, чем в заголовке: недостающие считаются
	// пустыми, и решает это проверка обязательных полей.
	result := parse(t, "term,translation,example\n사람,человек\n물\n")

	if len(result.Rows) != 1 || result.Rows[0].Term != "사람" {
		t.Fatalf("годные строки = %+v", result.Rows)
	}
	if len(result.Errors) != 1 || result.Errors[0].Line != 3 {
		t.Fatalf("ошибки = %+v", result.Errors)
	}
	if !strings.Contains(result.Errors[0].Reason, "перевод") {
		t.Errorf("причина = %q, ожидалось про перевод", result.Errors[0].Reason)
	}
}

func TestParseReportsUnknownPartOfSpeech(t *testing.T) {
	t.Parallel()

	result := parse(t, "term,translation,pos\n사람,человек,существительное\n")

	if len(result.Rows) != 0 {
		t.Errorf("строки = %+v, ожидалась отвергнутая", result.Rows)
	}
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0].Reason, "часть речи") {
		t.Errorf("ошибки = %+v", result.Errors)
	}
}

func TestParseReportsTooLongValues(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("а", lexicon.MaxTermLen+1)
	result := parse(t, "term,translation\n"+long+",человек\n사람,"+strings.Repeat("б", lexicon.MaxTranslationLen+1)+"\n")

	if len(result.Rows) != 0 {
		t.Errorf("строки = %+v, обе должны были быть отвергнуты", result.Rows)
	}
	if len(result.Errors) != 2 {
		t.Fatalf("ошибки = %+v", result.Errors)
	}
	if !strings.Contains(result.Errors[0].Reason, "длиннее") || !strings.Contains(result.Errors[1].Reason, "длиннее") {
		t.Errorf("причины = %+v", result.Errors)
	}
}

func TestParseFileErrors(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		text string
		want error
	}{
		{"пустой файл", "", importcsv.ErrEmpty},
		{"одни пробелы", "   \n\n", importcsv.ErrEmpty},
		{"нет колонки слова", "translation,example\nчеловек,пример\n", importcsv.ErrNoHeader},
		{"нет колонки перевода", "term,example\n사람,пример\n", importcsv.ErrNoHeader},
		{"нет заголовка вовсе", "사람,человек\n물,вода\n", importcsv.ErrNoHeader},
		{"не UTF-8", "term,translation\n\xf0\x28\x8c\x28,человек\n", importcsv.ErrNotUTF8},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := importcsv.Parse(strings.NewReader(tt.text))
			if !errors.Is(err, tt.want) {
				t.Errorf("Parse() = %v, ожидалась %v", err, tt.want)
			}
		})
	}
}

func TestParseRejectsTooLargeFile(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	b.WriteString("term,translation\n")
	line := "사람,человек\n"
	for b.Len() <= importcsv.MaxFileSize {
		b.WriteString(line)
	}

	if _, err := importcsv.Parse(strings.NewReader(b.String())); !errors.Is(err, importcsv.ErrTooLarge) {
		t.Errorf("Parse() = %v, ожидалась ErrTooLarge", err)
	}
}

func TestParseRejectsTooManyRows(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	b.WriteString("term,translation\n")
	for i := 0; i <= importcsv.MaxRows; i++ {
		b.WriteString("사람,человек\n")
	}

	// Файл влезает в два мегабайта, но строк в нём больше предела:
	// это уже не «свои слова», а чужой словарь.
	if _, err := importcsv.Parse(strings.NewReader(b.String())); !errors.Is(err, importcsv.ErrTooManyRows) {
		t.Errorf("Parse() = %v, ожидалась ErrTooManyRows", err)
	}
}

func TestParseAcceptsExactlyMaxRows(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	b.WriteString("term,translation\n")
	for i := 0; i < importcsv.MaxRows; i++ {
		b.WriteString("사람,человек\n")
	}

	// Ровно предел — ещё не превышение: границу легко сдвинуть на единицу
	// и отвергать файлы, которые собирали под объявленный лимит.
	result := parse(t, b.String())
	if result.Total != importcsv.MaxRows {
		t.Errorf("строк = %d, ожидалось %d", result.Total, importcsv.MaxRows)
	}
}
