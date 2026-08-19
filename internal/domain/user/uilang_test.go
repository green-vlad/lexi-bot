package user_test

import (
	"errors"
	"testing"

	"lexi-bot/internal/domain/user"
)

func TestParseUILang(t *testing.T) {
	t.Parallel()

	valid := []struct {
		in   string
		want user.UILang
	}{
		{"ru", user.UILangRU},
		{"RU", user.UILangRU},
		{"  en ", user.UILangEN},
	}
	for _, tt := range valid {
		in, want := tt.in, tt.want

		got, err := user.ParseUILang(in)
		if err != nil {
			t.Fatalf("ParseUILang(%q) вернул ошибку: %v", in, err)
		}
		if got != want {
			t.Errorf("ParseUILang(%q) = %q, ожидалось %q", in, got, want)
		}
	}

	tests := []struct {
		in   string
		want error
	}{
		{"", user.ErrRequired},
		{"   ", user.ErrRequired},
		{"ko", user.ErrInvalid},    // язык изучения, но не язык интерфейса
		{"ru-RU", user.ErrInvalid}, // региональные варианты не поддерживаем
		{"русский", user.ErrInvalid},
	}
	for _, tt := range tests {
		if _, err := user.ParseUILang(tt.in); !errors.Is(err, tt.want) {
			t.Errorf("ParseUILang(%q) = %v, ожидалась ошибка %v", tt.in, err, tt.want)
		}
	}
}

func TestMatchUILang(t *testing.T) {
	t.Parallel()

	// language_code из Telegram приходит и с регионом, и без него.
	tests := []struct {
		in      string
		want    user.UILang
		matched bool
	}{
		{"ru", user.UILangRU, true},
		{"ru-RU", user.UILangRU, true},
		{"EN-us", user.UILangEN, true},
		{"  en  ", user.UILangEN, true},
		{"ko", user.DefaultUILang, false},
		{"", user.DefaultUILang, false},
	}

	for _, tt := range tests {
		got, matched := user.MatchUILang(tt.in)
		if got != tt.want || matched != tt.matched {
			t.Errorf("MatchUILang(%q) = %q, %t; ожидалось %q, %t", tt.in, got, matched, tt.want, tt.matched)
		}
	}
}

func TestSupportedUILangs(t *testing.T) {
	t.Parallel()

	langs := user.SupportedUILangs()
	if len(langs) < 2 {
		t.Fatalf("ожидалось не меньше двух языков интерфейса, получено %d", len(langs))
	}
	for _, l := range langs {
		if !l.IsSupported() {
			t.Errorf("язык %q из списка не считается поддерживаемым", l)
		}
	}
	if !user.DefaultUILang.IsSupported() {
		t.Error("язык по умолчанию обязан быть поддерживаемым")
	}

	// Список отдаётся копией: испортить его снаружи нельзя.
	langs[0] = user.UILang("xx")
	if user.SupportedUILangs()[0] == user.UILang("xx") {
		t.Error("SupportedUILangs() отдаёт изменяемый внутренний список")
	}
}
