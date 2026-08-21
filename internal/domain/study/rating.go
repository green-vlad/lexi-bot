package study

import (
	"fmt"
	"strings"
)

// Rating — оценка ответа, четыре кнопки Anki. Она не всегда приходит от
// пользователя напрямую: в режимах выбора и ввода её выводит RatingResolver
// (T-011) из правильности и скорости ответа.
type Rating uint8

// Оценки. Нумерация с единицы, как в SM-2 и в Anki: ноль означает, что оценка
// не задана, и Validate это ловит.
const (
	// RatingAgain — не вспомнил; карточка возвращается в обучение.
	RatingAgain Rating = iota + 1
	// RatingHard — вспомнил с трудом.
	RatingHard
	// RatingGood — вспомнил.
	RatingGood
	// RatingEasy — вспомнил мгновенно; интервал растёт быстрее обычного.
	RatingEasy
)

var ratingNames = map[Rating]string{
	RatingAgain: "again",
	RatingHard:  "hard",
	RatingGood:  "good",
	RatingEasy:  "easy",
}

// Ratings возвращает все оценки в порядке возрастания — порядок кнопок под
// карточкой.
func Ratings() []Rating {
	return []Rating{RatingAgain, RatingHard, RatingGood, RatingEasy}
}

// ParseRating разбирает оценку из строки — так она приходит из callback_data
// кнопки и из базы.
func ParseRating(s string) (Rating, error) {
	name := strings.ToLower(strings.TrimSpace(s))
	if name == "" {
		return 0, fmt.Errorf("rating: %w", ErrRequired)
	}
	for r, known := range ratingNames {
		if name == known {
			return r, nil
		}
	}
	return 0, fmt.Errorf("rating %q: %w", s, ErrInvalid)
}

// String возвращает имя оценки. У неизвестного значения — диагностическая
// запись вида rating(9): она заметна в логе и не разбирается обратно.
func (r Rating) String() string {
	if name, ok := ratingNames[r]; ok {
		return name
	}
	return fmt.Sprintf("rating(%d)", uint8(r))
}

// IsValid сообщает, что оценка входит в набор допустимых.
func (r Rating) IsValid() bool {
	_, ok := ratingNames[r]
	return ok
}

// Failed сообщает, что ответ провален: карточка уходит в переобучение,
// а счётчик провалов растёт.
func (r Rating) Failed() bool { return r == RatingAgain }
