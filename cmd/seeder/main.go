// Команда seeder загружает встроенные словари в базу.
//
// Запускается на каждом выкате и потому обязана быть идемпотентной: второй
// прогон подряд должен ничего не изменить и честно об этом сказать. Отчёт
// печатается всегда — по нему видно, поехал ли словарь, ещё до того, как
// это заметят пользователи.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"lexi-bot/internal/adapter/seeds"
	storage "lexi-bot/internal/adapter/storage/postgres"
	"lexi-bot/internal/infra/config"
	"lexi-bot/internal/infra/logger"
	"lexi-bot/internal/infra/postgres"
	"lexi-bot/internal/usecase/seeding"
	seedfiles "lexi-bot/seeds"
)

const loadTimeout = 5 * time.Minute

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "загрузка словарей не удалась:", err)
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ctx, cancel := context.WithTimeout(ctx, loadTimeout)
	defer cancel()

	// Разбор идёт до подключения к базе: битый словарь должен обнаружиться
	// раньше, чем мы что-то в неё запишем.
	decks, err := seeds.Load(seedfiles.FS)
	if err != nil {
		return err
	}

	pool, err := postgres.NewPool(ctx, postgres.DefaultPoolConfig(cfg.DatabaseURL))
	if err != nil {
		return err
	}
	defer pool.Close()

	service, err := seeding.New(seeding.Deps{
		Decks:   storage.NewDeckRepo(pool),
		Lexemes: storage.NewLexemeRepo(pool),
		Tx:      storage.NewTxManager(pool),
	})
	if err != nil {
		return err
	}

	report, err := service.Load(ctx, decks)
	if err != nil {
		return err
	}

	for _, deck := range report.Decks {
		log.Info("колода загружена",
			slog.String("code", deck.Code),
			slog.Int("added", deck.Added),
			slog.Int("changed", deck.Changed),
			slog.Int("unchanged", deck.Unchanged))
	}
	log.Info("словари загружены",
		slog.Int("decks", len(report.Decks)),
		slog.Int("added", report.Added()),
		slog.Int("changed", report.Changed()),
		slog.Int("unchanged", report.Unchanged()))
	return nil
}
