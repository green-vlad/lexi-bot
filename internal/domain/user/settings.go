package user

import (
	"fmt"
	"time"
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
// Хранится она в users.timezone (см. PLAN.md §4) — сопоставление колонок
// и полей домена делает репозиторий.
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
	ReverseDirection bool
}

// DefaultSettings возвращает настройки нового пользователя в заданной таймзоне.
// Напоминание выключено: время для него спрашивают отдельным шагом онбординга.
func DefaultSettings(tz Timezone) Settings {
	return Settings{
		NewPerDay:        DefaultNewPerDay,
		MaxReviewsPerDay: DefaultReviewsPerDay,
		Timezone:         tz,
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
	return nil
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
