package lexicon

import "fmt"

// LexemeID — идентификатор лексемы в хранилище. Ноль означает, что сущность
// ещё не сохранена.
type LexemeID int64

// PartOfSpeech — часть речи. Набор намеренно закрытый и грубый: он нужен не для
// лингвистики, а для подбора правдоподобных дистракторов в режиме выбора из
// четырёх вариантов (T-032) и для CHECK-ограничения в базе.
type PartOfSpeech string

// Известные части речи. Пустое значение допустимо и означает «не размечено».
const (
	POSUnknown      PartOfSpeech = ""
	POSNoun         PartOfSpeech = "noun"
	POSVerb         PartOfSpeech = "verb"
	POSAdjective    PartOfSpeech = "adj"
	POSAdverb       PartOfSpeech = "adv"
	POSPronoun      PartOfSpeech = "pron"
	POSNumeral      PartOfSpeech = "num"
	POSAdposition   PartOfSpeech = "adp" // предлоги и послелоги
	POSConjunction  PartOfSpeech = "conj"
	POSParticle     PartOfSpeech = "part"
	POSInterjection PartOfSpeech = "interj"
	POSPhrase       PartOfSpeech = "phrase"
	POSOther        PartOfSpeech = "other"
)

var knownPOS = map[PartOfSpeech]bool{
	POSUnknown: true, POSNoun: true, POSVerb: true, POSAdjective: true,
	POSAdverb: true, POSPronoun: true, POSNumeral: true, POSAdposition: true,
	POSConjunction: true, POSParticle: true, POSInterjection: true,
	POSPhrase: true, POSOther: true,
}

// ParsePartOfSpeech разбирает часть речи из строки словаря или CSV-файла.
// Регистр и краевые пробелы значения не имеют; пустая строка даёт POSUnknown.
func ParsePartOfSpeech(s string) (PartOfSpeech, error) {
	pos, err := cleanText("pos", s, 16)
	if err != nil {
		return POSUnknown, err
	}
	normalized := PartOfSpeech(lowerASCII(pos))
	if !knownPOS[normalized] {
		return POSUnknown, fmt.Errorf("pos %q: %w", s, ErrInvalid)
	}
	return normalized, nil
}

// IsKnown сообщает, что значение входит в набор допустимых частей речи.
func (p PartOfSpeech) IsKnown() bool { return knownPOS[p] }

// Lexeme — слово или устойчивое выражение изучаемого языка. Переводы хранятся
// отдельно (см. Translation), поэтому одна лексема обслуживает любое число
// языков перевода.
type Lexeme struct {
	ID      LexemeID
	Lang    Language
	Term    string
	Reading string // транскрипция или романизация; пусто, если языку не нужна
	POS     PartOfSpeech
	// Example — слово в живой фразе. Написан на изучаемом языке и потому
	// живёт на лексеме, а не на переводе.
	Example string
	// FreqRank — место в частотном списке, 1 — самое частое слово.
	// Ноль означает «частотность неизвестна» и ставит слово в конец очереди.
	FreqRank int
	// OwnerID — владелец личного слова. Ноль означает встроенную лексему,
	// общую для всех пользователей.
	OwnerID int64
}

// LexemeParams — входные данные конструктора. Структура вместо длинного списка
// аргументов: у лексемы шесть полей, четыре из них необязательные.
type LexemeParams struct {
	Lang     Language
	Term     string
	Reading  string
	Example  string
	POS      PartOfSpeech
	FreqRank int
	OwnerID  int64
}

// NewLexeme нормализует поля и проверяет их. Term и Reading приводятся к
// каноничному виду (краевые пробелы убираются, внутренние схлопываются),
// поэтому «  большой   дом » и «большой дом» дают одну и ту же лексему.
func NewLexeme(p LexemeParams) (Lexeme, error) {
	term, err := requireText("term", p.Term, MaxTermLen)
	if err != nil {
		return Lexeme{}, err
	}
	reading, err := cleanText("reading", p.Reading, MaxReadingLen)
	if err != nil {
		return Lexeme{}, err
	}
	example, err := cleanText("example", p.Example, MaxExampleLen)
	if err != nil {
		return Lexeme{}, err
	}

	lex := Lexeme{
		Lang:     p.Lang,
		Term:     term,
		Reading:  reading,
		Example:  example,
		POS:      p.POS,
		FreqRank: p.FreqRank,
		OwnerID:  p.OwnerID,
	}
	if err := lex.Validate(); err != nil {
		return Lexeme{}, err
	}
	return lex, nil
}

// Validate проверяет инварианты лексемы, ничего не изменяя. Применяется к
// значениям, восстановленным из хранилища, где нормализация уже выполнена.
func (l Lexeme) Validate() error {
	if l.Lang.IsZero() {
		return fmt.Errorf("lang_code: %w", ErrRequired)
	}
	if l.Term == "" {
		return fmt.Errorf("term: %w", ErrRequired)
	}
	if !l.POS.IsKnown() {
		return fmt.Errorf("pos %q: %w", l.POS, ErrInvalid)
	}
	if l.FreqRank < 0 {
		return fmt.Errorf("freq_rank: %w (ожидалось неотрицательное число)", ErrInvalid)
	}
	if l.OwnerID < 0 {
		return fmt.Errorf("owner_user_id: %w (ожидался неотрицательный идентификатор)", ErrInvalid)
	}
	return nil
}

// IsBuiltin сообщает, что лексема пришла из встроенного словаря и видна всем.
func (l Lexeme) IsBuiltin() bool { return l.OwnerID == 0 }

// lowerASCII опускает регистр латиницы, не трогая остальное: коды частей речи
// и слаги колод по определению состоят из ASCII.
func lowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
