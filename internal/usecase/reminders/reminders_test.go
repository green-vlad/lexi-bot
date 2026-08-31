package reminders_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/internal/usecase/reminders"
)

// fakeSettings отдаёт получателей напоминаний.
type fakeSettings struct {
	recipients []port.UserReminder
	failWith   error
}

func (f *fakeSettings) Reminding(context.Context) ([]port.UserReminder, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	return f.recipients, nil
}

func (f *fakeSettings) Get(context.Context, user.ID) (user.Settings, error) {
	return user.Settings{}, port.ErrNotFound
}
func (f *fakeSettings) Save(context.Context, user.ID, user.Settings) error { return nil }

// fakeOutbox ведёт очередь так же, как база: уникальность по получателю,
// виду и моменту. Заглушка, принимающая всё подряд, сделала бы бессмысленной
// проверку «напоминание ставится ровно один раз».
type fakeOutbox struct {
	queued   []port.Notification
	failWith error
	// busy изображает второй инстанс, держащий блокировку планировщика.
	busy bool
}

func (f *fakeOutbox) Schedule(_ context.Context, notifications []port.Notification) (int, error) {
	if f.failWith != nil {
		return 0, f.failWith
	}
	if f.busy {
		return 0, nil
	}

	added := 0
	for _, n := range notifications {
		if f.has(&n) {
			continue
		}
		f.queued = append(f.queued, n)
		added++
	}
	return added, nil
}

func (f *fakeOutbox) has(want *port.Notification) bool {
	for _, n := range f.queued {
		if n.UserID == want.UserID && n.Kind == want.Kind && n.ScheduledFor.Equal(want.ScheduledFor) {
			return true
		}
	}
	return false
}

func (f *fakeOutbox) Pending(context.Context, time.Time, int) ([]port.Notification, error) {
	return nil, nil
}
func (f *fakeOutbox) MarkSent(context.Context, []port.NotificationID, time.Time) error { return nil }

type fixture struct {
	service  *reminders.Service
	settings *fakeSettings
	outbox   *fakeOutbox
	now      time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	f := &fixture{
		settings: &fakeSettings{},
		outbox:   &fakeOutbox{},
		// 21:30 в Сеуле — 12:30 UTC. Разница нужна, чтобы подмена
		// таймзоны пользователя на UTC сразу ломала тест.
		now: time.Date(2026, 8, 31, 12, 30, 0, 0, time.UTC),
	}

	service, err := reminders.New(reminders.Deps{
		Settings: f.settings, Outbox: f.outbox,
		Clock: port.ClockFunc(func() time.Time { return f.now }),
	})
	if err != nil {
		t.Fatalf("reminders.New() вернул ошибку: %v", err)
	}
	f.service = service
	return f
}

func (f *fixture) recipient(tz, at string) {
	f.settings.recipients = append(f.settings.recipients, port.UserReminder{
		UserID:   user.ID(len(f.settings.recipients) + 1),
		Timezone: user.MustParseTimezone(tz),
		At:       user.MustParseTimeOfDay(at),
	})
}

func (f *fixture) tick(t *testing.T) reminders.Report {
	t.Helper()

	report, err := f.service.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick() вернул ошибку: %v", err)
	}
	return report
}

func TestTickSchedulesWhenLocalTimeCame(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.recipient("Asia/Seoul", "21:30")

	report := f.tick(t)
	if report.Scheduled != 1 {
		t.Fatalf("поставлено %d, ожидалось одно", report.Scheduled)
	}
	if len(f.outbox.queued) != 1 {
		t.Fatalf("очередь = %+v", f.outbox.queued)
	}

	queued := f.outbox.queued[0]
	if queued.Kind != port.NotificationReminder {
		t.Errorf("вид = %q", queued.Kind)
	}
	// Момент назначен на местные 21:30, а не на 21:30 UTC.
	if !queued.ScheduledFor.Equal(f.now) {
		t.Errorf("момент = %v, ожидался %v", queued.ScheduledFor, f.now)
	}
}

func TestTickSchedulesOnlyOncePerDay(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.recipient("Asia/Seoul", "21:30")

	if got := f.tick(t).Scheduled; got != 1 {
		t.Fatalf("первый тик поставил %d", got)
	}

	// Следующий тик попадает в то же окно: момент напоминания в сутках
	// один, и очередь его не задваивает.
	f.now = f.now.Add(5 * time.Minute)
	if got := f.tick(t).Scheduled; got != 0 {
		t.Errorf("второй тик поставил %d, ожидался ноль", got)
	}
	if len(f.outbox.queued) != 1 {
		t.Errorf("очередь = %+v, ожидалась одна запись", f.outbox.queued)
	}

	// А назавтра напоминание ставится снова.
	f.now = f.now.AddDate(0, 0, 1)
	if got := f.tick(t).Scheduled; got != 1 {
		t.Errorf("назавтра поставлено %d, ожидалось одно", got)
	}
}

func TestTickIgnoresTimeNotYetCome(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	// 23:00 по Сеулу ещё впереди: вечернее напоминание не должно уходить
	// днём того же дня.
	f.recipient("Asia/Seoul", "23:00")

	if got := f.tick(t); got.Considered != 1 || got.Scheduled != 0 {
		t.Errorf("тик = %+v, ожидалось «рассмотрен, но не поставлен»", got)
	}
}

func TestTickIgnoresStaleTime(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.recipient("Asia/Seoul", "21:30")
	// Бот простоял дольше окна: выплёскивать вчерашние напоминания разом
	// он не должен.
	f.now = f.now.Add(reminders.Window + time.Minute)

	if got := f.tick(t).Scheduled; got != 0 {
		t.Errorf("поставлено %d, ожидался ноль", got)
	}
}

func TestTickCatchesUpMissedTick(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.recipient("Asia/Seoul", "21:30")
	// Тик пропущен: перезапуск или подвисший запрос. Напоминание должно
	// уйти, а не потеряться.
	f.now = f.now.Add(reminders.Window - time.Minute)

	if got := f.tick(t).Scheduled; got != 1 {
		t.Errorf("поставлено %d, ожидалось одно", got)
	}
}

func TestTickRespectsEachTimezone(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	// Сейчас 12:30 UTC: в Сеуле 21:30, в Москве 15:30, в Лондоне 13:30.
	f.recipient("Asia/Seoul", "21:30")
	f.recipient("Europe/Moscow", "21:30")
	f.recipient("Europe/London", "13:30")

	report := f.tick(t)
	if report.Considered != 3 {
		t.Errorf("рассмотрено %d, ожидалось три", report.Considered)
	}
	// Время подошло только у двоих: москвичу до 21:30 ещё шесть часов.
	if report.Scheduled != 2 {
		t.Fatalf("поставлено %d, ожидалось два: %+v", report.Scheduled, f.outbox.queued)
	}
	for _, n := range f.outbox.queued {
		if n.UserID == 2 {
			t.Error("напоминание ушло тому, у кого время ещё не наступило")
		}
	}
}

func TestTickSkipsWhenAnotherInstanceHoldsLock(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.recipient("Asia/Seoul", "21:30")
	f.outbox.busy = true

	// Тик уже выполняет другой инстанс: ждать нечего, он поставит
	// в очередь ровно то же самое.
	if got := f.tick(t).Scheduled; got != 0 {
		t.Errorf("поставлено %d, ожидался ноль", got)
	}
}

func TestTickWithoutRecipients(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	report := f.tick(t)
	if report.Considered != 0 || report.Scheduled != 0 {
		t.Errorf("тик = %+v, ожидались нули", report)
	}
}

func TestTickReportsFailures(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.settings.failWith = errors.New("база недоступна")

	if _, err := f.service.Tick(context.Background()); err == nil {
		t.Error("недоступная база должна быть ошибкой")
	}
}

func TestNewNeedsDependencies(t *testing.T) {
	t.Parallel()

	if _, err := reminders.New(reminders.Deps{}); err == nil {
		t.Error("сценарий без зависимостей должен быть ошибкой")
	}
}
