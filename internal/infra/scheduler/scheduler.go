// Package scheduler запускает периодические задачи внутри процесса бота.
//
// Тикер, а не библиотека выражений cron: расписание здесь одно и простое —
// «раз в пять минут», — а выбор момента внутри суток всё равно делает
// сценарий, который знает про таймзоны пользователей. Библиотека дала бы
// синтаксис, которым некому пользоваться, и ещё одно дерево зависимостей.
package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// Job — задача, выполняемая по тику.
type Job struct {
	// Name попадает в лог: без него непонятно, какая из задач упала.
	Name string
	// Every — период между запусками.
	Every time.Duration
	// Run выполняет задачу. Ошибка не останавливает планировщик:
	// упавший тик — повод записать в лог, а не бросить расписание.
	Run func(ctx context.Context) error
}

// Scheduler гоняет задачи по расписанию.
type Scheduler struct {
	jobs []Job
	log  *slog.Logger
	wg   sync.WaitGroup
}

// New создаёт планировщик.
func New(log *slog.Logger, jobs ...Job) (*Scheduler, error) {
	if log == nil {
		return nil, errors.New("планировщику нужен логгер")
	}
	for _, job := range jobs {
		switch {
		case job.Name == "":
			return nil, errors.New("у задачи планировщика нет имени")
		case job.Every <= 0:
			return nil, errors.New("у задачи " + job.Name + " не задан период")
		case job.Run == nil:
			return nil, errors.New("у задачи " + job.Name + " нечего выполнять")
		}
	}
	return &Scheduler{jobs: jobs, log: log}, nil
}

// Start запускает задачи и возвращается сразу.
//
// Первый запуск — по первому тику, а не немедленно: бот только что поднялся,
// и подгружать старт ещё и рассылкой незачем.
func (s *Scheduler) Start(ctx context.Context) {
	for i := range s.jobs {
		job := s.jobs[i]

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.loop(ctx, &job)
		}()
	}
}

// Stop дожидается, пока начатые задачи доработают.
//
// Отменяет их не он, а контекст, переданный в Start: задача, увидевшая
// отмену, должна прекратить работу сама — так же, как это делают
// обработчики апдейтов.
func (s *Scheduler) Stop() { s.wg.Wait() }

// loop гоняет одну задачу до отмены контекста.
func (s *Scheduler) loop(ctx context.Context, job *Job) {
	ticker := time.NewTicker(job.Every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.log.Info("задача планировщика остановлена", slog.String("job", job.Name))
			return
		case <-ticker.C:
			s.run(ctx, job)
		}
	}
}

// run выполняет один тик, не давая панике уронить процесс.
//
// Задача ходит в базу и в сеть, и падение в ней не должно уносить с собой
// бота, который в это время отвечает людям.
func (s *Scheduler) run(ctx context.Context, job *Job) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("паника в задаче планировщика",
				slog.String("job", job.Name), slog.Any("panic", r))
		}
	}()

	if err := job.Run(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		s.log.Error("задача планировщика не удалась",
			slog.String("job", job.Name), slog.Any("error", err))
	}
}
