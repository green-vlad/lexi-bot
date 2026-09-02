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
	"lexi-bot/internal/infra/health"
	"lexi-bot/internal/infra/logger"
	"lexi-bot/internal/infra/metrics"
	"lexi-bot/internal/infra/postgres"
	"lexi-bot/internal/infra/scheduler"
	"lexi-bot/internal/usecase/account"
	"lexi-bot/internal/usecase/courses"
	"lexi-bot/internal/usecase/delivery"
	"lexi-bot/internal/usecase/importing"
	"lexi-bot/internal/usecase/intro"
	"lexi-bot/internal/usecase/onboarding"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/internal/usecase/reminders"
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

	transport, err := telegram.New(telegram.Config{
		Token:       cfg.BotToken,
		PollTimeout: cfg.PollTimeout,
		Logger:      log,
	})
	if err != nil {
		return err
	}

	registry := metrics.New()
	alerter := telegram.NewAlerter(transport, cfg.AdminChatID, time.Now, log)

	// Транспорт создаётся до миграции затем, чтобы о сорванной миграции
	// было кому сообщить: бот при ней не поднимется, и молча упавший
	// процесс объяснит себя только логом на сервере.
	if err := migrate(ctx, cfg.DatabaseURL, log); err != nil {
		alerter.Alert(ctx, "migration", "🔥 Миграция не прошла, бот не поднялся: "+err.Error())
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

	service, err := health.New(health.Config{
		Addr:    cfg.HTTPAddr,
		Ping:    pool.Ping,
		Metrics: registry.Expose,
		Clock:   time.Now,
		Logger:  log,
	})
	if err != nil {
		return err
	}
	poolMetrics(registry, pool)
	service.Start(ctx)
	defer service.Stop()

	transport.SetHooks(service.PollSucceeded, alerter.APIFailed)

	handler, err := router(transport, catalog, pool, &cfg, log, registry, alerter)
	if err != nil {
		return err
	}

	jobs, stopLimiter, err := background(pool, transport, catalog, log)
	if err != nil {
		return err
	}
	defer stopLimiter()
	jobs.Start(ctx)
	// Фоновые задачи останавливаются раньше пула, но позже транспорта:
	// начатый тик имеет право дописать свою транзакцию.
	defer jobs.Stop()

	log.Info("бот запущен")
	if err := transport.Run(ctx, handler); err != nil {
		return err
	}

	log.Info("получен сигнал завершения, останавливаемся")
	return nil
}

// reminderTick — как часто планировщик смотрит, кому пора напомнить.
//
// Пять минут: точнее незачем, напоминание на минуту позже обещанного никого
// не подводит, а реже — значит промахиваться мимо окна и терять напоминания.
const reminderTick = 5 * time.Minute

// background собирает задачи, которые идут сами по себе, без апдейтов.
//
// Их две, и разделены они не случайно: планировщик только записывает, кому
// написать, и его работу можно повторять сколько угодно раз; рассылка
// необратима, и у неё свои заботы — скорость и заблокировавшие бота.
func background(
	pool *pgxpool.Pool, messenger port.Messenger, catalog port.Catalog, log *slog.Logger,
) (*scheduler.Scheduler, func(), error) {
	clock := port.ClockFunc(time.Now)
	outbox := storage.NewOutboxRepo(pool)

	reminding, err := reminders.New(reminders.Deps{
		Settings: storage.NewSettingsRepo(pool),
		Outbox:   outbox,
		Clock:    clock,
	})
	if err != nil {
		return nil, nil, err
	}

	limiter := delivery.NewTickerLimiter(delivery.DefaultRate)
	sending, err := delivery.New(&delivery.Deps{
		Outbox:    outbox,
		Users:     storage.NewUserRepo(pool),
		Messenger: messenger,
		Catalog:   catalog,
		Clock:     clock,
		Limiter:   limiter,
	})
	if err != nil {
		limiter.Stop()
		return nil, nil, err
	}

	jobs, err := scheduler.New(log,
		scheduler.Job{
			Name:  "напоминания",
			Every: reminderTick,
			Run: func(ctx context.Context) error {
				report, err := reminding.Tick(ctx)
				if err != nil {
					return err
				}
				if report.Scheduled > 0 {
					log.Info("напоминания поставлены в очередь",
						slog.Int("scheduled", report.Scheduled),
						slog.Int("considered", report.Considered))
				}
				return nil
			},
		},
		scheduler.Job{
			Name:  "рассылка",
			Every: delivery.Interval,
			Run: func(ctx context.Context) error {
				report, err := sending.Deliver(ctx)
				if err != nil {
					return err
				}
				if report.Sent > 0 || report.Blocked > 0 || report.Failed > 0 {
					log.Info("рассылка отработала",
						slog.Int("sent", report.Sent),
						slog.Int("blocked", report.Blocked),
						slog.Int("failed", report.Failed))
				}
				return nil
			},
		},
	)
	if err != nil {
		limiter.Stop()
		return nil, nil, err
	}
	return jobs, limiter.Stop, nil
}

// router собирает маршруты и общий для них конвейер middleware.
//
// Порядок middleware существенен. Снаружи — восстановление после паники:
// оно обязано поймать в том числе панику в остальных middleware. Дальше
// логирование, чтобы в лог попал и апдейт, обработка которого сорвалась
// на определении пользователя. Затем определение пользователя, и только
// после него локализация — язык интерфейса известен из его настроек.
func router(
	transport *telegram.Transport, catalog port.Catalog, pool *pgxpool.Pool,
	cfg *config.Config, log *slog.Logger, registry *metrics.Registry, alerter *telegram.Alerter,
) (port.UpdateHandler, error) {
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
		telegram.Recover(transport, catalog, log, alerter),
		telegram.Measure(registry),
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

	sm2, err := study.NewSM2(study.DefaultSM2Config(), jitter)
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
		Scheduler: sm2,
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
		Scheduler: sm2,
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

	accountService, err := account.New(account.Deps{Users: users})
	if err != nil {
		return nil, err
	}
	accountHandler, err := telegram.NewAccount(accountService, courseService, transport)
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
	accountHandler.Register(r)
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

// poolMetrics показывает состояние пула соединений.
//
// Датчиками, а не счётчиками: важно не сколько соединений брали за всё
// время, а сколько занято прямо сейчас — по этому числу видно утечку.
func poolMetrics(registry *metrics.Registry, pool *pgxpool.Pool) {
	registry.NewGauge("lexi_db_connections_acquired",
		"Занятые соединения пула", func() float64 {
			return float64(pool.Stat().AcquiredConns())
		})
	registry.NewGauge("lexi_db_connections_idle",
		"Свободные соединения пула", func() float64 {
			return float64(pool.Stat().IdleConns())
		})
	registry.NewGauge("lexi_db_connections_total",
		"Всего соединений в пуле", func() float64 {
			return float64(pool.Stat().TotalConns())
		})
}
