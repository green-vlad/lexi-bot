package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"lexi-bot/internal/usecase/port"
)

// Пороги тревоги по ошибкам Telegram API.
const (
	// APIErrorBurst — сколько ошибок подряд считается серией.
	//
	// Одиночная ошибка — обычное дело: сеть моргнула, Telegram ответил 502.
	// Тревожиться стоит, когда они идут одна за другой: это уже отобранный
	// токен, второй инстанс или лежащий Telegram.
	APIErrorBurst = 10
	// APIErrorWindow — за какое время должна набраться серия.
	APIErrorWindow = 5 * time.Minute
	// AlertCooldown — как часто повторять одну и ту же тревогу.
	//
	// Без этого лежащий Telegram превратил бы админский чат в ленту
	// из одинаковых сообщений, и настоящую тревогу в ней было бы не найти.
	AlertCooldown = 30 * time.Minute
)

// Alerter шлёт тревожные сообщения в админский чат.
//
// Отдельной инфраструктуры оповещений у нас нет и не нужно: бот уже умеет
// писать в Telegram, а админский чат — такой же чат, как остальные.
type Alerter struct {
	messenger port.Messenger
	chat      port.ChatID
	log       *slog.Logger
	clock     func() time.Time

	mu       sync.Mutex
	lastSent map[string]time.Time
	failures []time.Time
}

// NewAlerter создаёт отправщика тревог. Нулевой чат выключает их: в разработке
// админского чата обычно нет, и падать из-за этого незачем.
func NewAlerter(messenger port.Messenger, chat int64, clock func() time.Time, log *slog.Logger) *Alerter {
	if clock == nil {
		clock = time.Now
	}
	return &Alerter{
		messenger: messenger, chat: port.ChatID(chat),
		log: log, clock: clock, lastSent: map[string]time.Time{},
	}
}

// Enabled сообщает, есть ли куда слать тревоги.
func (a *Alerter) Enabled() bool { return a != nil && a.chat != 0 && a.messenger != nil }

// Alert отправляет тревогу с указанным ключом.
//
// Ключ — не текст: одна и та же беда может описываться разными словами
// («не удалось подключиться», «таймаут»), а придерживать надо именно
// повторы одной беды.
func (a *Alerter) Alert(ctx context.Context, key, text string) {
	if !a.Enabled() || !a.allow(key) {
		return
	}

	if _, err := a.messenger.SendMessage(ctx, port.OutgoingMessage{
		ChatID: a.chat, Text: text, DisablePreview: true,
	}); err != nil {
		// Тревога не дошла — это само по себе плохо, но падать из-за
		// недоставленного уведомления нельзя: бот в это время работает.
		a.log.Error("не удалось отправить тревогу",
			slog.String("key", key), slog.Any("error", err))
	}
}

// allow придерживает повторы одной и той же тревоги.
func (a *Alerter) allow(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := a.clock()
	if last, ok := a.lastSent[key]; ok && now.Sub(last) < AlertCooldown {
		return false
	}
	a.lastSent[key] = now
	return true
}

// APIFailed отмечает ошибку Telegram API и поднимает тревогу, если они
// пошли серией.
func (a *Alerter) APIFailed(err error) {
	if !a.Enabled() {
		return
	}

	if count, burst := a.record(); burst {
		a.Alert(context.Background(), "telegram_api",
			fmt.Sprintf("⚠️ Ошибки Telegram API идут подряд: %d за последние %s.\nПоследняя: %v",
				count, APIErrorWindow, err))
	}
}

// record добавляет ошибку в окно и говорит, набралась ли серия.
func (a *Alerter) record() (int, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := a.clock()
	// Окно скользящее: редкие ошибки, разнесённые во времени, серией
	// не считаются, иначе бот, проработавший месяц, однажды поднял бы
	// тревогу просто потому, что ошибок накопилось.
	fresh := make([]time.Time, 0, len(a.failures)+1)
	for _, at := range a.failures {
		if now.Sub(at) <= APIErrorWindow {
			fresh = append(fresh, at)
		}
	}
	fresh = append(fresh, now)
	a.failures = fresh
	return len(a.failures), len(a.failures) >= APIErrorBurst
}
