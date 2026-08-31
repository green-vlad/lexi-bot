package scheduler_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"lexi-bot/internal/infra/scheduler"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

func TestSchedulerRunsUntilStopped(t *testing.T) {
	t.Parallel()

	var runs atomic.Int64
	done := make(chan struct{})

	s, err := scheduler.New(quietLogger(), scheduler.Job{
		Name: "тик", Every: time.Millisecond,
		Run: func(context.Context) error {
			if runs.Add(1) == 3 {
				close(done)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New() вернул ошибку: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("задача выполнилась %d раз, ожидалось три", runs.Load())
	}

	cancel()
	s.Stop()

	// После остановки задача больше не запускается.
	settled := runs.Load()
	time.Sleep(20 * time.Millisecond)
	if runs.Load() != settled {
		t.Errorf("после остановки задача выполнилась ещё %d раз", runs.Load()-settled)
	}
}

func TestSchedulerSurvivesFailureAndPanic(t *testing.T) {
	t.Parallel()

	var runs atomic.Int64
	done := make(chan struct{})

	// Упавший тик — повод записать в лог, а не бросить расписание:
	// иначе одна ошибка в базе выключила бы напоминания навсегда.
	s, err := scheduler.New(quietLogger(), scheduler.Job{
		Name: "падучая", Every: time.Millisecond,
		Run: func(context.Context) error {
			switch runs.Add(1) {
			case 1:
				return errors.New("база недоступна")
			case 2:
				panic("что-то пошло не так")
			case 3:
				close(done)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New() вернул ошибку: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("задача выполнилась %d раз: ошибка или паника остановили расписание", runs.Load())
	}
	cancel()
	s.Stop()
}

func TestSchedulerRunsEveryJob(t *testing.T) {
	t.Parallel()

	var first, second atomic.Int64
	s, err := scheduler.New(quietLogger(),
		scheduler.Job{Name: "первая", Every: time.Millisecond, Run: func(context.Context) error {
			first.Add(1)
			return nil
		}},
		scheduler.Job{Name: "вторая", Every: time.Millisecond, Run: func(context.Context) error {
			second.Add(1)
			return nil
		}},
	)
	if err != nil {
		t.Fatalf("New() вернул ошибку: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	cancel()
	s.Stop()

	if first.Load() == 0 || second.Load() == 0 {
		t.Errorf("выполнений: первая %d, вторая %d — обе должны были запуститься",
			first.Load(), second.Load())
	}
}

func TestSchedulerChecksJobs(t *testing.T) {
	t.Parallel()

	run := func(context.Context) error { return nil }

	for _, tt := range []struct {
		name string
		job  scheduler.Job
	}{
		{"без имени", scheduler.Job{Every: time.Second, Run: run}},
		{"без периода", scheduler.Job{Name: "тик", Run: run}},
		{"без тела", scheduler.Job{Name: "тик", Every: time.Second}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := scheduler.New(quietLogger(), tt.job); err == nil {
				t.Error("ожидалась ошибка")
			}
		})
	}

	if _, err := scheduler.New(nil); err == nil {
		t.Error("планировщик без логгера должен быть ошибкой")
	}
}
