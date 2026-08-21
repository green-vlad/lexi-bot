//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"lexi-bot/internal/adapter/storage/postgres"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/test/pgtest"
)

func TestSettingsRoundTrip(t *testing.T) {
	pool := pgtest.New(t)
	repo := postgres.NewSettingsRepo(pool)
	ctx := context.Background()

	owner := ensureUser(t, pool, 777)

	want := user.DefaultSettings(user.MustParseTimezone("Asia/Seoul"))
	want, err := want.WithNewPerDay(25)
	if err != nil {
		t.Fatalf("WithNewPerDay() вернул ошибку: %v", err)
	}
	want, err = want.WithMaxReviewsPerDay(300)
	if err != nil {
		t.Fatalf("WithMaxReviewsPerDay() вернул ошибку: %v", err)
	}
	want, err = want.WithReminderAt(user.MustParseTimeOfDay("21:30"))
	if err != nil {
		t.Fatalf("WithReminderAt() вернул ошибку: %v", err)
	}
	want, err = want.WithQuizModes([]study.Mode{study.ModeTyping, study.ModeRecall})
	if err != nil {
		t.Fatalf("WithQuizModes() вернул ошибку: %v", err)
	}
	want.ReverseDirection = true

	if err := repo.Save(ctx, owner.ID, want); err != nil {
		t.Fatalf("Save() вернул ошибку: %v", err)
	}

	got, err := repo.Get(ctx, owner.ID)
	if err != nil {
		t.Fatalf("Get() вернул ошибку: %v", err)
	}

	if got.NewPerDay != want.NewPerDay || got.MaxReviewsPerDay != want.MaxReviewsPerDay {
		t.Errorf("лимиты = %d/%d, ожидались %d/%d",
			got.NewPerDay, got.MaxReviewsPerDay, want.NewPerDay, want.MaxReviewsPerDay)
	}
	if got.Timezone.String() != "Asia/Seoul" {
		t.Errorf("Timezone = %q, ожидалось Asia/Seoul", got.Timezone)
	}
	if got.ReminderAt.String() != "21:30" {
		t.Errorf("ReminderAt = %q, ожидалось 21:30", got.ReminderAt)
	}
	if !got.ReverseDirection {
		t.Error("ReverseDirection не сохранился")
	}
	if len(got.QuizModes) != 2 || !got.ModeEnabled(study.ModeRecall) || !got.ModeEnabled(study.ModeTyping) {
		t.Errorf("QuizModes = %v, ожидались recall и typing", got.QuizModes)
	}
	if got.ModeEnabled(study.ModeChoice) {
		t.Error("режим choice не включался")
	}
}

func TestSettingsSaveOverwrites(t *testing.T) {
	pool := pgtest.New(t)
	repo := postgres.NewSettingsRepo(pool)
	ctx := context.Background()

	owner := ensureUser(t, pool, 777)

	first := user.DefaultSettings(user.UTCTimezone())
	first, err := first.WithReminderAt(user.MustParseTimeOfDay("09:00"))
	if err != nil {
		t.Fatalf("WithReminderAt() вернул ошибку: %v", err)
	}
	if err := repo.Save(ctx, owner.ID, first); err != nil {
		t.Fatalf("Save() вернул ошибку: %v", err)
	}

	// Выключение напоминания — это NULL в колонке, а не полночь.
	second := user.DefaultSettings(user.UTCTimezone())
	if err := repo.Save(ctx, owner.ID, second); err != nil {
		t.Fatalf("повторный Save() вернул ошибку: %v", err)
	}

	got, err := repo.Get(ctx, owner.ID)
	if err != nil {
		t.Fatalf("Get() вернул ошибку: %v", err)
	}
	if got.RemindersEnabled() {
		t.Errorf("напоминание = %q, ожидалось выключенным", got.ReminderAt)
	}

	var rows int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM user_settings").Scan(&rows); err != nil {
		t.Fatalf("подсчёт настроек не прошёл: %v", err)
	}
	if rows != 1 {
		t.Errorf("строк настроек %d, ожидалась одна", rows)
	}
}

func TestSettingsMidnightReminderIsNotDisabled(t *testing.T) {
	pool := pgtest.New(t)
	repo := postgres.NewSettingsRepo(pool)
	ctx := context.Background()

	owner := ensureUser(t, pool, 777)

	// Полночь — обычное время напоминания, и отличаться от «выключено»
	// она обязана и в базе тоже.
	s, err := user.DefaultSettings(user.UTCTimezone()).WithReminderAt(user.MustParseTimeOfDay("00:00"))
	if err != nil {
		t.Fatalf("WithReminderAt() вернул ошибку: %v", err)
	}
	if err := repo.Save(ctx, owner.ID, s); err != nil {
		t.Fatalf("Save() вернул ошибку: %v", err)
	}

	got, err := repo.Get(ctx, owner.ID)
	if err != nil {
		t.Fatalf("Get() вернул ошибку: %v", err)
	}
	if !got.RemindersEnabled() {
		t.Error("напоминание в полночь должно оставаться включённым")
	}
	if got.ReminderAt.String() != "00:00" {
		t.Errorf("ReminderAt = %q, ожидалось 00:00", got.ReminderAt)
	}
}

func TestSettingsNotFound(t *testing.T) {
	pool := pgtest.New(t)
	repo := postgres.NewSettingsRepo(pool)
	ctx := context.Background()

	owner := ensureUser(t, pool, 777)

	// У пользователя без настроек их нет: подставлять значения по умолчанию —
	// дело сценария онбординга, а не репозитория.
	if _, err := repo.Get(ctx, owner.ID); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("Get() = %v, ожидалась ErrNotFound", err)
	}
}

func TestSettingsRejectsUnknownUser(t *testing.T) {
	pool := pgtest.New(t)
	repo := postgres.NewSettingsRepo(pool)
	ctx := context.Background()

	err := repo.Save(ctx, 12345, user.DefaultSettings(user.UTCTimezone()))
	if !errors.Is(err, port.ErrNotFound) {
		t.Errorf("Save() для несуществующего пользователя = %v, ожидалась ErrNotFound", err)
	}
}

func TestSettingsRejectsBrokenValues(t *testing.T) {
	pool := pgtest.New(t)
	repo := postgres.NewSettingsRepo(pool)
	ctx := context.Background()

	owner := ensureUser(t, pool, 777)

	// Домен отсекает такое раньше базы — и должен, иначе ошибка всплыла бы
	// в виде нарушения CHECK посреди сохранения.
	broken := user.DefaultSettings(user.UTCTimezone())
	broken.NewPerDay = 1000
	if err := repo.Save(ctx, owner.ID, broken); !errors.Is(err, user.ErrOutOfRange) {
		t.Errorf("Save() = %v, ожидалась доменная ошибка ErrOutOfRange", err)
	}
}
