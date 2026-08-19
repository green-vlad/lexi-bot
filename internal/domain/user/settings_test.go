package user_test

import (
	"errors"
	"testing"
	"time"

	"lexi-bot/internal/domain/user"
)

func TestDefaultSettingsAreValid(t *testing.T) {
	t.Parallel()

	s := user.DefaultSettings(user.MustParseTimezone("Europe/Moscow"))
	if err := s.Validate(); err != nil {
		t.Fatalf("настройки по умолчанию не проходят валидацию: %v", err)
	}
	if s.RemindersEnabled() {
		t.Error("напоминание по умолчанию должно быть выключено")
	}
	if s.ReverseDirection {
		t.Error("обратное направление по умолчанию должно быть выключено")
	}
}

func TestSettingsBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		newPer  int
		reviews int
		wantErr bool
	}{
		{"нижняя граница новых слов", user.MinNewPerDay, user.DefaultReviewsPerDay, false},
		{"верхняя граница новых слов", user.MaxNewPerDay, user.DefaultReviewsPerDay, false},
		{"новых слов меньше минимума", user.MinNewPerDay - 1, user.DefaultReviewsPerDay, true},
		{"новых слов больше максимума", user.MaxNewPerDay + 1, user.DefaultReviewsPerDay, true},
		{"нижняя граница повторений", user.DefaultNewPerDay, user.MinReviewsPerDay, false},
		{"верхняя граница повторений", user.DefaultNewPerDay, user.MaxReviewsPerDay, false},
		{"повторений меньше минимума", user.DefaultNewPerDay, user.MinReviewsPerDay - 1, true},
		{"повторений больше максимума", user.DefaultNewPerDay, user.MaxReviewsPerDay + 1, true},
		{"ноль новых слов", 0, user.DefaultReviewsPerDay, true},
		{"отрицательное значение", -5, user.DefaultReviewsPerDay, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := user.Settings{
				NewPerDay:        tt.newPer,
				MaxReviewsPerDay: tt.reviews,
				Timezone:         user.UTCTimezone(),
			}
			err := s.Validate()
			if tt.wantErr {
				if !errors.Is(err, user.ErrOutOfRange) {
					t.Fatalf("Validate() = %v, ожидалась ошибка ErrOutOfRange", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() вернул ошибку на допустимых значениях: %v", err)
			}
		})
	}
}

func TestSettingsRequireTimezone(t *testing.T) {
	t.Parallel()

	s := user.Settings{NewPerDay: 10, MaxReviewsPerDay: 100}
	if err := s.Validate(); !errors.Is(err, user.ErrRequired) {
		t.Fatalf("Validate() = %v, ожидалась ошибка ErrRequired", err)
	}
}

func TestSettingsWithKeepsOriginalOnError(t *testing.T) {
	t.Parallel()

	base := user.DefaultSettings(user.UTCTimezone())

	updated, err := base.WithNewPerDay(25)
	if err != nil {
		t.Fatalf("WithNewPerDay() вернул ошибку: %v", err)
	}
	if updated.NewPerDay != 25 {
		t.Errorf("NewPerDay = %d, ожидалось 25", updated.NewPerDay)
	}
	if base.NewPerDay != user.DefaultNewPerDay {
		t.Errorf("исходные настройки изменились: NewPerDay = %d", base.NewPerDay)
	}

	if _, err := base.WithNewPerDay(user.MaxNewPerDay + 1); !errors.Is(err, user.ErrOutOfRange) {
		t.Errorf("WithNewPerDay() = %v, ожидалась ошибка ErrOutOfRange", err)
	}
	if _, err := base.WithMaxReviewsPerDay(0); !errors.Is(err, user.ErrOutOfRange) {
		t.Errorf("WithMaxReviewsPerDay() = %v, ожидалась ошибка ErrOutOfRange", err)
	}
	if _, err := base.WithTimezone(user.Timezone{}); !errors.Is(err, user.ErrRequired) {
		t.Errorf("WithTimezone() = %v, ожидалась ошибка ErrRequired", err)
	}
}

func TestSettingsDayStartFollowsTimezoneChange(t *testing.T) {
	t.Parallel()

	// Пользователь переехал: тот же момент времени попадает в другие сутки,
	// и дневной лимит должен считаться по новой зоне.
	moment := time.Date(2026, 8, 19, 20, 0, 0, 0, time.UTC)

	before := user.DefaultSettings(user.MustParseTimezone("America/New_York"))
	after, err := before.WithTimezone(user.MustParseTimezone("Asia/Seoul"))
	if err != nil {
		t.Fatalf("WithTimezone() вернул ошибку: %v", err)
	}

	if before.DayStart(moment).Equal(after.DayStart(moment)) {
		t.Fatal("после смены таймзоны начало суток должно измениться")
	}
	if got, want := after.DayStart(moment).Format(time.DateOnly), "2026-08-20"; got != want {
		t.Errorf("сутки в новой зоне = %q, ожидалось %q", got, want)
	}
	if !before.SameDay(moment, moment.Add(3*time.Hour)) {
		t.Error("моменты внутри одних суток Нью-Йорка должны считаться одним днём")
	}
	if after.SameDay(moment, moment.Add(-6*time.Hour)) {
		t.Error("моменты по разные стороны сеульской полуночи не должны совпадать")
	}
}

func TestSettingsReminders(t *testing.T) {
	t.Parallel()

	tz := user.MustParseTimezone("Europe/Moscow")
	loc := tz.Location()

	s, err := user.DefaultSettings(tz).WithReminderAt(user.MustParseTimeOfDay("21:30"))
	if err != nil {
		t.Fatalf("WithReminderAt() вернул ошибку: %v", err)
	}
	if !s.RemindersEnabled() {
		t.Fatal("напоминание должно быть включено")
	}

	// До времени напоминания — сегодня.
	now := time.Date(2026, 8, 19, 9, 0, 0, 0, loc)
	want := time.Date(2026, 8, 19, 21, 30, 0, 0, loc)
	got, ok := s.NextReminder(now)
	if !ok || !got.Equal(want) {
		t.Errorf("NextReminder(%v) = %v (%t), ожидалось %v", now, got, ok, want)
	}

	// После — переносится на следующие сутки, а не на «через 24 часа».
	now = time.Date(2026, 8, 19, 22, 0, 0, 0, loc)
	want = time.Date(2026, 8, 20, 21, 30, 0, 0, loc)
	got, ok = s.NextReminder(now)
	if !ok || !got.Equal(want) {
		t.Errorf("NextReminder(%v) = %v (%t), ожидалось %v", now, got, ok, want)
	}

	// Ровно в момент напоминания следующее — уже завтрашнее.
	got, ok = s.NextReminder(time.Date(2026, 8, 19, 21, 30, 0, 0, loc))
	if !ok || !got.Equal(want) {
		t.Errorf("NextReminder() в момент напоминания = %v (%t), ожидалось %v", got, ok, want)
	}

	off := user.DefaultSettings(tz)
	if _, ok := off.NextReminder(now); ok {
		t.Error("при выключенном напоминании NextReminder() должен возвращать false")
	}
}

func TestSettingsReminderAcrossDSTShift(t *testing.T) {
	t.Parallel()

	// В ночь перевода часов вперёд следующее напоминание — в те же 21:30
	// местного времени, хотя между ними прошло 23 часа, а не 24.
	tz := user.MustParseTimezone("America/New_York")
	loc := tz.Location()

	s, err := user.DefaultSettings(tz).WithReminderAt(user.MustParseTimeOfDay("21:30"))
	if err != nil {
		t.Fatalf("WithReminderAt() вернул ошибку: %v", err)
	}

	now := time.Date(2026, 3, 7, 22, 0, 0, 0, loc) // после напоминания 7 марта
	next, ok := s.NextReminder(now)
	if !ok {
		t.Fatal("напоминание должно быть включено")
	}
	want := time.Date(2026, 3, 8, 21, 30, 0, 0, loc)
	if !next.Equal(want) {
		t.Errorf("NextReminder() = %v, ожидалось %v", next, want)
	}
	if got := next.Sub(time.Date(2026, 3, 7, 21, 30, 0, 0, loc)); got != 23*time.Hour {
		t.Errorf("интервал между напоминаниями = %v, ожидалось 23h (сутки перехода короче)", got)
	}
}
