package health_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"lexi-bot/internal/infra/health"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

type fixture struct {
	server *health.Server
	addr   string
	now    time.Time
	dbErr  error
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	f := &fixture{
		// Свободный порт выбирает система: тесты идут параллельно,
		// и фиксированный номер однажды столкнулся бы сам с собой.
		addr: "127.0.0.1:0",
		now:  time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
	}

	server, err := health.New(health.Config{
		Addr:  f.addr,
		Ping:  func(context.Context) error { return f.dbErr },
		Clock: func() time.Time { return f.now },
		Metrics: func(w io.Writer) error {
			_, err := fmt.Fprintln(w, "lexi_updates_total 42")
			return err
		},
		Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("health.New() вернул ошибку: %v", err)
	}
	f.server = server
	return f
}

// serve поднимает сервер на свободном порту и возвращает его адрес.
func (f *fixture) serve(t *testing.T) string {
	t.Helper()

	// Порт нужен известный: слушатель прячется внутри, поэтому просим
	// конкретный свободный, найденный заранее.
	addr := freePort(t)
	server, err := health.New(health.Config{
		Addr:  addr,
		Ping:  func(context.Context) error { return f.dbErr },
		Clock: func() time.Time { return f.now },
		Metrics: func(w io.Writer) error {
			_, err := fmt.Fprintln(w, "lexi_updates_total 42")
			return err
		},
		Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("health.New() вернул ошибку: %v", err)
	}
	f.server = server

	server.Start(t.Context())
	t.Cleanup(server.Stop)
	waitReady(t, "http://"+addr+"/healthz")
	return "http://" + addr
}

func (f *fixture) get(t *testing.T, url string) (status int, body string) {
	t.Helper()

	resp, err := http.Get(url) //nolint:noctx // тестовый запрос к локальному серверу
	if err != nil {
		t.Fatalf("запрос %s не прошёл: %v", url, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("чтение ответа не прошло: %v", err)
	}
	return resp.StatusCode, string(raw)
}

func TestHealthzIsGreenWhenEverythingWorks(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	base := f.serve(t)

	status, body := f.get(t, base+"/healthz")
	if status != http.StatusOK {
		t.Errorf("код = %d, ожидался 200: %s", status, body)
	}
	if !strings.Contains(body, "ok") {
		t.Errorf("тело = %q", body)
	}
}

func TestHealthzTurnsRedWithoutDatabase(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	base := f.serve(t)
	f.dbErr = errors.New("пул закрыт")

	status, body := f.get(t, base+"/healthz")
	// 503, а не 500: бот не сломан, он временно не может работать.
	if status != http.StatusServiceUnavailable {
		t.Errorf("код = %d, ожидался 503", status)
	}
	if !strings.Contains(body, "база недоступна") {
		t.Errorf("тело = %q", body)
	}
}

func TestHealthzNoticesStalePolling(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	base := f.serve(t)

	// Пока цикла не было, бот здоров: он только что поднялся и ждёт
	// первого апдейта.
	if status, _ := f.get(t, base+"/healthz"); status != http.StatusOK {
		t.Errorf("код до первого цикла = %d, ожидался 200", status)
	}

	f.server.PollSucceeded()
	if status, _ := f.get(t, base+"/healthz"); status != http.StatusOK {
		t.Errorf("код после цикла = %d, ожидался 200", status)
	}

	// А вот молчание дольше порога — уже болезнь: процесс жив, но апдейтов
	// не получает.
	f.now = f.now.Add(health.StaleAfter + time.Minute)
	status, body := f.get(t, base+"/healthz")
	if status != http.StatusServiceUnavailable {
		t.Errorf("код = %d, ожидался 503", status)
	}
	if !strings.Contains(body, "опрос Telegram молчит") {
		t.Errorf("тело = %q", body)
	}
}

func TestHealthzReportsEveryProblem(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	base := f.serve(t)
	f.dbErr = errors.New("пул закрыт")
	f.server.PollSucceeded()
	f.now = f.now.Add(health.StaleAfter + time.Minute)

	// Человеку у пульта полезнее увидеть обе жалобы разом, чем узнавать
	// о второй после починки первой.
	_, body := f.get(t, base+"/healthz")
	if !strings.Contains(body, "база недоступна") || !strings.Contains(body, "опрос Telegram молчит") {
		t.Errorf("тело = %q, ожидались обе жалобы", body)
	}
}

func TestMetricsAreServed(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	base := f.serve(t)

	status, body := f.get(t, base+"/metrics")
	if status != http.StatusOK {
		t.Errorf("код = %d", status)
	}
	if !strings.Contains(body, "lexi_updates_total 42") {
		t.Errorf("тело = %q", body)
	}
}

func TestServerChecksConfig(t *testing.T) {
	t.Parallel()

	ping := func(context.Context) error { return nil }
	clock := time.Now

	for _, tt := range []struct {
		name string
		cfg  health.Config
	}{
		{"без адреса", health.Config{Ping: ping, Clock: clock, Logger: quietLogger()}},
		{"без проверки базы", health.Config{Addr: "127.0.0.1:0", Clock: clock, Logger: quietLogger()}},
		{"без часов", health.Config{Addr: "127.0.0.1:0", Ping: ping, Logger: quietLogger()}},
		{"без логгера", health.Config{Addr: "127.0.0.1:0", Ping: ping, Clock: clock}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := health.New(tt.cfg); err == nil {
				t.Error("ожидалась ошибка")
			}
		})
	}
}

func TestBusyPortDoesNotStopTheBot(t *testing.T) {
	t.Parallel()

	addr := freePort(t)
	first, err := health.New(health.Config{
		Addr: addr, Ping: func(context.Context) error { return nil },
		Clock: time.Now, Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("health.New() вернул ошибку: %v", err)
	}
	first.Start(t.Context())
	t.Cleanup(first.Stop)
	waitReady(t, "http://"+addr+"/healthz")

	// Второй сервер на том же порту не поднимется — и это не должно
	// ронять процесс: занятый порт метрик не повод оставить людей
	// без занятий.
	second, err := health.New(health.Config{
		Addr: addr, Ping: func(context.Context) error { return nil },
		Clock: time.Now, Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("health.New() вернул ошибку: %v", err)
	}
	second.Start(t.Context())
	second.Stop()
}
