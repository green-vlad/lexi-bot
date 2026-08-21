package lexicon

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// MinTypoLength — с какой длины слова прощается опечатка.
//
// На коротких словах допуск в один символ разрушителен: «дом» и «дон», «cat»
// и «car» — разные слова, а не промах пальцем. Порог в четыре символа
// оставляет опечатки там, где они действительно опечатки.
const MinTypoLength = 4

// MaxTypoDistance — сколько правок считается опечаткой, а не другим ответом.
const MaxTypoDistance = 1

// Match — насколько ответ пользователя совпал с ожидаемым переводом.
// Значения упорядочены по возрастанию качества: чем больше, тем лучше ответ.
type Match uint8

// Степени совпадения.
const (
	// MatchNone — ответ не засчитан.
	MatchNone Match = iota
	// MatchTypo — расхождение на одну правку: похоже на промах по клавише.
	MatchTypo
	// MatchNormalized — совпало после отбрасывания артиклей.
	MatchNormalized
	// MatchExact — совпало полностью. Регистр, лишние пробелы и пунктуация
	// не в счёт: ошибками перевода они не являются.
	MatchExact
)

// Accepted сообщает, что ответ засчитан хотя бы частично.
func (m Match) Accepted() bool { return m != MatchNone }

// AnswerCheck — результат проверки ответа.
type AnswerCheck struct {
	Match Match
	// Distance — расстояние Левенштейна до ближайшего допустимого перевода,
	// посчитанное на нормализованных формах.
	Distance int
	// Expected — перевод, с которым совпал ответ, а при промахе — ближайший
	// из допустимых: его показывают пользователю вместе с разбором.
	Expected string
}

// SplitVariants разбивает текст перевода на отдельные значения по «;».
//
// Несколько значений в одной строке приходят из CSV-импорта («дом; здание»),
// и засчитывать нужно любое из них.
func SplitVariants(text string) []string {
	parts := strings.Split(text, ";")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// AcceptedAnswers разворачивает переводы лексемы в плоский список допустимых
// ответов: у одной лексемы их может быть и десяток, и засчитывается любой.
func AcceptedAnswers(translations []Translation) []string {
	out := make([]string, 0, len(translations))
	for _, t := range translations {
		out = append(out, SplitVariants(t.Text)...)
	}
	return out
}

// CheckAnswer сравнивает ответ пользователя со всеми допустимыми переводами
// и возвращает лучшее совпадение.
//
// lang — язык перевода: от него зависит, какие артикли отбрасывать.
func CheckAnswer(answer string, accepted []string, lang Language) AnswerCheck {
	best := AnswerCheck{Distance: -1}

	answerBasic := foldAnswer(answer)
	answerFull := Normalize(answer, lang)

	for _, candidate := range accepted {
		got := compareOne(answerBasic, answerFull, candidate, lang)
		if got.Match > best.Match || best.Distance < 0 ||
			(got.Match == best.Match && got.Distance < best.Distance) {
			best = got
		}
		if best.Match == MatchExact {
			break
		}
	}

	if best.Distance < 0 {
		best.Distance = 0
	}
	return best
}

// compareOne сравнивает ответ с одним переводом.
func compareOne(answerBasic, answerFull, candidate string, lang Language) AnswerCheck {
	result := AnswerCheck{Expected: candidate}

	if answerBasic != "" && answerBasic == foldAnswer(candidate) {
		result.Match = MatchExact
		return result
	}

	candidateFull := Normalize(candidate, lang)
	if answerFull != "" && answerFull == candidateFull {
		result.Match = MatchNormalized
		return result
	}

	result.Distance = Levenshtein(answerFull, candidateFull)
	if result.Distance <= MaxTypoDistance && longEnoughForTypo(answerFull, candidateFull) {
		result.Match = MatchTypo
	}
	return result
}

// longEnoughForTypo проверяет, что слово достаточно длинное, чтобы правка
// в нём была опечаткой, а не другим словом.
func longEnoughForTypo(answer, candidate string) bool {
	return len([]rune(answer)) >= MinTypoLength && len([]rune(candidate)) >= MinTypoLength
}

// Normalize приводит текст к форме, в которой ответы сравниваются:
// NFC → нижний регистр → пунктуация прочь → пробелы схлопнуты → артикли
// и глагольное «to» отброшены.
//
// Нормализация NFC обязательна: «й» можно записать одним кодом или как «и»
// с комбинирующей краткой, выглядят они одинаково, а сравниваются как разные
// строки. То же с корейским письмом, где слог собирается из чамо.
func Normalize(text string, lang Language) string {
	return stripArticles(foldAnswer(text), lang)
}

// foldAnswer убирает всё, что не является ошибкой перевода: приводит текст
// к NFC, опускает регистр, выбрасывает пунктуацию и схлопывает пробелы.
// Ответ, совпавший на этом уровне, считается точным.
//
// Артикли здесь не трогаются: в испанском «la casa» и «casa» различаются
// знанием рода, и засчитывать такой ответ как безупречный неправильно —
// этим занимается Normalize уровнем выше.
func foldAnswer(text string) string {
	return stripPunctuation(strings.ToLower(norm.NFC.String(text)))
}

// stripPunctuation убирает пунктуацию и символы. Апострофы удаляются без следа
// («don't» → «dont»), остальное превращается в пробел, чтобы «well-known»
// и «well known» сравнялись.
func stripPunctuation(text string) string {
	var b strings.Builder
	b.Grow(len(text))

	for _, r := range text {
		switch {
		case isApostrophe(r):
			continue
		case unicode.IsPunct(r) || unicode.IsSymbol(r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func isApostrophe(r rune) bool {
	switch r {
	case '\'', '’', 'ʼ', '‘', '`':
		return true
	default:
		return false
	}
}

// articles — служебные слова, которые отбрасываются в начале ответа.
// Ключ — основной субтег языка перевода: региональные варианты («pt-BR»)
// пользуются тем же набором, что и базовый язык.
//
// Русского и корейского здесь нет намеренно: артиклей в них не существует,
// и отбрасывать в них нечего.
var articles = map[string]map[string]bool{
	"en": {"the": true, "a": true, "an": true, "to": true},
	"es": {"el": true, "la": true, "los": true, "las": true, "un": true, "una": true, "unos": true, "unas": true},
	"pt": {"o": true, "a": true, "os": true, "as": true, "um": true, "uma": true},
	"it": {"il": true, "lo": true, "la": true, "i": true, "gli": true, "le": true, "un": true, "uno": true, "una": true},
	"fr": {"le": true, "la": true, "les": true, "un": true, "une": true, "des": true, "l": true},
	"de": {"der": true, "die": true, "das": true, "den": true, "dem": true, "des": true,
		"ein": true, "eine": true, "einen": true, "einem": true, "einer": true},
}

// stripArticles отбрасывает ведущие артикли и английское «to» у глаголов:
// «the house» и «house» — один и тот же ответ, как и «to run» и «run».
//
// Если от ответа не остаётся ничего, отбрасывание отменяется: пользователь,
// написавший «the», ответил именно это.
func stripArticles(text string, lang Language) string {
	known, ok := articles[lang.Base()]
	if !ok || text == "" {
		return text
	}

	words := strings.Fields(text)
	cut := 0
	for cut < len(words) && known[words[cut]] {
		cut++
	}
	if cut == 0 || cut == len(words) {
		return text
	}
	return strings.Join(words[cut:], " ")
}
