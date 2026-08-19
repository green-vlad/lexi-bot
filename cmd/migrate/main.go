// Команда migrate управляет схемой базы вручную.
//
// Приложение накатывает миграции само при старте, поэтому в обычной жизни эта
// команда не нужна. Она существует для разработки и для разбора аварий: посмотреть
// статус, откатить последнюю миграцию, проверить доступность базы.
//
//	go run ./cmd/migrate up
//	go run ./cmd/migrate status
//	go run ./cmd/migrate down
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"lexi-bot/internal/infra/config"
	"lexi-bot/internal/infra/postgres"
)

const usage = `Управление схемой базы данных.

Использование:
  migrate <команда>

Команды:
  up       накатить все непринятые миграции
  down     откатить последнюю применённую миграцию
  status   показать состояние всех миграций
  version  показать текущую версию схемы

Строка подключения берётся из переменной окружения DATABASE_URL.`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, usage)
		return errors.New("ожидалась ровно одна команда")
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("не задана переменная окружения DATABASE_URL")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	migrator, err := postgres.NewMigrator(dsn)
	if err != nil {
		return err
	}
	defer func() { _ = migrator.Close() }()

	if err := migrator.Ping(ctx); err != nil {
		return fmt.Errorf("%w (проверьте DATABASE_URL=%s и что база поднята)", err, config.RedactDSN(dsn))
	}

	switch command := os.Args[1]; command {
	case "up":
		return up(ctx, migrator)
	case "down":
		return down(ctx, migrator)
	case "status":
		return status(ctx, migrator)
	case "version":
		return version(ctx, migrator)
	default:
		fmt.Fprintln(os.Stderr, usage)
		return fmt.Errorf("неизвестная команда %q", command)
	}
}

func up(ctx context.Context, m *postgres.Migrator) error {
	applied, err := m.Up(ctx)
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		fmt.Println("схема уже актуальна, применять нечего")
		return nil
	}
	for _, a := range applied {
		fmt.Printf("применена %d %s (%s)\n", a.Version, a.Name, a.Duration.Round(time.Millisecond))
	}
	return nil
}

func down(ctx context.Context, m *postgres.Migrator) error {
	applied, err := m.Down(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("откачена %d %s (%s)\n", applied.Version, applied.Name, applied.Duration.Round(time.Millisecond))
	return nil
}

func status(ctx context.Context, m *postgres.Migrator) error {
	statuses, err := m.Status(ctx)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	// Запись в буфер tabwriter не может завершиться ошибкой, ошибки вывода
	// всплывут при Flush — его результат мы возвращаем.
	_, _ = fmt.Fprintln(w, "ВЕРСИЯ\tМИГРАЦИЯ\tСОСТОЯНИЕ")
	for _, s := range statuses {
		state := "не применена"
		if s.Applied {
			state = s.AppliedAt.Local().Format(time.DateTime)
		}
		_, _ = fmt.Fprintf(w, "%d\t%s\t%s\n", s.Version, s.Name, state)
	}
	return w.Flush()
}

func version(ctx context.Context, m *postgres.Migrator) error {
	v, err := m.Version(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("текущая версия схемы: %d\n", v)
	return nil
}
