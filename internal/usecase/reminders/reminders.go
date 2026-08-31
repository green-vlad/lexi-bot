// Package reminders ставит напоминания о занятии в очередь отправки.
//
// Сам он ничего не отправляет: тик планировщика только записывает, кому
// и на какой момент напомнить, а рассылка идёт отдельно (T-047). Разделение
// не формальное — запись в очередь идёт одной транзакцией и повторяется
// сколько угодно раз без вреда, а отправка необратима, и смешивать их
// значило бы получать двойные сообщения при каждом обрыве.
package reminders

import (
	"context"
	"errors"
	"fmt"
	"time"

	"lexi-bot/internal/usecase/port"
)

// Window — насколько назад тик подбирает пропущенные напоминания.
//
// Больше периода тика намеренно: пропущенный тик — обычное дело при
// перезапуске или подвисшем запросе, и напоминание не должно теряться
// из-за того, что бот на минуту отвлёкся. Повторов это не создаёт:
// момент напоминания в сутках один, и очередь его не задваивает.
const Window = 15 * time.Minute

// Deps — зависимости планировщика напоминаний.
type Deps struct {
	Settings port.SettingsRepo
	Outbox   port.OutboxRepo
	Clock    port.Clock
}

// Service — сценарий напоминаний.
type Service struct {
	deps Deps
}

// New создаёт сценарий.
func New(deps Deps) (*Service, error) {
	switch {
	case deps.Settings == nil:
		return nil, errors.New("напоминаниям нужен SettingsRepo")
	case deps.Outbox == nil:
		return nil, errors.New("напоминаниям нужен OutboxRepo")
	case deps.Clock == nil:
		return nil, errors.New("напоминаниям нужны часы")
	}
	return &Service{deps: deps}, nil
}

// Report — что сделал один тик.
type Report struct {
	// Considered — сколько получателей рассмотрено.
	Considered int
	// Scheduled — сколько напоминаний добавилось в очередь. Ноль при
	// непустом Considered — обычное дело: у большинства время ещё не пришло
	// или напоминание уже стоит.
	Scheduled int
}

// Tick ставит в очередь напоминания, время которых наступило.
//
// Момент напоминания считается в сутках пользователя, а не прибавлением
// часов: в день перевода часов «21:30» остаётся в 21:30, и напоминание
// не должно уезжать на час.
func (s *Service) Tick(ctx context.Context) (Report, error) {
	recipients, err := s.deps.Settings.Reminding(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("получить получателей напоминаний: %w", err)
	}

	now := s.deps.Clock.Now()
	report := Report{Considered: len(recipients)}

	due := make([]port.Notification, 0, len(recipients))
	for _, recipient := range recipients {
		at, ok := recipient.At.On(now, recipient.Timezone)
		if !ok {
			continue
		}
		if !inWindow(at, now) {
			continue
		}
		due = append(due, port.Notification{
			UserID:       recipient.UserID,
			Kind:         port.NotificationReminder,
			ScheduledFor: at,
		})
	}

	scheduled, err := s.deps.Outbox.Schedule(ctx, due)
	if err != nil {
		return Report{}, fmt.Errorf("поставить напоминания в очередь: %w", err)
	}
	report.Scheduled = scheduled
	return report, nil
}

// inWindow сообщает, что момент уже наступил и ещё не устарел.
//
// Будущее отсекается, чтобы вечернее напоминание не ушло утром того же дня.
// Слишком старое — чтобы бот, простоявший сутки, не выплеснул человеку
// вчерашние напоминания разом.
func inWindow(at, now time.Time) bool {
	if at.After(now) {
		return false
	}
	return now.Sub(at) <= Window
}
