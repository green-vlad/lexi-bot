package postgres

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Параметры пула по умолчанию.
//
// Приложение рассчитано на сотню пользователей, а это единицы запросов
// в секунду: десяти подключений хватает с запасом, и держать больше вредно —
// каждое подключение к Postgres стоит процесса на стороне сервера.
const (
	DefaultMaxConns          = 10
	DefaultMinConns          = 2
	DefaultMaxConnLifetime   = 30 * time.Minute
	DefaultMaxConnIdleTime   = 5 * time.Minute
	DefaultConnectTimeout    = 5 * time.Second
	DefaultStatementTimeout  = 5 * time.Second
	DefaultHealthCheckPeriod = time.Minute
)

// PoolConfig — параметры пула подключений.
type PoolConfig struct {
	DSN string
	// MaxConns и MinConns — верхняя и нижняя границы числа подключений.
	MaxConns int32
	MinConns int32
	// MaxConnLifetime ограничивает возраст подключения. Нужен не ради
	// экономии, а чтобы после смены сетевого маршрута или перезапуска базы
	// пул не держал мёртвые соединения бесконечно.
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	// ConnectTimeout — сколько ждём установки соединения.
	ConnectTimeout time.Duration
	// StatementTimeout обрывает запрос на стороне сервера. Без него запрос,
	// заблокированный чужой транзакцией, держит подключение до бесконечности,
	// и пул выедается один слот за другим.
	StatementTimeout  time.Duration
	HealthCheckPeriod time.Duration
	// AppName попадает в pg_stat_activity: по нему видно, чьи это запросы.
	AppName string
}

// DefaultPoolConfig возвращает параметры по умолчанию для указанной строки
// подключения. Указатель, а не значение, — чтобы вызывающий правил нужные
// поля на месте, как это делает pgxpool.ParseConfig.
func DefaultPoolConfig(dsn string) *PoolConfig {
	return &PoolConfig{
		DSN:               dsn,
		MaxConns:          DefaultMaxConns,
		MinConns:          DefaultMinConns,
		MaxConnLifetime:   DefaultMaxConnLifetime,
		MaxConnIdleTime:   DefaultMaxConnIdleTime,
		ConnectTimeout:    DefaultConnectTimeout,
		StatementTimeout:  DefaultStatementTimeout,
		HealthCheckPeriod: DefaultHealthCheckPeriod,
		AppName:           "lexi-bot",
	}
}

// NewPool открывает пул подключений и проверяет, что база отвечает.
//
// Проверка при создании не формальность: без неё приложение считается
// запущенным, отвечает на healthcheck и падает только на первом запросе
// пользователя — то есть авария обнаружится позже и не там.
func NewPool(ctx context.Context, cfg *PoolConfig) (*pgxpool.Pool, error) {
	if cfg == nil {
		return nil, fmt.Errorf("параметры пула не заданы")
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("разобрать строку подключения: %w", err)
	}

	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	poolCfg.HealthCheckPeriod = cfg.HealthCheckPeriod
	poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout

	if poolCfg.ConnConfig.RuntimeParams == nil {
		poolCfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	if cfg.AppName != "" {
		poolCfg.ConnConfig.RuntimeParams["application_name"] = cfg.AppName
	}
	if cfg.StatementTimeout > 0 {
		ms := strconv.FormatInt(cfg.StatementTimeout.Milliseconds(), 10)
		poolCfg.ConnConfig.RuntimeParams["statement_timeout"] = ms
		// Забытая открытая транзакция держит блокировки и не даёт вакууму
		// чистить мёртвые строки, поэтому обрываем и её — с запасом,
		// чтобы не мешать честным транзакциям.
		poolCfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] =
			strconv.FormatInt((cfg.StatementTimeout * 2).Milliseconds(), 10)
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("создать пул подключений: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("база не отвечает: %w", err)
	}
	return pool, nil
}
