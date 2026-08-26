package study

import (
	"fmt"
	"time"
)

// ReviewID — идентификатор записи в журнале повторений.
type ReviewID int64

// Review — запись журнала: что пользователь ответил, как это оценили и как
// изменилось состояние карточки. Таблица только пополняется, записи не
// правятся и не удаляются.
//
// Журнал нужен не для отчётности: по нему считается статистика (T-045), и он
// же станет обучающей выборкой, если мы перейдём с SM-2 на FSRS. Поэтому здесь
// хранится состояние и до, и после ответа — восстановить его задним числом
// по одной лишь карточке невозможно.
//
// Идентификатора пользователя тут нет намеренно: в базе колонка user_id есть,
// но это денормализация ради быстрых агрегатов, и заполняет её репозиторий из
// курса. Домену она не нужна, а без неё пакет не зависит от domain/user.
type Review struct {
	ID     ReviewID
	CardID CardID
	// RatedAt — момент ответа.
	RatedAt time.Time
	Rating  Rating
	Mode    Mode
	// AnswerRaw — что пользователь ввёл в режиме typing, как есть.
	// В остальных режимах пусто.
	AnswerRaw string
	// IsCorrect — засчитан ли ответ. Считается по режиму: в choice это
	// выбор верного варианта, в typing — принятое совпадение с переводом.
	IsCorrect bool
	// Duration — сколько времени занял ответ. В базе хранится в миллисекундах.
	Duration time.Duration

	// Состояние карточки до и после ответа.
	PrevInterval float64
	NewInterval  float64
	PrevEase     float64
	NewEase      float64
}

// ReviewParams — входные данные конструктора.
type ReviewParams struct {
	CardID    CardID
	RatedAt   time.Time
	Rating    Rating
	Mode      Mode
	AnswerRaw string
	IsCorrect bool
	Duration  time.Duration
	Prev      CardState
	Next      CardState
}

// NewReview собирает запись журнала из ответа и двух состояний карточки.
func NewReview(p ReviewParams) (Review, error) {
	review := Review{
		CardID:       p.CardID,
		RatedAt:      p.RatedAt,
		Rating:       p.Rating,
		Mode:         p.Mode,
		AnswerRaw:    p.AnswerRaw,
		IsCorrect:    p.IsCorrect,
		Duration:     p.Duration,
		PrevInterval: p.Prev.IntervalDays,
		NewInterval:  p.Next.IntervalDays,
		PrevEase:     p.Prev.EaseFactor,
		NewEase:      p.Next.EaseFactor,
	}
	if err := review.Validate(); err != nil {
		return Review{}, err
	}
	return review, nil
}

// Validate проверяет инварианты записи журнала.
func (r Review) Validate() error {
	if r.CardID <= 0 {
		return fmt.Errorf("card_id: %w", ErrRequired)
	}
	if r.RatedAt.IsZero() {
		return fmt.Errorf("rated_at: %w", ErrRequired)
	}
	if !r.Rating.IsValid() {
		return fmt.Errorf("rating: %w (%s)", ErrInvalid, r.Rating)
	}
	if !r.Mode.IsValid() {
		return fmt.Errorf("mode %q: %w", r.Mode, ErrInvalid)
	}
	if r.Duration < 0 {
		return fmt.Errorf("duration_ms = %v: %w (ожидалось неотрицательное значение)", r.Duration, ErrOutOfRange)
	}
	if r.PrevInterval < 0 || r.NewInterval < 0 {
		return fmt.Errorf("interval: %w (ожидалось неотрицательное число)", ErrOutOfRange)
	}
	if r.AnswerRaw != "" && r.Mode != ModeTyping {
		return fmt.Errorf("answer_raw: %w (текст ответа бывает только в режиме %s)", ErrInvalid, ModeTyping)
	}
	return nil
}
