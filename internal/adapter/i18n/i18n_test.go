package i18n_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"lexi-bot/internal/adapter/i18n"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/locales"
)

func newCatalog(t *testing.T) *i18n.Catalog {
	t.Helper()

	catalog, err := i18n.NewCatalog(locales.FS)
	if err != nil {
		t.Fatalf("NewCatalog() вернул ошибку: %v", err)
	}
	return catalog
}

func TestRussianPluralForms(t *testing.T) {
	t.Parallel()

	// Ради этого и берётся библиотека: в русском три формы, и выбирать их
	// самим означало бы зашить правила языка в код.
	ru := newCatalog(t).For(user.UILangRU)

	tests := []struct {
		count int
		want  string
	}{
		{1, "1 карточка ждёт повторения"},
		{2, "2 карточки ждут повторения"},
		{3, "3 карточки ждут повторения"},
		{5, "5 карточек ждут повторения"},
		{11, "11 карточек ждут повторения"},
		{21, "21 карточка ждёт повторения"},
		{22, "22 карточки ждут повторения"},
		{25, "25 карточек ждут повторения"},
		{101, "101 карточка ждёт повторения"},
		{111, "111 карточек ждут повторения"},
		{0, "0 карточек ждут повторения"},
	}

	for _, tt := range tests {
		got, err := ru.Plural("cards.due", tt.count, nil)
		if err != nil {
			t.Fatalf("Plural(%d) вернул ошибку: %v", tt.count, err)
		}
		if got != tt.want {
			t.Errorf("Plural(%d) = %q, ожидалось %q", tt.count, got, tt.want)
		}
	}
}

func TestEnglishPluralForms(t *testing.T) {
	t.Parallel()

	en := newCatalog(t).For(user.UILangEN)

	tests := []struct {
		count int
		want  string
	}{
		{1, "1 card is due"},
		{2, "2 cards are due"},
		{0, "0 cards are due"},
		{21, "21 cards are due"},
	}

	for _, tt := range tests {
		got, err := en.Plural("cards.due", tt.count, nil)
		if err != nil {
			t.Fatalf("Plural(%d) вернул ошибку: %v", tt.count, err)
		}
		if got != tt.want {
			t.Errorf("Plural(%d) = %q, ожидалось %q", tt.count, got, tt.want)
		}
	}
}

func TestNamedPlaceholders(t *testing.T) {
	t.Parallel()

	catalog := newCatalog(t)

	got, err := catalog.For(user.UILangRU).T("start.greeting", port.Args{"Name": "Андрей"})
	if err != nil {
		t.Fatalf("T() вернул ошибку: %v", err)
	}
	if !strings.Contains(got, "Андрей") {
		t.Errorf("T() = %q, подстановка не сработала", got)
	}

	// Подстановки в плюральном сообщении соседствуют с числом.
	withArgs, err := catalog.For(user.UILangEN).T("session.finished", port.Args{"NextAt": "21:30"})
	if err != nil {
		t.Fatalf("T() вернул ошибку: %v", err)
	}
	if !strings.Contains(withArgs, "21:30") {
		t.Errorf("T() = %q, подстановка не сработала", withArgs)
	}
}

func TestUnknownKeyIsAnError(t *testing.T) {
	t.Parallel()

	// Пустая строка ушла бы к пользователю молча и выглядела бы как поломка
	// бота; ошибка попадает в лог и в тест.
	ru := newCatalog(t).For(user.UILangRU)

	got, err := ru.T("нет.такого.ключа", nil)
	if err == nil {
		t.Fatalf("T() для неизвестного ключа вернул %q без ошибки", got)
	}
	if got != "" {
		t.Errorf("при ошибке возвращено %q", got)
	}
	if !strings.Contains(err.Error(), "нет.такого.ключа") {
		t.Errorf("ошибка %v не называет ключ", err)
	}

	if _, err := ru.Plural("нет.такого.ключа", 3, nil); err == nil {
		t.Error("Plural() для неизвестного ключа не вернул ошибку")
	}
}

func TestLangIsReported(t *testing.T) {
	t.Parallel()

	catalog := newCatalog(t)
	for _, lang := range user.SupportedUILangs() {
		if got := catalog.For(lang).Lang(); got != lang {
			t.Errorf("Lang() = %q, ожидалось %q", got, lang)
		}
	}

	// Неизвестный язык не должен оставлять пользователя без ответа.
	fallback := catalog.For(user.UILang("ko"))
	if fallback == nil {
		t.Fatal("For() вернул nil для неподдерживаемого языка")
	}
	if fallback.Lang() != user.DefaultUILang {
		t.Errorf("Lang() = %q, ожидался язык по умолчанию", fallback.Lang())
	}
}

func TestEveryLanguageIsTranslated(t *testing.T) {
	t.Parallel()

	// Ключ должен переводиться на каждый поддерживаемый язык, и переводы
	// должны различаться — иначе в файле забыли строку.
	catalog := newCatalog(t)

	ru, err := catalog.For(user.UILangRU).T("common.cancelled", nil)
	if err != nil {
		t.Fatalf("T() вернул ошибку: %v", err)
	}
	en, err := catalog.For(user.UILangEN).T("common.cancelled", nil)
	if err != nil {
		t.Fatalf("T() вернул ошибку: %v", err)
	}
	if ru == en {
		t.Errorf("перевод на оба языка одинаков (%q): похоже, строку забыли", ru)
	}
}

func TestCatalogRejectsBrokenInput(t *testing.T) {
	t.Parallel()

	if _, err := i18n.NewCatalog(fstest.MapFS{}); err == nil {
		t.Error("пустой каталог переводов должен быть ошибкой")
	}

	// Файл есть, но не для всех поддерживаемых языков: бот молча отвечал бы
	// по-английски тому, кто выбрал русский.
	onlyEnglish := fstest.MapFS{
		"en.toml": &fstest.MapFile{Data: []byte("[common.cancelled]\nother = \"Cancelled.\"\n")},
	}
	if _, err := i18n.NewCatalog(onlyEnglish); err == nil {
		t.Error("отсутствие файла для поддерживаемого языка должно быть ошибкой")
	}

	broken := fstest.MapFS{
		"ru.toml": &fstest.MapFile{Data: []byte("это не toml =\n")},
		"en.toml": &fstest.MapFile{Data: []byte("[common.cancelled]\nother = \"Cancelled.\"\n")},
	}
	if _, err := i18n.NewCatalog(broken); err == nil {
		t.Error("битый файл переводов должен быть ошибкой")
	}
}
