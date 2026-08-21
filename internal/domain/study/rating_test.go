package study_test

import (
	"errors"
	"strings"
	"testing"

	"lexi-bot/internal/domain/study"
)

func TestRatingRoundTrip(t *testing.T) {
	t.Parallel()

	// Оценка уезжает в callback_data строкой и возвращается оттуда же:
	// разбор обязан быть обратным к String() для каждого значения.
	for _, r := range study.Ratings() {
		name := r.String()
		back, err := study.ParseRating(name)
		if err != nil {
			t.Fatalf("ParseRating(%q) вернул ошибку: %v", name, err)
		}
		if back != r {
			t.Errorf("ParseRating(%q) = %v, ожидалось %v", name, back, r)
		}
	}
}

func TestRatingNames(t *testing.T) {
	t.Parallel()

	want := map[study.Rating]string{
		study.RatingAgain: "again",
		study.RatingHard:  "hard",
		study.RatingGood:  "good",
		study.RatingEasy:  "easy",
	}
	for r, name := range want {
		if r.String() != name {
			t.Errorf("String() = %q, ожидалось %q", r.String(), name)
		}
	}

	// Нумерация зафиксирована формулой SM-2 и порядком кнопок.
	if study.RatingAgain != 1 || study.RatingHard != 2 || study.RatingGood != 3 || study.RatingEasy != 4 {
		t.Error("нумерация оценок изменилась: она должна совпадать с SM-2")
	}
}

func TestParseRatingNormalizes(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"good", "GOOD", "  Good  "} {
		got, err := study.ParseRating(in)
		if err != nil {
			t.Fatalf("ParseRating(%q) вернул ошибку: %v", in, err)
		}
		if got != study.RatingGood {
			t.Errorf("ParseRating(%q) = %v, ожидалось good", in, got)
		}
	}
}

func TestParseRatingErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want error
	}{
		{"", study.ErrRequired},
		{"   ", study.ErrRequired},
		{"3", study.ErrInvalid},
		{"perfect", study.ErrInvalid},
		{"rating(9)", study.ErrInvalid},
	}
	for _, tt := range tests {
		got, err := study.ParseRating(tt.in)
		if !errors.Is(err, tt.want) {
			t.Errorf("ParseRating(%q) = %v, ожидалась ошибка %v", tt.in, err, tt.want)
		}
		if got != 0 {
			t.Errorf("при ошибке возвращена оценка %v", got)
		}
	}
}

func TestRatingIsValid(t *testing.T) {
	t.Parallel()

	for _, r := range study.Ratings() {
		if !r.IsValid() {
			t.Errorf("оценка %v должна быть допустимой", r)
		}
	}
	for _, r := range []study.Rating{0, 5, 255} {
		if r.IsValid() {
			t.Errorf("оценка %d не должна быть допустимой", uint8(r))
		}
		// Неизвестное значение печатается диагностически и не разбирается назад.
		if !strings.HasPrefix(r.String(), "rating(") {
			t.Errorf("String() = %q, ожидалась диагностическая запись", r.String())
		}
		if _, err := study.ParseRating(r.String()); err == nil {
			t.Errorf("ParseRating(%q) не должен разбирать диагностическую запись", r.String())
		}
	}
}

func TestRatingFailed(t *testing.T) {
	t.Parallel()

	if !study.RatingAgain.Failed() {
		t.Error("оценка again означает провал")
	}
	for _, r := range []study.Rating{study.RatingHard, study.RatingGood, study.RatingEasy} {
		if r.Failed() {
			t.Errorf("оценка %v не должна считаться провалом", r)
		}
	}
}
