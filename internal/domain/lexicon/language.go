package lexicon

import (
	"fmt"
	"strings"
)

// MaxLanguageLen — предел длины кода языка. Реальные теги BCP-47, которые нас
// интересуют, короче в разы; ограничение нужно, чтобы отсечь мусор на входе.
const MaxLanguageLen = 35

// Language — код языка по BCP-47 в каноничном написании: основной субтег
// строчными, письменность с заглавной, регион прописными (ko, en, pt-BR,
// zh-Hans-CN). Нулевое значение означает «язык не задан».
//
// Тип сравним оператором ==, потому что канонизация выполняется один раз при
// разборе: два написания одного языка дают одно и то же значение.
type Language struct {
	code string
}

// ParseLanguage разбирает и канонизирует код языка.
//
// Проверяется корректность формы тега, а не существование языка: реестр IANA в
// домен не тянем, набор доступных языков задаётся таблицей languages в базе.
// Расширения и приватные субтеги (en-x-custom) отвергаются — коды языков служат
// ключами в базе и в файлах локалей, и им положено быть простыми.
func ParseLanguage(s string) (Language, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Language{}, fmt.Errorf("lang_code: %w", ErrRequired)
	}
	if len(s) > MaxLanguageLen {
		return Language{}, fmt.Errorf("lang_code: %w (не более %d символов)", ErrTooLong, MaxLanguageLen)
	}

	parts := strings.Split(s, "-")
	for _, p := range parts {
		if len(p) == 1 {
			return Language{}, fmt.Errorf("lang_code %q: %w (расширения BCP-47 не поддерживаются)", s, ErrInvalid)
		}
		if p == "" || len(p) > 8 || !isASCIIAlnum(p) {
			return Language{}, fmt.Errorf("lang_code %q: %w (субтег должен состоять из 2–8 латинских букв и цифр)", s, ErrInvalid)
		}
	}

	// Основной субтег: только буквы.
	if !isASCIIAlpha(parts[0]) {
		return Language{}, fmt.Errorf("lang_code %q: %w (основной субтег должен состоять из букв)", s, ErrInvalid)
	}
	canon := make([]string, 0, len(parts))
	canon = append(canon, strings.ToLower(parts[0]))

	i := 1
	// Письменность: четыре буквы, записывается с заглавной (Hans, Cyrl).
	if i < len(parts) && len(parts[i]) == 4 && isASCIIAlpha(parts[i]) {
		canon = append(canon, strings.ToUpper(parts[i][:1])+strings.ToLower(parts[i][1:]))
		i++
	}
	// Регион: две буквы (RU) или три цифры (419).
	if i < len(parts) && ((len(parts[i]) == 2 && isASCIIAlpha(parts[i])) || (len(parts[i]) == 3 && isASCIIDigits(parts[i]))) {
		canon = append(canon, strings.ToUpper(parts[i]))
		i++
	}
	// Варианты: 5–8 знаков либо 4 знака, начинающихся с цифры (1901, rozaj).
	for ; i < len(parts); i++ {
		p := parts[i]
		wellFormed := len(p) >= 5 || (len(p) == 4 && isASCIIDigits(p[:1]))
		if !wellFormed {
			return Language{}, fmt.Errorf("lang_code %q: %w (субтег %q не на своём месте)", s, ErrInvalid, p)
		}
		canon = append(canon, strings.ToLower(p))
	}

	return Language{code: strings.Join(canon, "-")}, nil
}

// MustParseLanguage — ParseLanguage для констант и таблиц в тестах: паникует
// вместо возврата ошибки. В обработчиках пользовательского ввода не применять.
func MustParseLanguage(s string) Language {
	lang, err := ParseLanguage(s)
	if err != nil {
		panic(err)
	}
	return lang
}

// String возвращает каноничный код языка; у нулевого значения — пустую строку.
func (l Language) String() string { return l.code }

// IsZero сообщает, что язык не задан.
func (l Language) IsZero() bool { return l.code == "" }

// Base возвращает основной субтег без письменности, региона и вариантов:
// у pt-BR это pt. По нему выбирается файл локали и правила нормализации ответа.
func (l Language) Base() string {
	if base, _, found := strings.Cut(l.code, "-"); found {
		return base
	}
	return l.code
}

// MarshalText отдаёт каноничный код: Language одинаково выглядит в JSON,
// в YAML-описаниях колод и в параметрах SQL-запросов.
func (l Language) MarshalText() ([]byte, error) { return []byte(l.code), nil }

// UnmarshalText разбирает код языка теми же правилами, что и ParseLanguage.
func (l *Language) UnmarshalText(text []byte) error {
	lang, err := ParseLanguage(string(text))
	if err != nil {
		return err
	}
	*l = lang
	return nil
}

func isASCIIAlpha(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}

func isASCIIDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isASCIIAlnum(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		isLetter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		isDigit := c >= '0' && c <= '9'
		if !isLetter && !isDigit {
			return false
		}
	}
	return true
}
