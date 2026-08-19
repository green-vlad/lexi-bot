package user

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// TimeOfDay — время суток без даты и без зоны: «21:30 по местному времени
// пользователя». Именно так задаётся напоминание, и именно поэтому оно не
// может быть time.Time — момент напоминания разный каждый день, а настройка одна.
//
// Нулевое значение означает «не задано»: 00:00 — допустимое время, поэтому
// отличить «полночь» от «выключено» нулём минут нельзя, для этого есть флаг.
type TimeOfDay struct {
	minutes int
	set     bool
}

// NewTimeOfDay собирает время суток из часов и минут.
func NewTimeOfDay(hour, minute int) (TimeOfDay, error) {
	if hour < 0 || hour > 23 {
		return TimeOfDay{}, fmt.Errorf("reminder_at: %w (час %d вне 0..23)", ErrOutOfRange, hour)
	}
	if minute < 0 || minute > 59 {
		return TimeOfDay{}, fmt.Errorf("reminder_at: %w (минута %d вне 0..59)", ErrOutOfRange, minute)
	}
	return TimeOfDay{minutes: hour*60 + minute, set: true}, nil
}

// ParseTimeOfDay разбирает запись вида 21:30 или 9:05.
func ParseTimeOfDay(s string) (TimeOfDay, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return TimeOfDay{}, fmt.Errorf("reminder_at: %w", ErrRequired)
	}

	rawHour, rawMinute, found := strings.Cut(s, ":")
	if !found {
		return TimeOfDay{}, fmt.Errorf("reminder_at %q: %w (ожидался формат ЧЧ:ММ)", s, ErrInvalid)
	}
	hour, errHour := strconv.Atoi(rawHour)
	minute, errMinute := strconv.Atoi(rawMinute)
	if errHour != nil || errMinute != nil || len(rawMinute) != 2 {
		return TimeOfDay{}, fmt.Errorf("reminder_at %q: %w (ожидался формат ЧЧ:ММ)", s, ErrInvalid)
	}
	return NewTimeOfDay(hour, minute)
}

// MustParseTimeOfDay — ParseTimeOfDay для констант и таблиц в тестах.
func MustParseTimeOfDay(s string) TimeOfDay {
	t, err := ParseTimeOfDay(s)
	if err != nil {
		panic(err)
	}
	return t
}

// IsSet сообщает, что время суток задано.
func (t TimeOfDay) IsSet() bool { return t.set }

// Hour возвращает часы, Minute — минуты.
func (t TimeOfDay) Hour() int { return t.minutes / 60 }

// Minute возвращает минуты.
func (t TimeOfDay) Minute() int { return t.minutes % 60 }

// String возвращает запись вида 21:30; у незаданного значения — пустую строку.
func (t TimeOfDay) String() string {
	if !t.set {
		return ""
	}
	return fmt.Sprintf("%02d:%02d", t.Hour(), t.Minute())
}

// On возвращает момент, когда это время суток наступает в сутках, содержащих
// day, в таймзоне tz. Планировщик напоминаний (T-046) сравнивает с ним текущий
// момент.
//
// Время строится по календарю, а не прибавлением длительности к полуночи:
// в сутки перевода часов между полуночью и 21:30 проходит не 21 час 30 минут,
// и напоминание уехало бы на час.
func (t TimeOfDay) On(day time.Time, tz Timezone) (time.Time, bool) {
	if !t.set {
		return time.Time{}, false
	}
	loc := tz.Location()
	year, month, dayOfMonth := day.In(loc).Date()
	return time.Date(year, month, dayOfMonth, t.Hour(), t.Minute(), 0, 0, loc), true
}
