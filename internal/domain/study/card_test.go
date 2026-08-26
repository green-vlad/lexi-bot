package study_test

import (
	"errors"
	"testing"
	"time"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
)

var now = time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

func TestNewCardState(t *testing.T) {
	t.Parallel()

	s := study.NewCardState(now)
	if s.State != study.StateNew {
		t.Errorf("State = %v, ожидалось new", s.State)
	}
	if !s.DueAt.Equal(now) {
		t.Errorf("DueAt = %v, ожидалось %v: новую карточку показываем сразу", s.DueAt, now)
	}
	if s.EaseFactor != study.DefaultEaseFactor {
		t.Errorf("EaseFactor = %v, ожидалось %v", s.EaseFactor, study.DefaultEaseFactor)
	}
	if s.IntervalDays != 0 || s.Repetitions != 0 || s.Lapses != 0 || s.LearnStep != 0 {
		t.Errorf("у новой карточки должны быть нулевые счётчики, получено %+v", s)
	}
	if err := s.Validate(); err != nil {
		t.Errorf("Validate() вернул ошибку на новом состоянии: %v", err)
	}
}

func TestCardStateValidate(t *testing.T) {
	t.Parallel()

	valid := study.NewCardState(now)

	tests := []struct {
		name   string
		mutate func(s *study.CardState)
		want   error
	}{
		{"неизвестная фаза", func(s *study.CardState) { s.State = study.State(9) }, study.ErrInvalid},
		{"нет срока показа", func(s *study.CardState) { s.DueAt = time.Time{} }, study.ErrRequired},
		{"отрицательный интервал", func(s *study.CardState) { s.IntervalDays = -1 }, study.ErrOutOfRange},
		{"ease ниже пола", func(s *study.CardState) { s.EaseFactor = study.MinEaseFactor - 0.01 }, study.ErrOutOfRange},
		{"ease выше потолка", func(s *study.CardState) { s.EaseFactor = study.MaxEaseFactor + 0.01 }, study.ErrOutOfRange},
		{"ease не задан", func(s *study.CardState) { s.EaseFactor = 0 }, study.ErrOutOfRange},
		{"отрицательные повторения", func(s *study.CardState) { s.Repetitions = -1 }, study.ErrOutOfRange},
		{"отрицательные провалы", func(s *study.CardState) { s.Lapses = -1 }, study.ErrOutOfRange},
		{"отрицательный шаг обучения", func(s *study.CardState) { s.LearnStep = -1 }, study.ErrOutOfRange},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := valid
			tt.mutate(&s)
			if err := s.Validate(); !errors.Is(err, tt.want) {
				t.Errorf("Validate() = %v, ожидалась ошибка %v", err, tt.want)
			}
		})
	}

	// Границы включительно.
	for _, ef := range []float64{study.MinEaseFactor, study.MaxEaseFactor} {
		s := valid
		s.EaseFactor = ef
		if err := s.Validate(); err != nil {
			t.Errorf("ease_factor = %v: Validate() вернул ошибку %v", ef, err)
		}
	}
}

func TestCardStateIsDue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state study.State
		dueAt time.Time
		want  bool
	}{
		{"срок прошёл", study.StateReview, now.Add(-time.Hour), true},
		{"срок ровно сейчас", study.StateReview, now, true},
		{"срок ещё не наступил", study.StateReview, now.Add(time.Minute), false},
		{"на шагах обучения", study.StateLearning, now, true},
		{"забытая карточка", study.StateRelearning, now, true},
		// Новая карточка ждёт знакомства, а не повторения: её due_at
		// означает готовность слова к показу, а не подошедший срок.
		{"новая карточка со сроком сейчас", study.StateNew, now, false},
		{"отложенная карточка не выдаётся", study.StateSuspended, now.Add(-24 * time.Hour), false},
		{"слово, которое человек уже знает", study.StateKnown, now.Add(-24 * time.Hour), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := study.CardState{State: tt.state, DueAt: tt.dueAt, EaseFactor: study.DefaultEaseFactor}
			if got := s.IsDue(now); got != tt.want {
				t.Errorf("IsDue() = %t, ожидалось %t", got, tt.want)
			}
		})
	}
}

func TestCardStateInterval(t *testing.T) {
	t.Parallel()

	tests := []struct {
		days float64
		want time.Duration
	}{
		{0, 0},
		{1, 24 * time.Hour},
		{0.5, 12 * time.Hour},
		{10, 240 * time.Hour},
	}
	for _, tt := range tests {
		s := study.CardState{IntervalDays: tt.days}
		if got := s.Interval(); got != tt.want {
			t.Errorf("Interval() при %v сутках = %v, ожидалось %v", tt.days, got, tt.want)
		}
	}
}

func TestNewCard(t *testing.T) {
	t.Parallel()

	card, err := study.NewCard(7, 42, now)
	if err != nil {
		t.Fatalf("NewCard() вернул ошибку: %v", err)
	}
	if card.CourseID != 7 || card.LexemeID != 42 {
		t.Errorf("NewCard() = %+v, идентификаторы не сохранены", card)
	}
	if !card.IntroducedAt.Equal(now) {
		t.Errorf("IntroducedAt = %v, ожидалось %v", card.IntroducedAt, now)
	}
	if !card.IsNew() {
		t.Error("только что созданная карточка ещё ни разу не показывалась")
	}
	if card.IsDue(now) {
		t.Error("новая карточка ждёт знакомства, а не повторения")
	}
	if card.State != study.StateNew {
		t.Errorf("State = %v, ожидалось new", card.State)
	}
}

func TestNewCardErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		courseID study.CourseID
		lexemeID lexicon.LexemeID
		want     error
	}{
		{"без курса", 0, 42, study.ErrRequired},
		{"без лексемы", 7, 0, study.ErrRequired},
		{"отрицательный курс", -1, 42, study.ErrRequired},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			card, err := study.NewCard(tt.courseID, tt.lexemeID, now)
			if !errors.Is(err, tt.want) {
				t.Fatalf("NewCard() = %v, ожидалась ошибка %v", err, tt.want)
			}
			if card != (study.Card{}) {
				t.Errorf("при ошибке возвращена непустая карточка %+v", card)
			}
		})
	}

	if _, err := study.NewCard(7, 42, time.Time{}); !errors.Is(err, study.ErrRequired) {
		t.Error("без момента введения карточка не должна создаваться")
	}
}

func TestCardIsNewAfterReview(t *testing.T) {
	t.Parallel()

	card, err := study.NewCard(7, 42, now)
	if err != nil {
		t.Fatalf("NewCard() вернул ошибку: %v", err)
	}

	card.LastReviewedAt = now.Add(time.Minute)
	if card.IsNew() {
		t.Error("после первого ответа карточка перестаёт быть новой")
	}
	if err := card.Validate(); err != nil {
		t.Errorf("Validate() вернул ошибку: %v", err)
	}
}

func TestCardValidateChecksState(t *testing.T) {
	t.Parallel()

	// Карточка проверяет и собственные поля, и вложенное состояние.
	card, err := study.NewCard(7, 42, now)
	if err != nil {
		t.Fatalf("NewCard() вернул ошибку: %v", err)
	}
	card.EaseFactor = 0
	if !errors.Is(card.Validate(), study.ErrOutOfRange) {
		t.Error("Validate() пропустил недопустимый ease_factor вложенного состояния")
	}
}
