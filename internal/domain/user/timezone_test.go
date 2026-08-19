package user_test

import (
	"errors"
	"testing"
	"time"

	"lexi-bot/internal/domain/user"
)

func TestParseTimezone(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"UTC", "Europe/Moscow", "Asia/Seoul", "  America/New_York  "} {
		tz, err := user.ParseTimezone(name)
		if err != nil {
			t.Fatalf("ParseTimezone(%q) вернул ошибку: %v", name, err)
		}
		if tz.IsZero() {
			t.Errorf("ParseTimezone(%q) вернул нулевое значение", name)
		}
	}
}

func TestParseTimezoneErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want error
	}{
		{"пустая строка", "", user.ErrRequired},
		{"только пробелы", "   ", user.ErrRequired},
		{"неизвестная зона", "Europe/Atlantis", user.ErrInvalid},
		{"аббревиатура вместо зоны IANA", "MSK", user.ErrInvalid},
		{"Local зависит от машины", "Local", user.ErrInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tz, err := user.ParseTimezone(tt.in)
			if !errors.Is(err, tt.want) {
				t.Fatalf("ParseTimezone(%q) = %v, ожидалась ошибка %v", tt.in, err, tt.want)
			}
			if !tz.IsZero() {
				t.Errorf("при ошибке возвращена непустая зона %q", tz)
			}
		})
	}
}

func TestTimezoneZeroValueActsAsUTC(t *testing.T) {
	t.Parallel()

	// Нулевая таймзона не должна ронять сессию: считаем её UTC.
	var zero user.Timezone
	if !zero.IsZero() {
		t.Error("нулевое значение должно считаться незаданным")
	}
	if zero.Location() != time.UTC {
		t.Errorf("Location() = %v, ожидался UTC", zero.Location())
	}

	moment := time.Date(2026, 3, 14, 15, 9, 0, 0, time.UTC)
	want := time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC)
	if got := zero.DayStart(moment); !got.Equal(want) {
		t.Errorf("DayStart() = %v, ожидалось %v", got, want)
	}
}

func TestDayStartAcrossMidnight(t *testing.T) {
	t.Parallel()

	seoul := user.MustParseTimezone("Asia/Seoul") // UTC+9 круглый год
	loc := seoul.Location()

	tests := []struct {
		name string
		when time.Time
		want time.Time
	}{
		{
			name: "за минуту до полуночи — сутки ещё вчерашние",
			when: time.Date(2026, 8, 19, 23, 59, 0, 0, loc),
			want: time.Date(2026, 8, 19, 0, 0, 0, 0, loc),
		},
		{
			name: "ровно полночь — начало новых суток",
			when: time.Date(2026, 8, 20, 0, 0, 0, 0, loc),
			want: time.Date(2026, 8, 20, 0, 0, 0, 0, loc),
		},
		{
			name: "минута после полуночи — те же новые сутки",
			when: time.Date(2026, 8, 20, 0, 1, 0, 0, loc),
			want: time.Date(2026, 8, 20, 0, 0, 0, 0, loc),
		},
		{
			name: "момент задан в UTC — приводится к суткам пользователя",
			when: time.Date(2026, 8, 19, 16, 30, 0, 0, time.UTC), // 20 августа 01:30 в Сеуле
			want: time.Date(2026, 8, 20, 0, 0, 0, 0, loc),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := seoul.DayStart(tt.when); !got.Equal(tt.want) {
				t.Errorf("DayStart(%v) = %v, ожидалось %v", tt.when, got, tt.want)
			}
		})
	}
}

func TestDayStartDependsOnTimezone(t *testing.T) {
	t.Parallel()

	// Один и тот же момент попадает в разные календарные сутки у пользователей
	// в разных зонах — ради этого дневные лимиты и считаются не по UTC.
	moment := time.Date(2026, 8, 19, 20, 0, 0, 0, time.UTC)

	seoul := user.MustParseTimezone("Asia/Seoul")         // уже 20 августа, 05:00
	newYork := user.MustParseTimezone("America/New_York") // ещё 19 августа, 16:00

	if got, want := seoul.DayKey(moment), "2026-08-20"; got != want {
		t.Errorf("DayKey() в Сеуле = %q, ожидалось %q", got, want)
	}
	if got, want := newYork.DayKey(moment), "2026-08-19"; got != want {
		t.Errorf("DayKey() в Нью-Йорке = %q, ожидалось %q", got, want)
	}
	if seoul.DayStart(moment).Equal(newYork.DayStart(moment)) {
		t.Error("начало суток в разных зонах не должно совпадать")
	}
}

func TestSameDay(t *testing.T) {
	t.Parallel()

	moscow := user.MustParseTimezone("Europe/Moscow")
	loc := moscow.Location()

	morning := time.Date(2026, 8, 19, 9, 0, 0, 0, loc)
	evening := time.Date(2026, 8, 19, 23, 30, 0, 0, loc)
	night := time.Date(2026, 8, 20, 0, 30, 0, 0, loc)

	if !moscow.SameDay(morning, evening) {
		t.Error("утро и вечер одного дня должны попадать в одни сутки")
	}
	if moscow.SameDay(evening, night) {
		t.Error("моменты по разные стороны полуночи не должны попадать в одни сутки")
	}
}

func TestDayStartOnDSTTransition(t *testing.T) {
	t.Parallel()

	// 8 марта 2026 года в Нью-Йорке переводят часы вперёд в 02:00, но полночь
	// в этот день существует — начало суток обычное.
	newYork := user.MustParseTimezone("America/New_York")
	loc := newYork.Location()

	afterShift := time.Date(2026, 3, 8, 14, 0, 0, 0, loc)
	want := time.Date(2026, 3, 8, 0, 0, 0, 0, loc)
	if got := newYork.DayStart(afterShift); !got.Equal(want) {
		t.Errorf("DayStart() = %v, ожидалось %v", got, want)
	}

	// Сутки перехода короче 24 часов: следующая полночь наступает через 23 часа.
	next := newYork.DayStart(time.Date(2026, 3, 9, 10, 0, 0, 0, loc))
	if got := next.Sub(want); got != 23*time.Hour {
		t.Errorf("длительность суток перехода = %v, ожидалось 23h", got)
	}
}

func TestDayStartWhenMidnightDoesNotExist(t *testing.T) {
	t.Parallel()

	// На Кубе переход на летнее время происходит ровно в 00:00: полуночи
	// в этот день нет. Начало суток обязано существовать всё равно.
	havana := user.MustParseTimezone("America/Havana")
	loc := havana.Location()

	day := time.Date(2026, 3, 8, 12, 0, 0, 0, loc)
	start := havana.DayStart(day)

	if !havana.SameDay(start, day) {
		t.Errorf("DayStart() = %v выпало из суток %v", start, day)
	}
	if start.After(day) {
		t.Errorf("DayStart() = %v оказалось позже самого момента %v", start, day)
	}
	if h := start.Hour(); h != 0 && h != 1 {
		t.Errorf("час начала суток = %d, ожидалось 0 или 1 (нормализация несуществующей полуночи)", h)
	}
}

func TestNewTimezone(t *testing.T) {
	t.Parallel()

	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("LoadLocation() вернул ошибку: %v", err)
	}
	tz := user.NewTimezone(loc)
	if tz.String() != "Europe/Berlin" {
		t.Errorf("String() = %q, ожидалось Europe/Berlin", tz)
	}
	if user.UTCTimezone().String() != "UTC" {
		t.Errorf("UTCTimezone() = %q, ожидалось UTC", user.UTCTimezone())
	}
}

func TestMustParseTimezonePanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("MustParseTimezone не паникует на неизвестной зоне")
		}
	}()
	user.MustParseTimezone("Europe/Atlantis")
}
