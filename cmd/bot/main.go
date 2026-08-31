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
	"math/rand/v2"
	"os"
	"os/signal"
	"syscall"
	"time"
	// База таймзон IANA встраивается в бинарник: образ собирается на distroless,
	// где системного tzdata нет, а сутки пользователя считаются по его зоне.
	_ "time/tzdata"

	"github.com/jackc/pgx/v5/pgxpool"

	"lexi-bot/internal/adapter/i18n"
	// Пакетов с именем postgres два: инфраструктурный (пул и миграции)
	// и адаптер репозиториев. Псевдоним разводит их по ролям.
	storage "lexi-bot/internal/adapter/storage/postgres"
	"lexi-bot/internal/adapter/telegram"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/infra/config"
	"lexi-bot/internal/infra/logger"
	"lexi-bot/internal/infra/postgres"
	"lexi-bot/internal/usecase/courses"
	"lexi-bot/internal/usecase/importing"
	"lexi-bot/internal/usecase/intro"
	"lexi-bot/internal/usecase/onboarding"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/internal/usecase/session"
	"lexi-bot/internal/usecase/settings"
	"lexi-bot/internal/usecase/stats"
	"lexi-bot/internal/usecase/vocab"
	"lexi-bot/locales"
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

	catalog, err := i18n.NewCatalog(locales.FS)
	if err != nil {
		return err
	}

	transport, err := telegram.New(telegram.Config{
		Token:       cfg.BotToken,
		PollTimeout: cfg.PollTimeout,
		Logger:      log,
	})
	if err != nil {
		return err
	}

	handler, err := router(transport, catalog, pool, &cfg, log)
	if err != nil {
		return err
	}

	log.Info("бот запущен")
	if err := transport.Run(ctx, handler); err != nil {
		return err
	}

	log.Info("получен сигнал завершения, останавливаемся")
	return nil
}

// router собирает маршруты и общий для них конвейер middleware.
//
// Порядок middleware существенен. Снаружи — восстановление после паники:
// оно обязано поймать в том числе панику в остальных middleware. Дальше
// логирование, чтобы в лог попал и апдейт, обработка которого сорвалась
// на определении пользователя. Затем определение пользователя, и только
// после него локализация — язык интерфейса известен из его настроек.
func router(transport *telegram.Transport, catalog port.Catalog, pool *pgxpool.Pool, cfg *config.Config, log *slog.Logger) (port.UpdateHandler, error) {
	// Репозитории заводятся по одному разу и раздаются сценариям: каждый
	// из них — тонкая обёртка над пулом, но два экземпляра одного и того же
	// в графе зависимостей только сбивают с толку.
	users := storage.NewUserRepo(pool)
	courseRepo := storage.NewCourseRepo(pool)
	decks := storage.NewDeckRepo(pool)
	cards := storage.NewCardRepo(pool)

	dialogs, err := telegram.NewDialogs(&telegram.DialogsConfig{
		Sessions:  storage.NewSessionRepo(pool),
		Messenger: transport,
		Logger:    log,
	})
	if err != nil {
		return nil, err
	}

	onboardingService, err := onboarding.New(onboarding.Deps{
		Users:           users,
		Settings:        storage.NewSettingsRepo(pool),
		Decks:           decks,
		Courses:         courseRepo,
		DefaultTimezone: user.NewTimezone(cfg.DefaultTimezone),
	})
	if err != nil {
		return nil, err
	}

	start, err := telegram.NewOnboarding(onboardingService, dialogs, transport, catalog)
	if err != nil {
		return nil, err
	}

	r := telegram.NewRouter()
	r.Use(
		telegram.Recover(transport, catalog, log),
		telegram.Logging(log),
		telegram.AnswerCallbacks(transport, log),
		telegram.Identify(users, log),
		telegram.Localize(catalog),
		dialogs.Middleware(),
	)

	language, err := telegram.NewLanguage(users, transport, catalog)
	if err != nil {
		return nil, err
	}

	// Джиттер интервалов — не криптография: он лишь разводит карточки,
	// введённые в один день, чтобы они не возвращались все разом.
	// Предсказуемость этого разброса ничем не грозит.
	jitter := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0)) //nolint:gosec // разброс интервалов, а не секреты

	scheduler, err := study.NewSM2(study.DefaultSM2Config(), jitter)
	if err != nil {
		return nil, err
	}

	clock := port.ClockFunc(time.Now)

	courseService, err := courses.New(courses.Deps{
		Users:   users,
		Courses: courseRepo,
		Decks:   decks,
		Cards:   cards,
	})
	if err != nil {
		return nil, err
	}
	counters := storage.NewCounterRepo(pool)
	settingsRepo := storage.NewSettingsRepo(pool)
	lexemes := storage.NewLexemeRepo(pool)
	reviews := storage.NewReviewRepo(pool)

	learning, err := session.New(&session.Deps{
		Cards:     cards,
		Counters:  counters,
		Courses:   courseRepo,
		Settings:  settingsRepo,
		Lexemes:   lexemes,
		Decks:     decks,
		Reviews:   reviews,
		Clock:     clock,
		Rand:      jitter,
		Scheduler: scheduler,
		Resolver:  study.DefaultRatingResolver(),
	})
	if err != nil {
		return nil, err
	}

	introduction, err := intro.New(&intro.Deps{
		Cards:     cards,
		Counters:  counters,
		Courses:   courseRepo,
		Settings:  settingsRepo,
		Lexemes:   lexemes,
		Clock:     clock,
		Scheduler: scheduler,
	})
	if err != nil {
		return nil, err
	}

	menu, err := telegram.NewMenu(learning, introduction, courseService, transport)
	if err != nil {
		return nil, err
	}
	learn, err := telegram.NewLearn(learning, transport, catalog, clock, dialogs)
	if err != nil {
		return nil, err
	}
	introHandler, err := telegram.NewIntro(introduction, transport, menu)
	if err != nil {
		return nil, err
	}
	decksHandler, err := telegram.NewDecks(courseService, transport)
	if err != nil {
		return nil, err
	}

	vocabService, err := vocab.New(vocab.Deps{
		Users:   users,
		Decks:   decks,
		Lexemes: lexemes,
		Courses: courseRepo,
	})
	if err != nil {
		return nil, err
	}
	vocabHandler, err := telegram.NewVocab(vocabService, dialogs, transport)
	if err != nil {
		return nil, err
	}

	importService, err := importing.New(importing.Deps{
		Vocab: vocabService,
		Jobs:  storage.NewImportRepo(pool),
	})
	if err != nil {
		return nil, err
	}
	importHandler, err := telegram.NewImporting(importService, transport)
	if err != nil {
		return nil, err
	}

	settingsService, err := settings.New(settings.Deps{Settings: settingsRepo})
	if err != nil {
		return nil, err
	}
	settingsHandler, err := telegram.NewSettings(settingsService, dialogs, transport)
	if err != nil {
		return nil, err
	}

	statsService, err := stats.New(&stats.Deps{
		Users:    users,
		Courses:  courseRepo,
		Decks:    decks,
		Cards:    cards,
		Reviews:  reviews,
		Settings: settingsRepo,
		Clock:    clock,
	})
	if err != nil {
		return nil, err
	}
	statsHandler, err := telegram.NewStats(statsService, transport)
	if err != nil {
		return nil, err
	}

	start.Register(r)
	language.Register(r)
	menu.Register(r)
	learn.Register(r)
	introHandler.Register(r)
	decksHandler.Register(r)
	vocabHandler.Register(r)
	importHandler.Register(r)
	settingsHandler.Register(r)
	statsHandler.Register(r)
	r.Command("ping", telegram.Ping(transport))
	r.Unknown(telegram.UnknownCommand(transport))
	return r, nil
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
