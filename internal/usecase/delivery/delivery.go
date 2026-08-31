// Package delivery рассылает то, что накопилось в очереди отправки.
//
// Отделён от планировщика намеренно: тот только записывает, кому и когда
// написать, и его работу можно повторять сколько угодно раз. Отправка
// необратима, и у неё свои заботы — скорость, заблокировавшие бота,
// отметка о сделанном.
package delivery

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// Значения по умолчанию.
const (
	// DefaultRate — сколько сообщений в секунду шлём суммарно.
	// Telegram разрешает около тридцати; двадцать пять оставляют запас
	// на ответы живым людям, которые идут по тому же каналу.
	DefaultRate = 25
	// DefaultWorkers — сколько отправок идёт одновременно. Больше
	// ограничителя незачем: они всё равно упрутся в него по очереди.
	DefaultWorkers = 4
	// DefaultBatch — сколько сообщений берём за один заход.
	DefaultBatch = 200
)

// Deps — зависимости рассылки.
type Deps struct {
	Outbox    port.OutboxRepo
	Users     port.UserRepo
	Messenger port.Messenger
	Catalog   port.Catalog
	Clock     port.Clock
	// Limiter держит общую скорость отправки. Общий на всех воркеров:
	// ограничение у Telegram одно на бота, а не на горутину.
	Limiter Limiter
	// Workers и Batch — ноль означает значение по умолчанию.
	Workers int
	Batch   int
}

// Service — сценарий рассылки.
type Service struct {
	deps    Deps
	workers int
	batch   int
}

// New создаёт сценарий. Зависимости передаются указателем, как и в остальных
// конструкторах: структура крупная, а копировать её незачем.
func New(deps *Deps) (*Service, error) {
	if deps == nil {
		return nil, errors.New("рассылке нужны зависимости")
	}

	switch {
	case deps.Outbox == nil:
		return nil, errors.New("рассылке нужен OutboxRepo")
	case deps.Users == nil:
		return nil, errors.New("рассылке нужен UserRepo")
	case deps.Messenger == nil:
		return nil, errors.New("рассылке нужен мессенджер")
	case deps.Catalog == nil:
		return nil, errors.New("рассылке нужен каталог переводов")
	case deps.Clock == nil:
		return nil, errors.New("рассылке нужны часы")
	case deps.Limiter == nil:
		return nil, errors.New("рассылке нужен ограничитель скорости")
	}

	s := &Service{deps: *deps, workers: deps.Workers, batch: deps.Batch}
	if s.workers <= 0 {
		s.workers = DefaultWorkers
	}
	if s.batch <= 0 {
		s.batch = DefaultBatch
	}
	return s, nil
}

// Report — что сделал один заход рассылки.
type Report struct {
	// Sent — сколько сообщений ушло.
	Sent int
	// Blocked — скольким написать не удалось, потому что они заблокировали
	// бота. Эти записи деактивированы и больше не побеспокоят очередь.
	Blocked int
	// Failed — сколько отправок не удалось по другой причине. Они остались
	// в очереди и уйдут со следующим заходом.
	Failed int
}

// outcome — что вышло с одним сообщением.
type outcome struct {
	id      port.NotificationID
	blocked bool
	err     error
}

// Deliver отправляет всё, чему подошёл срок.
func (s *Service) Deliver(ctx context.Context) (Report, error) {
	now := s.deps.Clock.Now()

	pending, err := s.deps.Outbox.Pending(ctx, now, s.batch)
	if err != nil {
		return Report{}, fmt.Errorf("получить очередь отправки: %w", err)
	}
	if len(pending) == 0 {
		return Report{}, nil
	}

	outcomes := s.send(ctx, pending)

	report := Report{}
	done := make([]port.NotificationID, 0, len(outcomes))
	for _, result := range outcomes {
		switch {
		case result.blocked:
			report.Blocked++
			// Строку всё равно закрываем: повторять отправку тому,
			// кто заблокировал бота, бессмысленно, а незакрытая запись
			// будет всплывать в каждом заходе до конца времён.
			done = append(done, result.id)
		case result.err != nil:
			report.Failed++
		default:
			report.Sent++
			done = append(done, result.id)
		}
	}

	if err := s.deps.Outbox.MarkSent(ctx, done, now); err != nil {
		return Report{}, fmt.Errorf("отметить отправленные: %w", err)
	}
	return report, nil
}

// send рассылает пачку в несколько потоков.
func (s *Service) send(ctx context.Context, pending []port.Notification) []outcome {
	queue := make(chan port.Notification)
	results := make(chan outcome, len(pending))

	var wg sync.WaitGroup
	for i := 0; i < s.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range queue {
				results <- s.one(ctx, &n)
			}
		}()
	}

	for i := range pending {
		select {
		case <-ctx.Done():
			// Рассылку остановили: недоотправленное останется в очереди
			// и уйдёт после перезапуска.
			close(queue)
			wg.Wait()
			close(results)
			return collect(results)
		case queue <- pending[i]:
		}
	}
	close(queue)
	wg.Wait()
	close(results)
	return collect(results)
}

func collect(results <-chan outcome) []outcome {
	out := make([]outcome, 0, len(results))
	for result := range results {
		out = append(out, result)
	}
	return out
}

// one отправляет одно сообщение.
func (s *Service) one(ctx context.Context, n *port.Notification) outcome {
	if err := s.deps.Limiter.Wait(ctx); err != nil {
		return outcome{id: n.ID, err: err}
	}

	recipient, err := s.deps.Users.ByID(ctx, n.UserID)
	if err != nil {
		return outcome{id: n.ID, err: fmt.Errorf("найти получателя: %w", err)}
	}
	if !recipient.IsActive() {
		// Человек удалился между постановкой в очередь и отправкой:
		// писать ему нечего, но и строку держать незачем.
		return outcome{id: n.ID, blocked: true}
	}

	text, err := s.text(n.Kind, recipient.UILang)
	if err != nil {
		return outcome{id: n.ID, err: err}
	}

	_, err = s.deps.Messenger.SendMessage(ctx, port.OutgoingMessage{
		ChatID: port.ChatID(recipient.TelegramID),
		Text:   text,
	})
	switch {
	case errors.Is(err, port.ErrBlocked):
		if deactivateErr := s.deactivate(ctx, recipient.ID); deactivateErr != nil {
			return outcome{id: n.ID, err: deactivateErr}
		}
		return outcome{id: n.ID, blocked: true}
	case err != nil:
		return outcome{id: n.ID, err: err}
	}
	return outcome{id: n.ID}
}

// text подбирает сообщение под вид уведомления и язык человека.
func (s *Service) text(kind string, lang user.UILang) (string, error) {
	key := "notify." + kind
	text, err := s.deps.Catalog.For(lang).T(key, nil)
	if err != nil {
		return "", fmt.Errorf("собрать текст уведомления %q: %w", kind, err)
	}
	return text, nil
}

// deactivate помечает удалённым того, кто заблокировал бота.
//
// Мягко: журнал повторений и прогресс остаются. Человек, вернувшийся через
// полгода, продолжит с того места, где бросил, а не начнёт заново.
func (s *Service) deactivate(ctx context.Context, id user.ID) error {
	if err := s.deps.Users.SoftDelete(ctx, id, s.deps.Clock.Now()); err != nil {
		return fmt.Errorf("деактивировать пользователя %d: %w", id, err)
	}
	return nil
}

// Interval — как часто имеет смысл заходить в очередь.
//
// Полминуты: напоминания ставятся раз в пять минут, и человеку всё равно,
// придёт ли оно секунда в секунду. Чаще — значит будить базу впустую.
const Interval = 30 * time.Second
