package study_test

import (
	"errors"
	"testing"
	"time"

	"lexi-bot/internal/domain/study"
)

func reviewParams() study.ReviewParams {
	prev := study.CardState{
		State:        study.StateReview,
		DueAt:        now,
		IntervalDays: 10,
		EaseFactor:   2.5,
		Repetitions:  4,
	}
	next := study.CardState{
		State:        study.StateReview,
		DueAt:        now.AddDate(0, 0, 25),
		IntervalDays: 25,
		EaseFactor:   2.6,
		Repetitions:  5,
	}
	return study.ReviewParams{
		CardID:    11,
		RatedAt:   now,
		Rating:    study.RatingGood,
		Mode:      study.ModeChoice,
		IsCorrect: true,
		Duration:  1500 * time.Millisecond,
		Prev:      prev,
		Next:      next,
	}
}

func TestNewReviewKeepsBothStates(t *testing.T) {
	t.Parallel()

	// Журнал хранит состояние до и после ответа: по одной лишь карточке
	// восстановить его задним числом невозможно, а статистике и будущему
	// FSRS нужны обе точки.
	p := reviewParams()
	review, err := study.NewReview(p)
	if err != nil {
		t.Fatalf("NewReview() вернул ошибку: %v", err)
	}

	if review.PrevInterval != p.Prev.IntervalDays || review.NewInterval != p.Next.IntervalDays {
		t.Errorf("интервалы = %v → %v, ожидалось %v → %v",
			review.PrevInterval, review.NewInterval, p.Prev.IntervalDays, p.Next.IntervalDays)
	}
	if review.PrevEase != p.Prev.EaseFactor || review.NewEase != p.Next.EaseFactor {
		t.Errorf("ease = %v → %v, ожидалось %v → %v",
			review.PrevEase, review.NewEase, p.Prev.EaseFactor, p.Next.EaseFactor)
	}
	if review.CardID != p.CardID || review.Rating != p.Rating || review.Mode != p.Mode {
		t.Errorf("NewReview() = %+v, поля не сохранены", review)
	}
	if review.Duration != p.Duration {
		t.Errorf("Duration = %v, ожидалось %v", review.Duration, p.Duration)
	}
}

func TestNewReviewErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(p *study.ReviewParams)
		want   error
	}{
		{"без карточки", func(p *study.ReviewParams) { p.CardID = 0 }, study.ErrRequired},
		{"без момента ответа", func(p *study.ReviewParams) { p.RatedAt = time.Time{} }, study.ErrRequired},
		{"без оценки", func(p *study.ReviewParams) { p.Rating = 0 }, study.ErrInvalid},
		{"неизвестная оценка", func(p *study.ReviewParams) { p.Rating = study.Rating(9) }, study.ErrInvalid},
		{"без режима", func(p *study.ReviewParams) { p.Mode = "" }, study.ErrInvalid},
		{"неизвестный режим", func(p *study.ReviewParams) { p.Mode = study.Mode("dictation") }, study.ErrInvalid},
		{"отрицательная длительность", func(p *study.ReviewParams) { p.Duration = -time.Second }, study.ErrOutOfRange},
		{"отрицательный интервал", func(p *study.ReviewParams) { p.Next.IntervalDays = -1 }, study.ErrOutOfRange},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := reviewParams()
			tt.mutate(&p)

			review, err := study.NewReview(p)
			if !errors.Is(err, tt.want) {
				t.Fatalf("NewReview() = %v, ожидалась ошибка %v", err, tt.want)
			}
			if review != (study.Review{}) {
				t.Errorf("при ошибке возвращена непустая запись %+v", review)
			}
		})
	}
}

func TestReviewAnswerOnlyInTyping(t *testing.T) {
	t.Parallel()

	// Текст ответа бывает только там, где пользователь его печатал: иначе
	// в журнале появится поле, которого в этом режиме взяться неоткуда.
	p := reviewParams()
	p.Mode = study.ModeTyping
	p.AnswerRaw = "дом"
	if _, err := study.NewReview(p); err != nil {
		t.Fatalf("NewReview() в режиме typing вернул ошибку: %v", err)
	}

	p = reviewParams()
	p.Mode = study.ModeChoice
	p.AnswerRaw = "дом"
	if _, err := study.NewReview(p); !errors.Is(err, study.ErrInvalid) {
		t.Error("текст ответа в режиме choice должен быть отклонён")
	}
}

func TestReviewOfFailedAnswer(t *testing.T) {
	t.Parallel()

	p := reviewParams()
	p.Rating = study.RatingAgain
	p.IsCorrect = false
	p.Next = study.CardState{
		State:        study.StateRelearning,
		DueAt:        now.Add(10 * time.Minute),
		IntervalDays: 5,
		EaseFactor:   2.3,
		Lapses:       1,
	}

	review, err := study.NewReview(p)
	if err != nil {
		t.Fatalf("NewReview() вернул ошибку: %v", err)
	}
	if !review.Rating.Failed() {
		t.Error("оценка again должна считаться провалом")
	}
	if review.NewEase >= review.PrevEase {
		t.Errorf("после провала ease должен падать: %v → %v", review.PrevEase, review.NewEase)
	}
}
