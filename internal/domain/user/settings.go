package user

import (
	"fmt"
	"time"

	"lexi-bot/internal/domain/study"
)

// Границы настроек. Верхние пределы защищают пользователя от самого себя:
// сто новых слов в день — это уже несколько часов повторений на следующей
// неделе, и человек бросает бота раньше, чем успевает что-то выучить.
const (
	MinNewPerDay = 1
	MaxNewPerDay = 100

	MinReviewsPerDay = 10
	MaxReviewsPerDay = 500
)

// Значения по умолчанию для нового пользователя: десять слов в день — примерно
// пятнадцать минут занятий, лимит повторений с запасом перекрывает нагрузку,
// которую эти десять слов создадут через месяц.
const (
	DefaultNewPerDay     = 10
	DefaultReviewsPerDay = 200
)

// Settings — настройки обучения пользователя.
//
// Таймзона лежит здесь, а не в User, потому что она нужна ровно для одного:
// понять, когда у пользователя начались сутки, и обнулить дневные лимиты.
// Схема базы повторяет это решение — колонка user_settings.timezone.
type Settings struct {
	// NewPerDay — сколько новых слов вводить в день.
	NewPerDay int
	// MaxReviewsPerDay — потолок повторений в день, чтобы после перерыва
	// пользователь не получил очередь на тысячу карточек.
	MaxReviewsPerDay int
	// ReminderAt — местное время напоминания. Незаданное значение выключает его.
	ReminderAt TimeOfDay
	// Timezone — таймзона пользователя, задающая границу суток.
	Timezone Timezone
	// ReverseDirection меняет направление проверки на «перевод → слово».
	// Что это значит для режимов — в study.Direction.
	ReverseDirection bool
	// QuizModes — включённые режимы проверки. Пустым быть не может: иначе
	// карточку нечем показать. Хранится в каноничном порядке без повторов.
	QuizModes []study.Mode
}

// DefaultSettings возвращает настройки нового пользователя в заданной таймзоне.
// Напоминание выключено: время для него спрашивают отдельным шагом онбординга.
func DefaultSettings(tz Timezone) Settings {
	return Settings{
		NewPerDay:        DefaultNewPerDay,
		MaxReviewsPerDay: DefaultReviewsPerDay,
		Timezone:         tz,
		QuizModes:        study.Modes(),
	}
}

// Validate проверяет, что настройки в допустимых границах.
func (s Settings) Validate() error {
	if s.NewPerDay < MinNewPerDay || s.NewPerDay > MaxNewPerDay {
		return fmt.Errorf("new_per_day = %d: %w (ожидалось %d..%d)",
			s.NewPerDay, ErrOutOfRange, MinNewPerDay, MaxNewPerDay)
	}
	if s.MaxReviewsPerDay < MinReviewsPerDay || s.MaxReviewsPerDay > MaxReviewsPerDay {
		return fmt.Errorf("max_reviews_per_day = %d: %w (ожидалось %d..%d)",
			s.MaxReviewsPerDay, ErrOutOfRange, MinReviewsPerDay, MaxReviewsPerDay)
	}
	if s.Timezone.IsZero() {
		return fmt.Errorf("timezone: %w", ErrRequired)
	}
	if len(s.QuizModes) == 0 {
		return fmt.Errorf("quiz_modes: %w (нужен хотя бы один режим проверки)", ErrRequired)
	}
	for _, mode := range s.QuizModes {
		if !mode.IsValid() {
			return fmt.Errorf("quiz_modes: %w (неизвестный режим %q)", ErrInvalid, mode)
		}
	}
	return nil
}

// WithQuizModes возвращает копию настроек с другим набором режимов проверки.
// Набор приводится к каноничному порядку без повторов, поэтому «typing, typing,
// choice» и «choice, typing» дают одни и те же настройки.
func (s Settings) WithQuizModes(modes []study.Mode) (Settings, error) {
	canonical := make([]study.Mode, 0, len(modes))
	for _, known := range study.Modes() {
		for _, mode := range modes {
			if mode == known {
				canonical = append(canonical, known)
				break
			}
		}
	}
	// Неизвестные режимы канонизация молча выбросила бы, поэтому проверяем
	// исходный набор: опечатка в коде или в базе должна быть видна.
	for _, mode := range modes {
		if !mode.IsValid() {
			return Settings{}, fmt.Errorf("quiz_modes: %w (неизвестный режим %q)", ErrInvalid, mode)
		}
	}

	s.QuizModes = canonical
	if err := s.Validate(); err != nil {
		return Settings{}, err
	}
	return s, nil
}

// ModeEnabled сообщает, что режим проверки включён пользователем.
func (s Settings) ModeEnabled(mode study.Mode) bool {
	for _, enabled := range s.QuizModes {
		if enabled == mode {
			return true
		}
	}
	return false
}

// WithNewPerDay возвращает копию настроек с новым дневным лимитом новых слов.
// Настройки — значение, а не объект: сценарий получает изменённую копию и
// сохраняет её целиком, поэтому неудачная валидация ничего не портит.
func (s Settings) WithNewPerDay(n int) (Settings, error) {
	s.NewPerDay = n
	if err := s.Validate(); err != nil {
		return Settings{}, err
	}
	return s, nil
}

// WithMaxReviewsPerDay возвращает копию настроек с новым лимитом повторений.
func (s Settings) WithMaxReviewsPerDay(n int) (Settings, error) {
	s.MaxReviewsPerDay = n
	if err := s.Validate(); err != nil {
		return Settings{}, err
	}
	return s, nil
}

// WithTimezone возвращает копию настроек с другой таймзоной.
func (s Settings) WithTimezone(tz Timezone) (Settings, error) {
	s.Timezone = tz
	if err := s.Validate(); err != nil {
		return Settings{}, err
	}
	return s, nil
}

// WithReminderAt возвращает копию настроек с новым временем напоминания.
// Незаданное время выключает напоминания.
func (s Settings) WithReminderAt(at TimeOfDay) (Settings, error) {
	s.ReminderAt = at
	if err := s.Validate(); err != nil {
		return Settings{}, err
	}
	return s, nil
}

// Direction — в какую сторону спрашивать карточки.
func (s Settings) Direction() study.Direction {
	if s.ReverseDirection {
		return study.DirectionProduce
	}
	return study.DirectionRecognize
}

// RemindersEnabled сообщает, что пользователь просил напоминать.
func (s Settings) RemindersEnabled() bool { return s.ReminderAt.IsSet() }

// DayStart возвращает начало календарных суток пользователя, в которые попадает
// момент t: точку отсчёта дневных лимитов и ключ строки дневного счётчика.
func (s Settings) DayStart(t time.Time) time.Time { return s.Timezone.DayStart(t) }

// SameDay сообщает, что два момента приходятся на одни сутки пользователя.
func (s Settings) SameDay(a, b time.Time) bool { return s.Timezone.SameDay(a, b) }

// NextReminder возвращает ближайший момент напоминания строго после after.
// Второе значение — false, если напоминания выключены.
func (s Settings) NextReminder(after time.Time) (time.Time, bool) {
	today, ok := s.ReminderAt.On(after, s.Timezone)
	if !ok {
		return time.Time{}, false
	}
	if today.After(after) {
		return today, true
	}
	// Время сегодня уже прошло — берём тот же час следующих суток. Складывать
	// 24 часа нельзя: в день перехода на летнее время сутки короче или длиннее,
	// а напоминание всё равно должно прийти в обещанные 21:30.
	tomorrow := after.In(s.Timezone.Location()).AddDate(0, 0, 1)
	next, _ := s.ReminderAt.On(tomorrow, s.Timezone)
	return next, true
}
