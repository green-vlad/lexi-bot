// Package importcsv разбирает CSV-файлы, которые пользователь присылает
// в /import.
//
// Разбор частичный и в этом главное отличие от сидов: словарь бота грузит
// разработчик, и первая же кривая строка означает, что файл надо чинить
// целиком. Файл пользователя чинить некому — он собран руками в редакторе
// таблиц, и одна опечатка в трёхсотой строке не повод отвергнуть остальные
// двести девяносто девять. Поэтому годные строки возвращаются, а негодные
// складываются в отчёт с номерами.
package importcsv

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/usecase/port"
)

// Пределы файла.
const (
	// MaxFileSize — два мегабайта. Файл словаря на пять тысяч слов
	// с примерами занимает около полутора.
	MaxFileSize = 2 << 20
	// MaxRows — пять тысяч строк. Больше — это уже не «свои слова»,
	// а чужой словарь, и грузить его надо сидером.
	MaxRows = 5000
)

// Ошибки файла целиком. В отличие от построчных, они означают, что читать
// нечего: разбирать по строкам нечитаемый файл бессмысленно.
var (
	// ErrTooLarge — файл больше MaxFileSize.
	ErrTooLarge = errors.New("файл больше двух мегабайт")
	// ErrTooManyRows — строк больше MaxRows.
	ErrTooManyRows = fmt.Errorf("в файле больше %d строк", MaxRows)
	// ErrNotUTF8 — файл не в UTF-8.
	ErrNotUTF8 = errors.New("файл не в кодировке UTF-8")
	// ErrEmpty — в файле нет ни одной строки.
	ErrEmpty = errors.New("файл пуст")
	// ErrNoHeader — в первой строке нет обязательных колонок.
	ErrNoHeader = errors.New("в заголовке нет обязательных колонок")
)

// byteOrderMark — то, что редакторы таблиц дописывают в начало файла.
// Глазу он невидим, а первое имя колонки портит.
const byteOrderMark = "\uFEFF"

// separators — разделители, между которыми выбирает автоопределение.
var separators = []rune{',', ';'}

// variantSeparators — чем разделяют значения одного слова внутри ячейки.
// Те же, что в словарных файлах: косая черта из словарей, точка с запятой
// из выгрузок таблиц.
var variantSeparators = []string{"/", ";"}

// columns — имена колонок и их синонимы. Колонки ищутся по именам, а не
// по местам: человек собирает файл сам, и требовать от него точного порядка
// значило бы отвергать файлы, в которых всё есть.
var columns = []struct {
	names    []string
	required bool
}{
	{names: []string{"term", "слово", "word"}, required: true},
	{names: []string{"translation", "перевод", "meaning"}, required: true},
	{names: []string{"reading", "чтение", "транскрипция"}},
	{names: []string{"pos", "часть речи"}},
	{names: []string{"example", "пример"}},
}

// Индексы колонок в порядке объявления.
const (
	colTerm = iota
	colTranslation
	colReading
	colPOS
	colExample
)

// Row — разобранная строка файла.
type Row struct {
	// Line — номер строки в файле, считая заголовок: человек будет искать
	// её глазами именно по нему.
	Line         int
	Term         string
	Translations []string
	Reading      string
	POS          lexicon.PartOfSpeech
	Example      string
}

// Result — что вышло из файла.
type Result struct {
	// Rows — строки, которые удалось разобрать.
	Rows []Row
	// Errors — отвергнутые строки с номерами и причинами.
	Errors []port.ImportError
	// Total — сколько строк с данными было в файле. Пустые не считаются:
	// человек их не писал, их дописал редактор таблиц.
	Total int
}

// Parse читает файл со словами пользователя.
//
// Ошибка возвращается только тогда, когда испорчен весь файл: он слишком
// велик, не в UTF-8 или у него нет заголовка. Всё остальное — построчно,
// в Result.Errors.
func Parse(r io.Reader) (Result, error) {
	data, err := read(r)
	if err != nil {
		return Result{}, err
	}

	reader := csv.NewReader(bytes.NewReader(data))
	reader.Comma = detectSeparator(data)
	// Рваные строки разбираются, а не обрывают чтение: строка не с тем
	// числом колонок — это ошибка одной строки, а не всего файла.
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	reader.LazyQuotes = true

	header, err := reader.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return Result{}, ErrEmpty
		}
		return Result{}, fmt.Errorf("заголовок: %w", err)
	}

	index, err := mapColumns(header)
	if err != nil {
		return Result{}, err
	}

	return readRows(reader, index)
}

// read вычитывает файл целиком, проверяя размер и кодировку.
//
// Целиком потому, что разделитель определяется по заголовку, а решение
// о кодировке — по всему содержимому: узнать это, не прочитав файл,
// нельзя, а два мегабайта в памяти не жалко.
func read(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("прочитать файл: %w", err)
	}
	if len(data) > MaxFileSize {
		return nil, ErrTooLarge
	}

	data = bytes.TrimPrefix(data, []byte(byteOrderMark))
	if !utf8.Valid(data) {
		// Обычно это CSV, сохранённый в windows-1251: разбирать его как
		// UTF-8 значит получить абракадабру вместо слов.
		return nil, ErrNotUTF8
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, ErrEmpty
	}
	return data, nil
}

// detectSeparator выбирает разделитель по первой строке.
//
// Считаются только знаки вне кавычек: в «"человек;люди",saram» точка
// с запятой стоит внутри значения и разделителем не является.
func detectSeparator(data []byte) rune {
	line, _, _ := strings.Cut(string(data), "\n")

	counts := make(map[rune]int, len(separators))
	quoted := false
	for _, r := range line {
		if r == '"' {
			quoted = !quoted
			continue
		}
		if !quoted {
			for _, sep := range separators {
				if r == sep {
					counts[sep]++
				}
			}
		}
	}

	best, bestCount := separators[0], counts[separators[0]]
	for _, sep := range separators[1:] {
		if counts[sep] > bestCount {
			best, bestCount = sep, counts[sep]
		}
	}
	return best
}

// mapColumns находит колонки в заголовке.
func mapColumns(header []string) ([]int, error) {
	index := make([]int, len(columns))
	for i := range index {
		index[i] = -1
	}

	for at, cell := range header {
		name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(cell, byteOrderMark)))
		for i, column := range columns {
			if index[i] < 0 && matches(name, column.names) {
				index[i] = at
			}
		}
	}

	for i, column := range columns {
		if column.required && index[i] < 0 {
			return nil, fmt.Errorf("%w: нет колонки %q", ErrNoHeader, column.names[0])
		}
	}
	return index, nil
}

func matches(got string, names []string) bool {
	for _, want := range names {
		if got == want {
			return true
		}
	}
	return false
}

// readRows разбирает строки с данными.
func readRows(reader *csv.Reader, index []int) (Result, error) {
	var result Result

	line := 1
	for {
		record, err := reader.Read()
		line++

		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// Строка не разобралась совсем — например, из-за кавычки
			// посреди значения. Остальные от этого не страдают.
			result.Total++
			result.Errors = append(result.Errors, port.ImportError{
				Line: line, Reason: explain(err),
			})
			continue
		}
		if empty(record) {
			// Пустая строка в конце файла — обычное дело для выгрузок,
			// и ошибкой она не является.
			continue
		}

		result.Total++
		if result.Total > MaxRows {
			return Result{}, ErrTooManyRows
		}

		row, err := parseRow(record, index, line)
		if err != nil {
			result.Errors = append(result.Errors, port.ImportError{
				Line: line, Reason: err.Error(),
			})
			continue
		}
		result.Rows = append(result.Rows, row)
	}
	return result, nil
}

// parseRow превращает строку файла в слово с переводами.
func parseRow(record []string, index []int, line int) (Row, error) {
	term := strings.TrimSpace(cell(record, index[colTerm]))
	if term == "" {
		return Row{}, errors.New("пустое слово")
	}
	if len([]rune(term)) > lexicon.MaxTermLen {
		return Row{}, fmt.Errorf("слово длиннее %d символов", lexicon.MaxTermLen)
	}

	translations := splitVariants(cell(record, index[colTranslation]))
	if len(translations) == 0 {
		return Row{}, errors.New("пустой перевод")
	}
	for _, text := range translations {
		if len([]rune(text)) > lexicon.MaxTranslationLen {
			return Row{}, fmt.Errorf("перевод длиннее %d символов", lexicon.MaxTranslationLen)
		}
	}

	pos, err := lexicon.ParsePartOfSpeech(cell(record, index[colPOS]))
	if err != nil {
		// Часть речи украшает карточку, но опечатку в ней стоит показать:
		// иначе человек так и не узнает, почему разметка не сработала.
		return Row{}, fmt.Errorf("часть речи: %w", err)
	}

	return Row{
		Line:         line,
		Term:         term,
		Translations: translations,
		Reading:      strings.TrimSpace(cell(record, index[colReading])),
		POS:          pos,
		Example:      strings.TrimSpace(cell(record, index[colExample])),
	}, nil
}

// cell достаёт ячейку: отсутствующая колонка и пустая — одно и то же.
func cell(record []string, at int) string {
	if at < 0 || at >= len(record) {
		return ""
	}
	return record[at]
}

// splitVariants делит ячейку на значения одного слова.
func splitVariants(text string) []string {
	parts := []string{text}
	for _, sep := range variantSeparators {
		var next []string
		for _, part := range parts {
			next = append(next, strings.Split(part, sep)...)
		}
		parts = next
	}

	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// empty сообщает, что в строке нет ничего, кроме пробелов.
func empty(record []string) bool {
	for _, field := range record {
		if strings.TrimSpace(field) != "" {
			return false
		}
	}
	return true
}

// explain переводит жалобы разбора CSV на человеческий.
func explain(err error) string {
	var parseErr *csv.ParseError
	if errors.As(err, &parseErr) {
		return parseErr.Err.Error()
	}
	return err.Error()
}
