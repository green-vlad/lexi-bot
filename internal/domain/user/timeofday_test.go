package user_test

import (
	"errors"
	"testing"
	"time"

	"lexi-bot/internal/domain/user"
)

func TestParseTimeOfDay(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
	}{
		{"21:30", "21:30"},
		{"00:00", "00:00"},
		{"23:59", "23:59"},
		{"9:05", "09:05"},
		{"  7:00  ", "07:00"},
	}
	for _, tt := range tests {
		got, err := user.ParseTimeOfDay(tt.in)
		if err != nil {
			t.Fatalf("ParseTimeOfDay(%q) вернул ошибку: %v", tt.in, err)
		}
		if got.String() != tt.want {
			t.Errorf("ParseTimeOfDay(%q) = %q, ожидалось %q", tt.in, got, tt.want)
		}
		if !got.IsSet() {
			t.Errorf("ParseTimeOfDay(%q) вернул незаданное время", tt.in)
		}
	}
}

func TestParseTimeOfDayErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want error
	}{
		{"", user.ErrRequired},
		{"   ", user.ErrRequired},
		{"2130", user.ErrInvalid},
		{"21:3", user.ErrInvalid},
		{"21:30:00", user.ErrInvalid},
		{"вечером", user.ErrInvalid},
		{"24:00", user.ErrOutOfRange},
		{"-1:00", user.ErrOutOfRange},
		{"21:60", user.ErrOutOfRange},
	}
	for _, tt := range tests {
		got, err := user.ParseTimeOfDay(tt.in)
		if !errors.Is(err, tt.want) {
			t.Errorf("ParseTimeOfDay(%q) = %v, ожидалась ошибка %v", tt.in, err, tt.want)
		}
		if got.IsSet() {
			t.Errorf("при ошибке возвращено заданное время %q", got)
		}
	}
}

func TestTimeOfDayZeroValueIsNotMidnight(t *testing.T) {
	t.Parallel()

	// Ноль означает «напоминание выключено», а полночь — обычное время.
	var off user.TimeOfDay
	if off.IsSet() {
		t.Error("нулевое значение не должно считаться заданным")
	}
	if off.String() != "" {
		t.Errorf("String() = %q, ожидалась пустая строка", off)
	}
	if _, ok := off.On(time.Now(), user.UTCTimezone()); ok {
		t.Error("незаданное время не должно давать момент напоминания")
	}

	midnight := user.MustParseTimeOfDay("00:00")
	if !midnight.IsSet() {
		t.Error("полночь — заданное время")
	}
}

func TestNewTimeOfDayBounds(t *testing.T) {
	t.Parallel()

	if _, err := user.NewTimeOfDay(0, 0); err != nil {
		t.Errorf("NewTimeOfDay(0, 0) вернул ошибку: %v", err)
	}
	if _, err := user.NewTimeOfDay(23, 59); err != nil {
		t.Errorf("NewTimeOfDay(23, 59) вернул ошибку: %v", err)
	}
	for _, tc := range [][2]int{{24, 0}, {-1, 0}, {0, 60}, {0, -1}} {
		if _, err := user.NewTimeOfDay(tc[0], tc[1]); !errors.Is(err, user.ErrOutOfRange) {
			t.Errorf("NewTimeOfDay(%d, %d) = %v, ожидалась ошибка ErrOutOfRange", tc[0], tc[1], err)
		}
	}
}

func TestTimeOfDayOn(t *testing.T) {
	t.Parallel()

	tz := user.MustParseTimezone("Asia/Seoul")
	loc := tz.Location()
	at := user.MustParseTimeOfDay("21:30")

	// Момент внутри суток задан в UTC — время напоминания всё равно местное.
	day := time.Date(2026, 8, 19, 16, 0, 0, 0, time.UTC) // 20 августа 01:00 в Сеуле
	want := time.Date(2026, 8, 20, 21, 30, 0, 0, loc)

	got, ok := at.On(day, tz)
	if !ok {
		t.Fatal("время задано, ожидался момент")
	}
	if !got.Equal(want) {
		t.Errorf("On() = %v, ожидалось %v", got, want)
	}
	if h, m := got.In(loc).Hour(), got.In(loc).Minute(); h != 21 || m != 30 {
		t.Errorf("местное время = %02d:%02d, ожидалось 21:30", h, m)
	}
}

func TestTimeOfDayHourMinute(t *testing.T) {
	t.Parallel()

	at := user.MustParseTimeOfDay("07:05")
	if at.Hour() != 7 || at.Minute() != 5 {
		t.Errorf("Hour()/Minute() = %d/%d, ожидалось 7/5", at.Hour(), at.Minute())
	}
}

func TestMustParseTimeOfDayPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("MustParseTimeOfDay не паникует на некорректном значении")
		}
	}()
	user.MustParseTimeOfDay("25:00")
}
