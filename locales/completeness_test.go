package locales_test

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/BurntSushi/toml"

	"lexi-bot/locales"
)

// pluralCategories — формы, которые требует CLDR от языков, на которые
// переведён бот. У русского их четыре, у английского две; язык, добавленный
// в domain/user без строки здесь, уронит тест — и это правильно, иначе
// перевод молча выйдет с недостающими формами.
var pluralCategories = map[string][]string{
	"ru": {"one", "few", "many", "other"},
	"en": {"one", "other"},
}

// messageFields — служебные ключи сообщения go-i18n. Всё остальное внутри
// таблицы означает, что это не сообщение, а вложенная группа.
var messageFields = map[string]bool{
	"description": true, "hash": true, "leftdelim": true, "rightdelim": true,
	"zero": true, "one": true, "two": true, "few": true, "many": true, "other": true,
}

var placeholderRe = regexp.MustCompile(`\{\{\s*\.([A-Za-z0-9_]+)`)

// message — одно сообщение каталога.
type message struct {
	// forms — тексты по категориям: other у обычного сообщения, набор
	// категорий у сообщения с числом.
	forms map[string]string
	// placeholders — все подстановки сообщения, кроме Count: спеллинг числа
	// в отдельной форме — дело перевода («одна карточка» против «1 карточка»).
	placeholders map[string]bool
	plural       bool
}

// catalog — разобранный файл переводов.
type catalog struct {
	name     string
	lang     string
	messages map[string]message
}

func TestLocalesAreComplete(t *testing.T) {
	t.Parallel()

	problems := check(loadCatalogs(t, locales.FS))
	for _, problem := range problems {
		t.Error(problem)
	}
}

func TestMissingKeyIsReported(t *testing.T) {
	t.Parallel()

	// Ровно тот случай, ради которого тест и существует: ключ забыли
	// в одном из файлов, и без проверки бот молча отвечал бы на чужом
	// языке через запасной каталог.
	problems := check(loadCatalogs(t, fstest.MapFS{
		"ru.toml": file(`
			[common.cancelled]
			other = "Отменил."

			[common.error]
			other = "Что-то пошло не так."
		`),
		"en.toml": file(`
			[common.cancelled]
			other = "Cancelled."
		`),
	}))

	requireProblem(t, problems, "common.error", "en.toml")
}

func TestPlaceholderMismatchIsReported(t *testing.T) {
	t.Parallel()

	// Подстановка, потерянная при переводе, оставит пользователя без имени,
	// даты или числа — и заметить это в чате трудно.
	problems := check(loadCatalogs(t, fstest.MapFS{
		"ru.toml": file(`
			[start.greeting]
			other = "Привет, {{.Name}}!"
		`),
		"en.toml": file(`
			[start.greeting]
			other = "Hi there!"
		`),
	}))

	requireProblem(t, problems, "Name", "en.toml")
}

func TestMissingPluralFormIsReported(t *testing.T) {
	t.Parallel()

	// В русском без формы many «5 карточек» превратится в «5 карточки».
	problems := check(loadCatalogs(t, fstest.MapFS{
		"ru.toml": file(`
			[cards.due]
			one = "{{.Count}} карточка"
			few = "{{.Count}} карточки"
			other = "{{.Count}} карточки"
		`),
		"en.toml": file(`
			[cards.due]
			one = "{{.Count}} card"
			other = "{{.Count}} cards"
		`),
	}))

	requireProblem(t, problems, "many", "ru.toml")
}

func TestPluralityMismatchIsReported(t *testing.T) {
	t.Parallel()

	// Сообщение с числом в одном языке и без числа в другом — это разные
	// сообщения, и склеивать их одним ключом нельзя.
	problems := check(loadCatalogs(t, fstest.MapFS{
		"ru.toml": file(`
			[cards.due]
			one = "{{.Count}} карточка"
			few = "{{.Count}} карточки"
			many = "{{.Count}} карточек"
			other = "{{.Count}} карточки"
		`),
		"en.toml": file(`
			[cards.due]
			other = "cards are due"
		`),
	}))

	requireProblem(t, problems, "cards.due", "en.toml")
}

func TestEmptyTranslationIsReported(t *testing.T) {
	t.Parallel()

	problems := check(loadCatalogs(t, fstest.MapFS{
		"ru.toml": file(`
			[common.cancelled]
			other = ""
		`),
		"en.toml": file(`
			[common.cancelled]
			other = "Cancelled."
		`),
	}))

	requireProblem(t, problems, "common.cancelled", "ru.toml")
}

// check сверяет каталоги между собой и возвращает список найденных проблем.
func check(catalogs []catalog) []string {
	var problems []string

	// Эталон — объединение ключей всех файлов: недостающий ключ должен
	// находиться независимо от того, в каком файле он есть.
	all := map[string]bool{}
	for _, c := range catalogs {
		for key := range c.messages {
			all[key] = true
		}
	}

	for _, key := range sorted(all) {
		var present []catalog
		for _, c := range catalogs {
			if _, ok := c.messages[key]; ok {
				present = append(present, c)
				continue
			}
			problems = append(problems, fmt.Sprintf("ключ %q отсутствует в %s", key, c.name))
		}
		if len(present) < 2 {
			continue
		}

		reference := present[0]
		for _, c := range present[1:] {
			problems = append(problems, compare(key, reference, c)...)
		}
	}

	for _, c := range catalogs {
		problems = append(problems, checkForms(c)...)
	}
	return problems
}

// compare сверяет одно сообщение в двух каталогах.
func compare(key string, a, b catalog) []string {
	var problems []string

	first, second := a.messages[key], b.messages[key]
	if first.plural != second.plural {
		withNumber, without := a, b
		if second.plural {
			withNumber, without = b, a
		}
		problems = append(problems, fmt.Sprintf(
			"ключ %q в %s задан с числом, а в %s без числа",
			key, withNumber.name, without.name))
	}

	for _, name := range sorted(first.placeholders) {
		if !second.placeholders[name] {
			problems = append(problems, fmt.Sprintf(
				"подстановка {{.%s}} есть в ключе %q файла %s, но не в %s",
				name, key, a.name, b.name))
		}
	}
	for _, name := range sorted(second.placeholders) {
		if !first.placeholders[name] {
			problems = append(problems, fmt.Sprintf(
				"подстановка {{.%s}} есть в ключе %q файла %s, но не в %s",
				name, key, b.name, a.name))
		}
	}
	return problems
}

// checkForms проверяет сам файл: непустые тексты и полный набор форм
// для сообщений с числом.
func checkForms(c catalog) []string {
	var problems []string

	required, known := pluralCategories[c.lang]
	for _, key := range sortedMessages(c.messages) {
		msg := c.messages[key]

		for _, form := range sortedStrings(msg.forms) {
			if strings.TrimSpace(msg.forms[form]) == "" {
				problems = append(problems, fmt.Sprintf(
					"пустой перевод: ключ %q, форма %q, файл %s", key, form, c.name))
			}
		}

		if !msg.plural {
			continue
		}
		if !known {
			problems = append(problems, fmt.Sprintf(
				"для языка %q не описаны множественные формы (файл %s)", c.lang, c.name))
			continue
		}
		for _, form := range required {
			if _, ok := msg.forms[form]; !ok {
				problems = append(problems, fmt.Sprintf(
					"ключ %q в %s не имеет формы %q", key, c.name, form))
			}
		}
	}
	return problems
}

// loadCatalogs разбирает все файлы переводов.
func loadCatalogs(t *testing.T, fsys fs.FS) []catalog {
	t.Helper()

	names, err := fs.Glob(fsys, "*.toml")
	if err != nil {
		t.Fatalf("поиск файлов переводов не удался: %v", err)
	}
	if len(names) < 2 {
		t.Fatalf("файлов переводов %d, сверять нечего", len(names))
	}
	sort.Strings(names)

	catalogs := make([]catalog, 0, len(names))
	for _, name := range names {
		raw, err := fs.ReadFile(fsys, name)
		if err != nil {
			t.Fatalf("чтение %s не удалось: %v", name, err)
		}

		var tree map[string]any
		if err := toml.Unmarshal(raw, &tree); err != nil {
			t.Fatalf("разбор %s не удался: %v", name, err)
		}

		messages := map[string]message{}
		collect("", tree, messages)
		catalogs = append(catalogs, catalog{
			name:     name,
			lang:     strings.TrimSuffix(path.Base(name), ".toml"),
			messages: messages,
		})
	}
	return catalogs
}

// collect разворачивает вложенные таблицы TOML в плоские ключи с точками —
// так же, как это делает go-i18n при загрузке каталога.
func collect(prefix string, tree map[string]any, out map[string]message) {
	if msg, ok := asMessage(tree); ok && prefix != "" {
		out[prefix] = msg
		return
	}

	for key, value := range tree {
		full := key
		if prefix != "" {
			full = prefix + "." + key
		}

		switch v := value.(type) {
		case map[string]any:
			collect(full, v, out)
		case string:
			// Плоская запись вида key = "текст".
			out[full] = newMessage(map[string]string{"other": v})
		}
	}
}

// asMessage распознаёт таблицу сообщения: у неё только служебные ключи.
func asMessage(tree map[string]any) (message, bool) {
	forms := map[string]string{}
	for key, value := range tree {
		if !messageFields[key] {
			return message{}, false
		}
		text, ok := value.(string)
		if !ok {
			return message{}, false
		}
		if key == "description" || key == "hash" || key == "leftdelim" || key == "rightdelim" {
			continue
		}
		forms[key] = text
	}
	if len(forms) == 0 {
		return message{}, false
	}
	return newMessage(forms), true
}

func newMessage(forms map[string]string) message {
	msg := message{forms: forms, placeholders: map[string]bool{}}
	for form, text := range forms {
		if form != "other" {
			msg.plural = true
		}
		for _, match := range placeholderRe.FindAllStringSubmatch(text, -1) {
			// Count в сравнение не идёт: писать число в конкретной форме
			// или обойтись словом — дело перевода.
			if match[1] != "Count" {
				msg.placeholders[match[1]] = true
			}
		}
	}
	return msg
}

// requireProblem проверяет, что среди найденных проблем есть та самая:
// сообщение обязано называть и ключ, и файл, иначе искать нечего.
func requireProblem(t *testing.T, problems []string, parts ...string) {
	t.Helper()

	for _, problem := range problems {
		found := true
		for _, part := range parts {
			if !strings.Contains(problem, part) {
				found = false
				break
			}
		}
		if found {
			return
		}
	}
	t.Errorf("среди проблем нет упоминания %v; найдено: %v", parts, problems)
}

// file убирает отступы, которыми запись каталога выровнена в тесте.
func file(content string) *fstest.MapFile {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimLeft(line, "\t")
	}
	return &fstest.MapFile{Data: []byte(strings.Join(lines, "\n"))}
}

func sorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func sortedMessages(m map[string]message) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func sortedStrings(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
