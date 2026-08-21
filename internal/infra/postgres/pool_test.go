package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"lexi-bot/internal/infra/postgres"
)

func TestDefaultPoolConfig(t *testing.T) {
	t.Parallel()

	const dsn = "postgres://lexi:secret@localhost:5432/lexi?sslmode=disable"
	cfg := postgres.DefaultPoolConfig(dsn)

	if cfg.DSN != dsn {
		t.Errorf("DSN = %q, ожидалось %q", cfg.DSN, dsn)
	}
	if cfg.MaxConns < cfg.MinConns {
		t.Errorf("MaxConns = %d меньше MinConns = %d", cfg.MaxConns, cfg.MinConns)
	}
	if cfg.ConnectTimeout <= 0 || cfg.StatementTimeout <= 0 {
		t.Error("таймауты по умолчанию должны быть положительными")
	}
	if cfg.AppName == "" {
		t.Error("AppName должен быть задан: по нему видно наши запросы в pg_stat_activity")
	}
}

func TestNewPoolRejectsBrokenDSN(t *testing.T) {
	t.Parallel()

	cfg := postgres.DefaultPoolConfig("это не строка подключения")

	pool, err := postgres.NewPool(context.Background(), cfg)
	if err == nil {
		pool.Close()
		t.Fatal("NewPool() не заметил некорректную строку подключения")
	}
	if !strings.Contains(err.Error(), "строку подключения") {
		t.Errorf("ошибка %v не объясняет, что сломано", err)
	}
}

func TestNewPoolFailsWhenDatabaseUnreachable(t *testing.T) {
	t.Parallel()

	// Проверка при создании — не формальность: без неё приложение считалось бы
	// запущенным и падало бы только на первом запросе пользователя.
	cfg := postgres.DefaultPoolConfig("postgres://lexi:lexi@127.0.0.1:1/lexi?sslmode=disable")
	cfg.ConnectTimeout = time.Second

	pool, err := postgres.NewPool(context.Background(), cfg)
	if err == nil {
		pool.Close()
		t.Fatal("NewPool() не заметил недоступную базу")
	}
	if !strings.Contains(err.Error(), "не отвечает") {
		t.Errorf("ошибка %v не сообщает, что база недоступна", err)
	}
}

func TestNewPoolRejectsNilConfig(t *testing.T) {
	t.Parallel()

	pool, err := postgres.NewPool(context.Background(), nil)
	if err == nil {
		pool.Close()
		t.Fatal("NewPool(nil) не вернул ошибку")
	}
}
