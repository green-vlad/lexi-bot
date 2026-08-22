// Команда bot — точка входа приложения.
//
// Здесь и только здесь собирается граф зависимостей: конфигурация, логгер,
// подключение к базе, репозитории, сценарии и слой Telegram. Никакой бизнес-логики
// в этом пакете нет и быть не должно.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
	// База таймзон IANA встраивается в бинарник: образ собирается на distroless,
	// где системного tzdata нет, а сутки пользователя считаются по его зоне.
	_ "time/tzdata"

	"lexi-bot/internal/adapter/telegram"
	"lexi-bot/internal/infra/config"
	"lexi-bot/internal/infra/logger"
	"lexi-bot/internal/infra/postgres"
	"lexi-bot/internal/usecase/port"
)

const migrationTimeout = 30 * time.Second

func main() {
	if err := run(); err != nil {
		// Логгер к этому моменту может быть ещё не создан, поэтому пишем напрямую.
		fmt.Fprintln(os.Stderr, "запуск не удался:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return err
	}

	format := logger.FormatJSON
	if cfg.Env == config.EnvDev {
		format = logger.FormatText
	}
	log := logger.New(os.Stdout, cfg.LogLevel, format)
	log.Info("конфигурация загружена", slog.Any("config", cfg))

	// Контекст живёт до первого SIGINT или SIGTERM: по сигналу приложение
	// перестаёт брать новую работу и корректно завершает текущую.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := migrate(ctx, cfg.DatabaseURL, log); err != nil {
		return err
	}

	pool, err := postgres.NewPool(ctx, postgres.DefaultPoolConfig(cfg.DatabaseURL))
	if err != nil {
		return err
	}
	// Пул закрывается последним, уже после того, как транспорт доработал:
	// начатая обработка апдейта имеет право дописать свою транзакцию.
	defer pool.Close()

	transport, err := telegram.New(telegram.Config{
		Token:       cfg.BotToken,
		PollTimeout: cfg.PollTimeout,
		Logger:      log,
	})
	if err != nil {
		return err
	}

	// TODO(T-023): вместо временного обработчика здесь появится роутер
	// с middleware, а за ним — сценарии.
	log.Info("бот запущен")
	if err := transport.Run(ctx, ping(transport, log)); err != nil {
		return err
	}

	log.Info("получен сигнал завершения, останавливаемся")
	return nil
}

// ping — временный обработчик до появления роутера (T-023). Он отвечает
// на /ping и молчит на всё остальное: этого достаточно, чтобы убедиться,
// что транспорт живой, и честнее, чем притворяться работающим ботом.
func ping(messenger port.Messenger, log *slog.Logger) port.UpdateHandler {
	return port.UpdateHandlerFunc(func(ctx context.Context, u *port.Update) error {
		if u.Command != "ping" {
			return nil
		}

		if _, err := messenger.SendMessage(ctx, port.OutgoingMessage{ChatID: u.Chat, Text: "pong"}); err != nil {
			return err
		}
		log.Info("ответили pong", slog.Int64("chat_id", int64(u.Chat)))
		return nil
	})
}

// migrate приводит схему базы к актуальной версии. Приложение делает это само
// при старте, поэтому деплой — это просто замена образа, без отдельного шага.
func migrate(ctx context.Context, dsn string, log *slog.Logger) error {
	ctx, cancel := context.WithTimeout(ctx, migrationTimeout)
	defer cancel()

	migrator, err := postgres.NewMigrator(dsn)
	if err != nil {
		return err
	}
	defer func() {
		if err := migrator.Close(); err != nil {
			log.Warn("не удалось закрыть подключение для миграций", slog.Any("error", err))
		}
	}()

	if err := migrator.Ping(ctx); err != nil {
		return err
	}

	applied, err := migrator.Up(ctx)
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		log.Info("схема базы актуальна")
		return nil
	}
	for _, m := range applied {
		log.Info("миграция применена",
			slog.Int64("version", m.Version),
			slog.String("name", m.Name),
			slog.Duration("duration", m.Duration),
		)
	}
	return nil
}
