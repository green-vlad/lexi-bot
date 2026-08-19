package user

import (
	"fmt"
	"strings"
)

// UILang — язык интерфейса бота. Набор закрытый и совпадает с набором файлов
// в locales/: предлагать пользователю язык, которого нет в каталоге переводов,
// нельзя. Новый язык добавляется строкой в этом файле и файлом локали.
type UILang string

// Поддерживаемые языки интерфейса.
const (
	UILangRU UILang = "ru"
	UILangEN UILang = "en"
)

// DefaultUILang достаётся пользователю, чей язык в Telegram нам неизвестен.
const DefaultUILang = UILangEN

// uiLangs перечислены в порядке показа в меню выбора языка.
var uiLangs = []UILang{UILangRU, UILangEN}

// SupportedUILangs возвращает языки интерфейса в порядке показа пользователю.
func SupportedUILangs() []UILang {
	out := make([]UILang, len(uiLangs))
	copy(out, uiLangs)
	return out
}

// ParseUILang разбирает код языка интерфейса и требует, чтобы он поддерживался.
func ParseUILang(s string) (UILang, error) {
	lang := UILang(strings.ToLower(strings.TrimSpace(s)))
	if lang == "" {
		return "", fmt.Errorf("ui_lang: %w", ErrRequired)
	}
	if !lang.IsSupported() {
		return "", fmt.Errorf("ui_lang %q: %w", s, ErrInvalid)
	}
	return lang, nil
}

// MatchUILang подбирает язык интерфейса по коду из Telegram (language_code
// приходит в виде ru, en-US, pt-BR) и сообщает, удалось ли это.
//
// Смотрим только на основной субтег: региональные варианты одного языка нам
// безразличны, отдельного перевода для en-GB не будет. Если язык не поддержан,
// возвращается DefaultUILang и false — вызывающий код решает, спросить ли
// пользователя явно.
func MatchUILang(code string) (UILang, bool) {
	base, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(code)), "-")
	if lang := UILang(base); lang.IsSupported() {
		return lang, true
	}
	return DefaultUILang, false
}

// IsSupported сообщает, что для языка есть файл локали.
func (l UILang) IsSupported() bool {
	for _, known := range uiLangs {
		if l == known {
			return true
		}
	}
	return false
}

// String возвращает код языка.
func (l UILang) String() string { return string(l) }
