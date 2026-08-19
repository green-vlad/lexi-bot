package user

import (
	"fmt"
	"strings"
	"time"
)

// Timezone — таймзона пользователя: обёртка над *time.Location, которая знает
// своё имя IANA и умеет отвечать на главный вопрос дневных лимитов — «когда
// у этого пользователя начались сутки».
//
// Нулевое значение означает «не задана» и ведёт себя как UTC: методы на нём
// работают, а не паникуют, потому что таймзона может не дойти из базы, и
// падать из-за этого посреди учебной сессии нельзя.
type Timezone struct {
	loc *time.Location
}

// UTCTimezone — таймзона по умолчанию до того, как пользователь выбрал свою.
func UTCTimezone() Timezone { return Timezone{loc: time.UTC} }

// ParseTimezone разбирает имя зоны IANA (Europe/Moscow, Asia/Seoul).
//
// Список зон берётся из системы, а в контейнере на distroless его нет, поэтому
// точка входа импортирует time/tzdata — иначе здесь не нашлась бы ни одна зона,
// кроме UTC и Local.
func ParseTimezone(name string) (Timezone, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Timezone{}, fmt.Errorf("timezone: %w", ErrRequired)
	}
	// Local зависит от машины, на которой запущен процесс, и в базе бессмысленна.
	if name == "Local" {
		return Timezone{}, fmt.Errorf("timezone %q: %w (нужна зона IANA вида Europe/Moscow)", name, ErrInvalid)
	}

	loc, err := time.LoadLocation(name)
	if err != nil {
		return Timezone{}, fmt.Errorf("timezone %q: %w (неизвестная зона IANA)", name, ErrInvalid)
	}
	return Timezone{loc: loc}, nil
}

// MustParseTimezone — ParseTimezone для констант и таблиц в тестах.
func MustParseTimezone(name string) Timezone {
	tz, err := ParseTimezone(name)
	if err != nil {
		panic(err)
	}
	return tz
}

// NewTimezone оборачивает уже загруженную зону: так конфигурация передаёт
// значение DEFAULT_TIMEZONE, не разбирая имя второй раз.
func NewTimezone(loc *time.Location) Timezone { return Timezone{loc: loc} }

// IsZero сообщает, что таймзона не задана.
func (tz Timezone) IsZero() bool { return tz.loc == nil }

// Location возвращает зону для арифметики со временем; у нулевого значения — UTC.
func (tz Timezone) Location() *time.Location {
	if tz.loc == nil {
		return time.UTC
	}
	return tz.loc
}

// String возвращает имя зоны IANA; у нулевого значения — UTC.
func (tz Timezone) String() string { return tz.Location().String() }

// DayStart возвращает начало календарных суток пользователя, в которые попадает
// момент t. Результат живёт в таймзоне пользователя, а сравнивать его с чем
// угодно можно как обычно: моменты времени сравниваются по абсолютной шкале.
//
// В редких зонах полуночи в сутках может не быть: на Кубе часы переводят вперёд
// ровно в 00:00, и 8 марта сразу за 23:59:59 идёт 01:00. time.Date в таком
// случае нормализует время назад, в предыдущие сутки, — то есть без поправки
// ниже начало суток уехало бы на день назад вместе с дневным счётчиком.
func (tz Timezone) DayStart(t time.Time) time.Time {
	loc := tz.Location()
	local := t.In(loc)
	year, month, day := local.Date()

	start := time.Date(year, month, day, 0, 0, 0, 0, loc)
	if y, m, d := start.Date(); y != year || m != month || d != day {
		// Полуночи не существует — берём час ночи: перевод часов больше чем
		// на час в мировой практике не встречается, так что этот момент есть.
		start = time.Date(year, month, day, 1, 0, 0, 0, loc)
	}
	return start
}

// SameDay сообщает, что два момента приходятся на одни календарные сутки
// пользователя. Так дневной счётчик понимает, что лимит пора обнулить.
func (tz Timezone) SameDay(a, b time.Time) bool {
	return tz.DayStart(a).Equal(tz.DayStart(b))
}

// DayKey возвращает дату суток пользователя в формате YYYY-MM-DD — ключ строки
// в daily_counters. Ключом служит именно локальная дата, поэтому лимит
// обнуляется в полночь пользователя, а не в полночь UTC.
func (tz Timezone) DayKey(t time.Time) string {
	return tz.DayStart(t).Format(time.DateOnly)
}
