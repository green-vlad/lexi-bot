package telegram_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"lexi-bot/internal/adapter/telegram"
	"lexi-bot/internal/usecase/port"
)

const adminChat = int64(999)

type alertFixture struct {
	alerter   *telegram.Alerter
	messenger *fakeMessenger
	now       time.Time
}

func newAlertFixture(t *testing.T, chat int64) *alertFixture {
	t.Helper()

	f := &alertFixture{
		messenger: &fakeMessenger{},
		now:       time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
	}
	f.alerter = telegram.NewAlerter(f.messenger, chat, func() time.Time { return f.now }, quietLogger())
	return f
}

func TestAlertGoesToAdminChat(t *testing.T) {
	t.Parallel()

	f := newAlertFixture(t, adminChat)

	f.alerter.Alert(context.Background(), "panic", "🔥 всё сломалось")

	sent := f.messenger.last(t)
	if sent.ChatID != port.ChatID(adminChat) {
		t.Errorf("чат = %d, ожидался админский", sent.ChatID)
	}
	if !strings.Contains(sent.Text, "всё сломалось") {
		t.Errorf("текст = %q", sent.Text)
	}
}

func TestAlertsAreDisabledWithoutAdminChat(t *testing.T) {
	t.Parallel()

	f := newAlertFixture(t, 0)

	// В разработке админского чата обычно нет, и падать из-за этого незачем.
	if f.alerter.Enabled() {
		t.Error("без чата тревоги должны быть выключены")
	}
	f.alerter.Alert(context.Background(), "panic", "молчи")
	if f.messenger.count() != 0 {
		t.Error("тревога ушла в никуда")
	}
}

func TestAlertHoldsBackRepeats(t *testing.T) {
	t.Parallel()

	f := newAlertFixture(t, adminChat)

	f.alerter.Alert(context.Background(), "telegram_api", "первая")
	f.alerter.Alert(context.Background(), "telegram_api", "вторая")
	// Лежащий Telegram превратил бы админский чат в ленту одинаковых
	// сообщений, и настоящую тревогу в ней было бы не найти.
	if f.messenger.count() != 1 {
		t.Errorf("отправлено %d тревог, ожидалась одна", f.messenger.count())
	}

	// Другая беда проходит сразу: придерживаются повторы одной, а не всё.
	f.alerter.Alert(context.Background(), "panic", "паника")
	if f.messenger.count() != 2 {
		t.Errorf("отправлено %d, ожидалось две", f.messenger.count())
	}

	// А через паузу повтор снова проходит.
	f.now = f.now.Add(telegram.AlertCooldown + time.Minute)
	f.alerter.Alert(context.Background(), "telegram_api", "третья")
	if f.messenger.count() != 3 {
		t.Errorf("отправлено %d, ожидалось три", f.messenger.count())
	}
}

func TestAPIErrorsAlertOnlyAsBurst(t *testing.T) {
	t.Parallel()

	f := newAlertFixture(t, adminChat)
	boom := errors.New("Telegram ответил 502 Bad Gateway")

	// Одиночная ошибка — обычное дело: сеть моргнула.
	for i := 0; i < telegram.APIErrorBurst-1; i++ {
		f.alerter.APIFailed(boom)
	}
	if f.messenger.count() != 0 {
		t.Fatalf("тревога поднята на %d ошибках", telegram.APIErrorBurst-1)
	}

	f.alerter.APIFailed(boom)
	if f.messenger.count() != 1 {
		t.Fatalf("на серии тревога не поднялась")
	}
	if got := f.messenger.last(t).Text; !strings.Contains(got, "502") {
		t.Errorf("текст = %q, ожидалась последняя ошибка", got)
	}
}

func TestAPIErrorsOutsideWindowDoNotAccumulate(t *testing.T) {
	t.Parallel()

	f := newAlertFixture(t, adminChat)
	boom := errors.New("сеть моргнула")

	// Редкие ошибки, разнесённые во времени, серией не считаются:
	// иначе бот, проработавший месяц, однажды поднял бы тревогу на ровном
	// месте — просто потому, что ошибок накопилось.
	for i := 0; i < telegram.APIErrorBurst*2; i++ {
		f.alerter.APIFailed(boom)
		f.now = f.now.Add(telegram.APIErrorWindow + time.Minute)
	}
	if f.messenger.count() != 0 {
		t.Errorf("поднято %d тревог на разрозненных ошибках", f.messenger.count())
	}
}

func TestAlertSurvivesUndeliveredMessage(t *testing.T) {
	t.Parallel()

	f := newAlertFixture(t, adminChat)
	f.messenger.err = errors.New("админский чат недоступен")

	// Недоставленная тревога — плохо, но падать из-за неё нельзя:
	// бот в это время работает.
	f.alerter.Alert(context.Background(), "panic", "🔥")
}
