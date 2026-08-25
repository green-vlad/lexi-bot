// Package lexicon описывает словарную часть домена: языки, лексемы, их переводы
// и колоды, из которых собираются учебные курсы.
//
// Ключевое решение модели — лексема хранится ровно один раз, а переводы вынесены
// в отдельную сущность с собственным языком. Благодаря этому один корейский
// частотный список обслуживает и корейско-русский, и корейско-английский курс:
// новый язык перевода добавляется строками в translations, а не копией колоды.
//
// Пакет ничего не знает ни о базе, ни о Telegram, ни о текущем времени: здесь
// только значения и правила их построения. Конструкторы New* — единственный
// поддерживаемый способ получить корректную сущность из пользовательского ввода;
// значения, восстановленные из хранилища, проверяются методом Validate.
package lexicon

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Ограничения длины текстовых полей в рунах (не в байтах: слово из десяти
// корейских слогов должно проходить так же, как из десяти латинских букв).
const (
	MaxTermLen        = 200
	MaxExampleLen     = 500
	MaxReadingLen     = 200
	MaxTranslationLen = 300
	MaxNoteLen        = 500
	MaxTitleLen       = 100
	MaxDescriptionLen = 500
	MaxDeckCodeLen    = 64
)

// cleanText приводит текст к каноничному виду: убирает краевые пробелы,
// схлопывает внутренние пробельные последовательности в один пробел и отвергает
// управляющие символы. Пустая строка допустима — её отсекает requireText.
//
// Здесь намеренно нет нормализации Unicode (NFC): она нужна для сравнения
// ответов пользователя и появится вместе с ним, в domain/lexicon (T-010).
func cleanText(field, s string, maxRunes int) (string, error) {
	var b strings.Builder
	b.Grow(len(s))

	pendingSpace := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			pendingSpace = b.Len() > 0
		case unicode.IsControl(r):
			return "", fmt.Errorf("%s: %w (управляющий символ)", field, ErrInvalid)
		default:
			if pendingSpace {
				b.WriteRune(' ')
				pendingSpace = false
			}
			b.WriteRune(r)
		}
	}

	out := b.String()
	if utf8.RuneCountInString(out) > maxRunes {
		return "", fmt.Errorf("%s: %w (не более %d символов)", field, ErrTooLong, maxRunes)
	}
	return out, nil
}

// requireText — cleanText для поля, которое не может быть пустым.
func requireText(field, s string, maxRunes int) (string, error) {
	out, err := cleanText(field, s, maxRunes)
	if err != nil {
		return "", err
	}
	if out == "" {
		return "", fmt.Errorf("%s: %w", field, ErrRequired)
	}
	return out, nil
}
