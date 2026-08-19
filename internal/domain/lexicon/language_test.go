package lexicon_test

import (
	"errors"
	"testing"

	"lexi-bot/internal/domain/lexicon"
)

func TestParseLanguageCanonical(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"простой код", "ru", "ru"},
		{"верхний регистр приводится к нижнему", "KO", "ko"},
		{"краевые пробелы отбрасываются", "  en  ", "en"},
		{"регион записывается прописными", "pt-br", "pt-BR"},
		{"письменность записывается с заглавной", "zh-hans", "zh-Hans"},
		{"письменность и регион вместе", "ZH-HANS-cn", "zh-Hans-CN"},
		{"числовой регион", "es-419", "es-419"},
		{"вариант из цифр", "de-CH-1901", "de-CH-1901"},
		{"буквенный вариант", "sl-rozaj", "sl-rozaj"},
		{"трёхбуквенный основной субтег", "haw", "haw"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			lang, err := lexicon.ParseLanguage(tt.in)
			if err != nil {
				t.Fatalf("ParseLanguage(%q) вернул ошибку: %v", tt.in, err)
			}
			if got := lang.String(); got != tt.want {
				t.Errorf("ParseLanguage(%q) = %q, ожидалось %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseLanguageErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want error
	}{
		{"пустая строка", "", lexicon.ErrRequired},
		{"только пробелы", "   ", lexicon.ErrRequired},
		{"слишком длинный код", "ru-Cyrl-RU-variant1-variant2-variant3", lexicon.ErrTooLong},
		{"односимвольный субтег", "r", lexicon.ErrInvalid},
		{"цифры в основном субтеге", "r2", lexicon.ErrInvalid},
		{"подчёркивание вместо дефиса", "ru_RU", lexicon.ErrInvalid},
		{"висячий дефис", "ru-", lexicon.ErrInvalid},
		{"двойной дефис", "ru--RU", lexicon.ErrInvalid},
		{"дефис в начале", "-ru", lexicon.ErrInvalid},
		{"расширение", "en-x-custom", lexicon.ErrInvalid},
		{"трёхбуквенный регион", "en-ZZZ", lexicon.ErrInvalid},
		{"субтег длиннее восьми знаков", "en-variantvariant", lexicon.ErrInvalid},
		{"не латиница", "рус", lexicon.ErrInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			lang, err := lexicon.ParseLanguage(tt.in)
			if !errors.Is(err, tt.want) {
				t.Fatalf("ParseLanguage(%q) = %v, ожидалась ошибка %v", tt.in, err, tt.want)
			}
			if !lang.IsZero() {
				t.Errorf("при ошибке возвращён непустой язык %q", lang)
			}
		})
	}
}

func TestLanguageZeroValue(t *testing.T) {
	t.Parallel()

	var zero lexicon.Language
	if !zero.IsZero() {
		t.Error("нулевое значение должно считаться незаданным языком")
	}
	if zero.String() != "" {
		t.Errorf("String() = %q, ожидалась пустая строка", zero.String())
	}
	if lexicon.MustParseLanguage("ru").IsZero() {
		t.Error("разобранный язык не должен считаться нулевым")
	}
}

func TestLanguageComparable(t *testing.T) {
	t.Parallel()

	// Канонизация при разборе — то, ради чего Language сравним оператором ==.
	if lexicon.MustParseLanguage("PT-br") != lexicon.MustParseLanguage("pt-BR") {
		t.Error("разные написания одного языка должны давать одно значение")
	}
	if lexicon.MustParseLanguage("ru") == lexicon.MustParseLanguage("en") {
		t.Error("разные языки не должны совпадать")
	}
}

func TestLanguageBase(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"ru":         "ru",
		"pt-BR":      "pt",
		"zh-Hans-CN": "zh",
		"de-CH-1901": "de",
	}

	for in, want := range tests {
		if got := lexicon.MustParseLanguage(in).Base(); got != want {
			t.Errorf("Base(%q) = %q, ожидалось %q", in, got, want)
		}
	}

	var zero lexicon.Language
	if got := zero.Base(); got != "" {
		t.Errorf("Base() нулевого значения = %q, ожидалась пустая строка", got)
	}
}

func TestLanguageTextRoundTrip(t *testing.T) {
	t.Parallel()

	for _, code := range []string{"ru", "en", "pt-BR", "zh-Hans-CN"} {
		text, err := lexicon.MustParseLanguage(code).MarshalText()
		if err != nil {
			t.Fatalf("MarshalText(%q) вернул ошибку: %v", code, err)
		}

		var back lexicon.Language
		if err := back.UnmarshalText(text); err != nil {
			t.Fatalf("UnmarshalText(%q) вернул ошибку: %v", text, err)
		}
		if back.String() != code {
			t.Errorf("после цикла marshal/unmarshal получено %q, ожидалось %q", back, code)
		}
	}
}

func TestLanguageUnmarshalTextInvalid(t *testing.T) {
	t.Parallel()

	lang := lexicon.MustParseLanguage("ru")
	if err := lang.UnmarshalText([]byte("ru_RU")); !errors.Is(err, lexicon.ErrInvalid) {
		t.Fatalf("UnmarshalText вернул %v, ожидалась ошибка ErrInvalid", err)
	}
	if lang.String() != "ru" {
		t.Errorf("при ошибке значение изменилось на %q", lang)
	}
}

func TestMustParseLanguagePanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("MustParseLanguage не паникует на некорректном коде")
		}
	}()
	lexicon.MustParseLanguage("ru_RU")
}
