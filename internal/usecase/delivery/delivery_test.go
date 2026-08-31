package delivery_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"lexi-bot/internal/adapter/i18n"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/delivery"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/locales"
)

// fakeOutbox ведёт очередь: Pending отдаёт неотправленное, MarkSent
// закрывает строки. Заглушка, забывающая отметку, скрыла бы повторную
// отправку одного и того же.
type fakeOutbox struct {
	mu       sync.Mutex
	queued   []port.Notification
	failWith error
	marked   int
}

func (f *fakeOutbox) Pending(_ context.Context, now time.Time, limit int) ([]port.Notification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failWith != nil {
		return nil, f.failWith
	}
	var out []port.Notification
	for _, n := range f.queued {
		if len(out) >= limit {
			break
		}
		if !n.Sent() && !n.ScheduledFor.After(now) {
			out = append(out, n)
		}
	}
	return out, nil
}

func (f *fakeOutbox) MarkSent(_ context.Context, ids []port.NotificationID, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, id := range ids {
		for i := range f.queued {
			if f.queued[i].ID == id {
				f.queued[i].SentAt = at
				f.marked++
			}
		}
	}
	return nil
}

func (f *fakeOutbox) Schedule(context.Context, []port.Notification) (int, error) { return 0, nil }

// fakeMessenger отвечает по сценарию: кому-то успешно, кому-то отказом.
type fakeMessenger struct {
	mu       sync.Mutex
	sent     []port.OutgoingMessage
	blocked  map[port.ChatID]bool
	failWith map[port.ChatID]error
}

func (f *fakeMessenger) SendMessage(_ context.Context, msg port.OutgoingMessage) (port.MessageID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.blocked[msg.ChatID] {
		return 0, fmt.Errorf("отправить: %w", port.ErrBlocked)
	}
	if err := f.failWith[msg.ChatID]; err != nil {
		return 0, err
	}
	f.sent = append(f.sent, msg)
	return port.MessageID(len(f.sent)), nil
}

func (f *fakeMessenger) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func (f *fakeMessenger) EditMessage(context.Context, port.MessageEdit) error       { return nil }
func (f *fakeMessenger) AnswerCallback(context.Context, port.CallbackAnswer) error { return nil }
func (f *fakeMessenger) SendDocument(context.Context, port.Document) error         { return nil }
func (f *fakeMessenger) DownloadFile(context.Context, string, int64) ([]byte, error) {
	return nil, nil
}

// fakeUsers помнит деактивацию: без этого проверять реакцию на блокировку
// было бы нечем.
type fakeUsers struct {
	mu      sync.Mutex
	users   map[user.ID]user.User
	deleted map[user.ID]time.Time
}

func (f *fakeUsers) ByID(_ context.Context, id user.ID) (user.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if u, ok := f.users[id]; ok {
		if at, gone := f.deleted[id]; gone {
			u.DeletedAt = at
		}
		return u, nil
	}
	return user.User{}, port.ErrNotFound
}

func (f *fakeUsers) SoftDelete(_ context.Context, id user.ID, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.deleted[id] = at
	return nil
}

func (f *fakeUsers) Ensure(_ context.Context, u *user.User) (user.User, bool, error) {
	return *u, false, nil
}
func (f *fakeUsers) ByTelegramID(context.Context, user.TelegramID) (user.User, error) {
	return user.User{}, port.ErrNotFound
}
func (f *fakeUsers) SetUILang(context.Context, user.ID, user.UILang) error           { return nil }
func (f *fakeUsers) SetCurrentCourse(context.Context, user.ID, study.CourseID) error { return nil }
func (f *fakeUsers) Purge(context.Context, user.ID) error                            { return nil }

// countingLimiter считает, сколько раз у него спрашивали разрешение.
type countingLimiter struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (l *countingLimiter) Wait(context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.calls++
	return l.err
}

func (l *countingLimiter) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

type fixture struct {
	service   *delivery.Service
	outbox    *fakeOutbox
	messenger *fakeMessenger
	users     *fakeUsers
	limiter   *countingLimiter
	now       time.Time
}

func newFixture(t *testing.T, recipients int) *fixture {
	t.Helper()

	f := &fixture{
		outbox: &fakeOutbox{},
		messenger: &fakeMessenger{
			blocked:  map[port.ChatID]bool{},
			failWith: map[port.ChatID]error{},
		},
		users:   &fakeUsers{users: map[user.ID]user.User{}, deleted: map[user.ID]time.Time{}},
		limiter: &countingLimiter{},
		now:     time.Date(2026, 8, 31, 12, 30, 0, 0, time.UTC),
	}

	for i := 1; i <= recipients; i++ {
		id := user.ID(i)
		f.users.users[id] = user.User{ID: id, TelegramID: user.TelegramID(1000 + i), UILang: user.UILangRU}
		f.outbox.queued = append(f.outbox.queued, port.Notification{
			ID: port.NotificationID(i), UserID: id,
			Kind: port.NotificationReminder, ScheduledFor: f.now.Add(-time.Minute),
		})
	}

	catalog, err := i18n.NewCatalog(locales.FS)
	if err != nil {
		t.Fatalf("NewCatalog() вернул ошибку: %v", err)
	}

	service, err := delivery.New(&delivery.Deps{
		Outbox: f.outbox, Users: f.users, Messenger: f.messenger,
		Catalog: catalog, Limiter: f.limiter,
		Clock:   port.ClockFunc(func() time.Time { return f.now }),
		Workers: 3,
	})
	if err != nil {
		t.Fatalf("delivery.New() вернул ошибку: %v", err)
	}
	f.service = service
	return f
}

func (f *fixture) deliver(t *testing.T) delivery.Report {
	t.Helper()

	report, err := f.service.Deliver(context.Background())
	if err != nil {
		t.Fatalf("Deliver() вернул ошибку: %v", err)
	}
	return report
}

func (f *fixture) chat(id user.ID) port.ChatID {
	return port.ChatID(f.users.users[id].TelegramID)
}

func TestDeliverSendsAndClosesQueue(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 3)

	report := f.deliver(t)
	if report.Sent != 3 || report.Blocked != 0 || report.Failed != 0 {
		t.Errorf("итог = %+v, ожидалось три отправленных", report)
	}
	if f.messenger.count() != 3 {
		t.Errorf("отправлено %d сообщений", f.messenger.count())
	}
	// Текст берётся из каталога по виду уведомления и языку человека.
	if got := f.messenger.sent[0].Text; !strings.Contains(got, "/learn") {
		t.Errorf("текст = %q", got)
	}

	// Второй заход ничего не находит: строки закрыты.
	if got := f.deliver(t); got.Sent != 0 {
		t.Errorf("второй заход отправил %d, ожидался ноль", got.Sent)
	}
}

func TestDeliverAsksLimiterBeforeEverySend(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 5)

	f.deliver(t)
	// Ограничение у Telegram одно на бота, а не на горутину: разрешение
	// спрашивается перед каждой отправкой, сколько бы воркеров ни было.
	if got := f.limiter.count(); got != 5 {
		t.Errorf("ограничитель спрошен %d раз, ожидалось пять", got)
	}
}

func TestDeliverDeactivatesBlocked(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 3)
	f.messenger.blocked[f.chat(2)] = true

	report := f.deliver(t)
	if report.Sent != 2 || report.Blocked != 1 {
		t.Errorf("итог = %+v, ожидались две отправки и одна блокировка", report)
	}
	// Заблокировавший бота деактивирован — мягко, с сохранением прогресса.
	if _, gone := f.users.deleted[2]; !gone {
		t.Error("заблокировавший бота не деактивирован")
	}
	// И его строка закрыта: иначе она всплывала бы в каждом заходе.
	if f.outbox.queued[1].SentAt.IsZero() {
		t.Error("строка заблокировавшего осталась в очереди")
	}
	// Рассылка на этом не остановилась.
	if f.messenger.count() != 2 {
		t.Errorf("отправлено %d, ожидалось два", f.messenger.count())
	}
}

func TestDeliverKeepsFailedInQueue(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 3)
	f.messenger.failWith[f.chat(2)] = errors.New("сеть отвалилась")

	report := f.deliver(t)
	if report.Sent != 2 || report.Failed != 1 {
		t.Errorf("итог = %+v", report)
	}
	// Временная неудача строку не закрывает: она уйдёт со следующим заходом.
	if !f.outbox.queued[1].SentAt.IsZero() {
		t.Error("неудачная отправка закрыла строку")
	}
	if _, gone := f.users.deleted[2]; gone {
		t.Error("сбой сети принят за блокировку")
	}

	// Следующий заход добирает оставшееся.
	f.messenger.failWith = map[port.ChatID]error{}
	if got := f.deliver(t).Sent; got != 1 {
		t.Errorf("второй заход отправил %d, ожидалось одно", got)
	}
}

func TestDeliverSkipsDeletedRecipient(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 2)
	// Человек удалился между постановкой в очередь и отправкой.
	f.users.deleted[1] = f.now.Add(-time.Hour)

	report := f.deliver(t)
	if report.Sent != 1 || report.Blocked != 1 {
		t.Errorf("итог = %+v", report)
	}
	if f.messenger.count() != 1 {
		t.Errorf("отправлено %d: писали удалённому", f.messenger.count())
	}
}

func TestDeliverIgnoresFutureAndEmptyQueue(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 1)
	f.outbox.queued[0].ScheduledFor = f.now.Add(time.Hour)

	// Будущее не рассылается: очередь не должна опережать расписание.
	if got := f.deliver(t); got.Sent != 0 {
		t.Errorf("итог = %+v, ожидался ноль", got)
	}
	if f.outbox.marked != 0 {
		t.Error("пустой заход тронул очередь")
	}
}

func TestDeliverReportsRepoFailure(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 1)
	f.outbox.failWith = errors.New("база недоступна")

	if _, err := f.service.Deliver(context.Background()); err == nil {
		t.Error("недоступная база должна быть ошибкой")
	}
}

func TestNewNeedsDependencies(t *testing.T) {
	t.Parallel()

	if _, err := delivery.New(&delivery.Deps{}); err == nil {
		t.Error("сценарий без зависимостей должен быть ошибкой")
	}
}

func TestTickerLimiterSpacesSends(t *testing.T) {
	t.Parallel()

	const perSecond = 200
	interval := time.Second / perSecond

	limiter := delivery.NewTickerLimiter(perSecond)
	defer limiter.Stop()

	const waits = 4
	start := time.Now()
	for i := 0; i < waits; i++ {
		if err := limiter.Wait(context.Background()); err != nil {
			t.Fatalf("Wait() вернул ошибку: %v", err)
		}
	}

	// Ограничитель разводит отправки во времени, а не пропускает залпом.
	// Порог с запасом от точного числа интервалов: тикер вправе сработать
	// на доли миллисекунды раньше, и тест, стоящий ровно на границе,
	// начал бы мигать.
	want := interval * (waits - 2)
	if elapsed := time.Since(start); elapsed < want {
		t.Errorf("%d ожиданий заняли %v, ожидалось не меньше %v", waits, elapsed, want)
	}
}

func TestTickerLimiterStopsOnCancel(t *testing.T) {
	t.Parallel()

	limiter := delivery.NewTickerLimiter(1)
	defer limiter.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := limiter.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Wait() = %v, ожидалась отмена", err)
	}
}
