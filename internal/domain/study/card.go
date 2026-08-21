package study

import (
	"fmt"
	"time"

	"lexi-bot/internal/domain/lexicon"
)

// Границы коэффициента лёгкости (ease factor) из SM-2.
//
// Пол 1.3 — не украшение: без него у карточки, которую пользователь стабильно
// заваливает, интервал схлопывается почти в ноль, и она начинает всплывать
// в каждой сессии, вытесняя всё остальное.
const (
	MinEaseFactor     = 1.3
	DefaultEaseFactor = 2.5
	MaxEaseFactor     = 5.0
)

// CardID — идентификатор карточки в хранилище.
type CardID int64

// CourseID — идентификатор курса (user_courses): «пользователь учит колоду X
// с переводом на язык Y». Карточка всегда принадлежит курсу, а не колоде:
// один и тот же корейский список у двух пользователей — это разные карточки.
type CourseID int64

// CardState — состояние интервального повторения одной карточки: всё, что
// нужно планировщику, чтобы назначить следующий показ, и ничего больше.
//
// Тип отделён от Card намеренно: Scheduler принимает и возвращает именно его,
// поэтому алгоритм не знает ни про идентификаторы, ни про лексемы, и тестируется
// таблицей значений.
type CardState struct {
	State State
	// DueAt — момент, когда карточку следует показать снова.
	DueAt time.Time
	// IntervalDays — текущий интервал в сутках. У карточек на шагах обучения
	// он дробный: 1 минута — это примерно 0.0007 суток.
	IntervalDays float64
	// EaseFactor — коэффициент лёгкости SM-2, не меньше MinEaseFactor.
	EaseFactor float64
	// Repetitions — сколько раз подряд карточку вспомнили. Провал обнуляет.
	Repetitions int
	// Lapses — сколько раз выученную карточку забыли за всю её жизнь.
	Lapses int
	// LearnStep — индекс шага обучения, пока карточка в фазе Learning
	// или Relearning; в остальных фазах не имеет смысла и равен нулю.
	LearnStep int
}

// NewCardState возвращает состояние только что введённой карточки: она новая,
// показать её нужно сразу, коэффициент лёгкости — стандартный для SM-2.
func NewCardState(now time.Time) CardState {
	return CardState{
		State:      StateNew,
		DueAt:      now,
		EaseFactor: DefaultEaseFactor,
	}
}

// Validate проверяет инварианты состояния.
func (s CardState) Validate() error {
	if !s.State.IsValid() {
		return fmt.Errorf("state: %w (%s)", ErrInvalid, s.State)
	}
	if s.DueAt.IsZero() {
		return fmt.Errorf("due_at: %w", ErrRequired)
	}
	if s.IntervalDays < 0 {
		return fmt.Errorf("interval_days = %v: %w (ожидалось неотрицательное число)", s.IntervalDays, ErrOutOfRange)
	}
	if s.EaseFactor < MinEaseFactor || s.EaseFactor > MaxEaseFactor {
		return fmt.Errorf("ease_factor = %v: %w (ожидалось %v..%v)", s.EaseFactor, ErrOutOfRange, MinEaseFactor, MaxEaseFactor)
	}
	if s.Repetitions < 0 {
		return fmt.Errorf("repetitions = %d: %w (ожидалось неотрицательное число)", s.Repetitions, ErrOutOfRange)
	}
	if s.Lapses < 0 {
		return fmt.Errorf("lapses = %d: %w (ожидалось неотрицательное число)", s.Lapses, ErrOutOfRange)
	}
	if s.LearnStep < 0 {
		return fmt.Errorf("learn_step = %d: %w (ожидалось неотрицательное число)", s.LearnStep, ErrOutOfRange)
	}
	return nil
}

// IsDue сообщает, что карточку пора показать: срок наступил и она не отложена.
func (s CardState) IsDue(now time.Time) bool {
	if s.State == StateSuspended {
		return false
	}
	return !s.DueAt.After(now)
}

// Interval возвращает текущий интервал длительностью — в таком виде его удобно
// складывать с моментом времени.
func (s CardState) Interval() time.Duration {
	return time.Duration(s.IntervalDays * float64(24*time.Hour))
}

// Card — слово курса вместе с его состоянием повторения.
type Card struct {
	ID       CardID
	CourseID CourseID
	LexemeID lexicon.LexemeID
	CardState
	// IntroducedAt — когда слово было введено в курс и заняло место
	// в дневном лимите новых слов.
	IntroducedAt time.Time
	// LastReviewedAt — момент последнего ответа; нулевое значение означает,
	// что карточку ещё ни разу не показывали.
	LastReviewedAt time.Time
}

// NewCard создаёт карточку для слова, которое вводится в курс сейчас.
func NewCard(courseID CourseID, lexemeID lexicon.LexemeID, now time.Time) (Card, error) {
	card := Card{
		CourseID:     courseID,
		LexemeID:     lexemeID,
		CardState:    NewCardState(now),
		IntroducedAt: now,
	}
	if err := card.Validate(); err != nil {
		return Card{}, err
	}
	return card, nil
}

// Validate проверяет инварианты карточки вместе с её состоянием.
func (c Card) Validate() error {
	if c.CourseID <= 0 {
		return fmt.Errorf("user_course_id: %w", ErrRequired)
	}
	if c.LexemeID <= 0 {
		return fmt.Errorf("lexeme_id: %w", ErrRequired)
	}
	if c.IntroducedAt.IsZero() {
		return fmt.Errorf("introduced_at: %w", ErrRequired)
	}
	return c.CardState.Validate()
}

// IsNew сообщает, что карточку ещё ни разу не показывали.
func (c Card) IsNew() bool { return c.LastReviewedAt.IsZero() }
