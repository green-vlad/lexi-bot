//go:build integration

package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"lexi-bot/internal/adapter/storage/postgres"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/test/pgtest"
)

func TestOutboxSchedulesOnce(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewOutboxRepo(pool)

	f := newCourse(t, pool, 1)
	at := testNow.Truncate(time.Minute)
	reminder := port.Notification{
		UserID: f.user.ID, Kind: port.NotificationReminder, ScheduledFor: at,
	}

	added, err := repo.Schedule(ctx, []port.Notification{reminder})
	if err != nil {
		t.Fatalf("Schedule() вернул ошибку: %v", err)
	}
	if added != 1 {
		t.Fatalf("добавлено %d, ожидалось одно", added)
	}

	// Повтор того же момента ничего не добавляет: уникальность и есть
	// гарантия того, что человек получит напоминание один раз.
	added, err = repo.Schedule(ctx, []port.Notification{reminder})
	if err != nil {
		t.Fatalf("повторный Schedule() вернул ошибку: %v", err)
	}
	if added != 0 {
		t.Errorf("добавлено ещё %d, ожидался ноль", added)
	}

	// А назавтра момент другой, и напоминание ставится снова.
	tomorrow := reminder
	tomorrow.ScheduledFor = at.AddDate(0, 0, 1)
	added, err = repo.Schedule(ctx, []port.Notification{tomorrow})
	if err != nil {
		t.Fatalf("Schedule() вернул ошибку: %v", err)
	}
	if added != 1 {
		t.Errorf("назавтра добавлено %d, ожидалось одно", added)
	}
}

func TestOutboxSurvivesConcurrentTicks(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewOutboxRepo(pool)

	f := newCourse(t, pool, 1)
	at := testNow.Truncate(time.Minute)

	// Восемь одновременных тиков — то же, что два инстанса бота, которым
	// одновременно пришло время напоминать. Строка должна быть одна.
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		total int
		errs  []error
	)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			added, err := repo.Schedule(ctx, []port.Notification{{
				UserID: f.user.ID, Kind: port.NotificationReminder, ScheduledFor: at,
			}})

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			total += added
		}()
	}
	wg.Wait()

	for _, err := range errs {
		t.Errorf("Schedule() вернул ошибку: %v", err)
	}
	if total != 1 {
		t.Errorf("добавлено %d строк, ожидалась одна", total)
	}

	var rows int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM outbox_notifications").Scan(&rows); err != nil {
		t.Fatalf("подсчёт очереди не прошёл: %v", err)
	}
	if rows != 1 {
		t.Errorf("в очереди %d строк, ожидалась одна", rows)
	}
}

func TestOutboxPendingAndMarkSent(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewOutboxRepo(pool)

	f := newCourse(t, pool, 1)
	at := testNow.Truncate(time.Minute)

	if _, err := repo.Schedule(ctx, []port.Notification{
		{UserID: f.user.ID, Kind: port.NotificationReminder, ScheduledFor: at.Add(-time.Hour)},
		{UserID: f.user.ID, Kind: port.NotificationReminder, ScheduledFor: at},
		{UserID: f.user.ID, Kind: port.NotificationReminder, ScheduledFor: at.Add(time.Hour)},
	}); err != nil {
		t.Fatalf("Schedule() вернул ошибку: %v", err)
	}

	// Будущее не выдаётся: рассылка не должна опережать расписание.
	pending, err := repo.Pending(ctx, at, 10)
	if err != nil {
		t.Fatalf("Pending() вернул ошибку: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("к отправке %d, ожидалось два: %+v", len(pending), pending)
	}
	// Порядок от старых к свежим: просроченное уходит первым.
	if !pending[0].ScheduledFor.Before(pending[1].ScheduledFor) {
		t.Errorf("порядок очереди = %+v", pending)
	}
	if pending[0].Sent() {
		t.Error("неотправленное сообщение помечено отправленным")
	}

	sentAt := at.Add(time.Minute)
	if err := repo.MarkSent(ctx, []port.NotificationID{pending[0].ID}, sentAt); err != nil {
		t.Fatalf("MarkSent() вернул ошибку: %v", err)
	}

	pending, err = repo.Pending(ctx, at, 10)
	if err != nil {
		t.Fatalf("Pending() вернул ошибку: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("к отправке %d, ожидалось одно", len(pending))
	}

	// Повторная отметка не переписывает чужой момент отправки: два воркера,
	// подхватившие одну строку, не должны спорить за неё.
	if err := repo.MarkSent(ctx, []port.NotificationID{1}, sentAt.Add(time.Hour)); err != nil {
		t.Fatalf("повторный MarkSent() вернул ошибку: %v", err)
	}
	var stored time.Time
	if err := pool.QueryRow(ctx, "SELECT sent_at FROM outbox_notifications WHERE id = 1").Scan(&stored); err != nil {
		t.Fatalf("чтение момента отправки не прошло: %v", err)
	}
	if !stored.Equal(sentAt) {
		t.Errorf("момент отправки = %v, ожидался прежний %v", stored, sentAt)
	}
}

func TestRemindingSkipsPausedAndDeleted(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewSettingsRepo(pool)

	f := newCourse(t, pool, 1)

	settings := user.DefaultSettings(user.MustParseTimezone("Asia/Seoul"))
	settings, err := settings.WithReminderAt(user.MustParseTimeOfDay("21:30"))
	if err != nil {
		t.Fatalf("WithReminderAt() вернул ошибку: %v", err)
	}
	if err := repo.Save(ctx, f.user.ID, settings); err != nil {
		t.Fatalf("Save() вернул ошибку: %v", err)
	}

	got, err := repo.Reminding(ctx)
	if err != nil {
		t.Fatalf("Reminding() вернул ошибку: %v", err)
	}
	if len(got) != 1 || got[0].UserID != f.user.ID {
		t.Fatalf("получатели = %+v", got)
	}
	if got[0].Timezone.String() != "Asia/Seoul" || got[0].At.String() != "21:30" {
		t.Errorf("получатель = %+v", got[0])
	}

	// Курс на паузе: напоминание пришло бы о занятии, которого нет.
	if _, err := pool.Exec(ctx, "UPDATE user_courses SET status = 'paused' WHERE user_id = $1",
		int64(f.user.ID)); err != nil {
		t.Fatalf("обновление курса не прошло: %v", err)
	}
	if got, err = repo.Reminding(ctx); err != nil || len(got) != 0 {
		t.Errorf("получатели = %+v, %v: курс на паузе", got, err)
	}

	// Удалённому не пишут вовсе.
	if _, err := pool.Exec(ctx, "UPDATE user_courses SET status = 'active' WHERE user_id = $1",
		int64(f.user.ID)); err != nil {
		t.Fatalf("обновление курса не прошло: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE users SET deleted_at = now() WHERE id = $1",
		int64(f.user.ID)); err != nil {
		t.Fatalf("удаление пользователя не прошло: %v", err)
	}
	if got, err = repo.Reminding(ctx); err != nil || len(got) != 0 {
		t.Errorf("получатели = %+v, %v: пользователь удалён", got, err)
	}
}

func TestRemindingSkipsWithoutReminder(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewSettingsRepo(pool)

	f := newCourse(t, pool, 1)
	// Напоминание выключено — по умолчанию оно и есть выключено.
	if err := repo.Save(ctx, f.user.ID, user.DefaultSettings(user.UTCTimezone())); err != nil {
		t.Fatalf("Save() вернул ошибку: %v", err)
	}

	got, err := repo.Reminding(ctx)
	if err != nil {
		t.Fatalf("Reminding() вернул ошибку: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("получатели = %+v, ожидалось пусто", got)
	}
}
