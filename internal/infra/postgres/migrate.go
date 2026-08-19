// Package postgres содержит инфраструктурную обвязку над PostgreSQL: накатывание
// миграций и пул подключений. Прикладной код сюда не заглядывает — он работает
// с репозиториями через порты, объявленные в usecase.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/pressly/goose/v3"

	// Регистрирует драйвер database/sql с именем "pgx/v5".
	_ "github.com/jackc/pgx/v5/stdlib"

	"lexi-bot/migrations"
)

const migrationDriver = "pgx/v5"

// Migrator накатывает и откатывает схему. Файлы миграций встроены в бинарник,
// поэтому одна и та же логика работает и при старте приложения, и из cmd/migrate.
type Migrator struct {
	db       *sql.DB
	provider *goose.Provider
}

// NewMigrator открывает отдельное подключение для работы со схемой.
// Вызывающий обязан закрыть мигратор через Close.
func NewMigrator(dsn string) (*Migrator, error) {
	db, err := sql.Open(migrationDriver, dsn)
	if err != nil {
		return nil, fmt.Errorf("открыть подключение для миграций: %w", err)
	}

	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("создать провайдер миграций: %w", err)
	}

	return &Migrator{db: db, provider: provider}, nil
}

// Close освобождает подключение.
func (m *Migrator) Close() error {
	return m.db.Close()
}

// Ping проверяет доступность базы. Отдельный шаг нужен, чтобы отличить
// «база недоступна» от «миграция сломана» — это разные аварии.
func (m *Migrator) Ping(ctx context.Context) error {
	if err := m.provider.Ping(ctx); err != nil {
		return fmt.Errorf("база недоступна: %w", err)
	}
	return nil
}

// Applied описывает одну применённую или откаченную миграцию.
type Applied struct {
	Version  int64
	Name     string
	Duration time.Duration
}

// Up накатывает все непринятые миграции и возвращает список применённых.
// Пустой список означает, что схема уже была актуальной.
func (m *Migrator) Up(ctx context.Context) ([]Applied, error) {
	results, err := m.provider.Up(ctx)
	if err != nil {
		return nil, fmt.Errorf("накатить миграции: %w", err)
	}
	return toApplied(results), nil
}

// Down откатывает последнюю применённую миграцию.
func (m *Migrator) Down(ctx context.Context) (Applied, error) {
	result, err := m.provider.Down(ctx)
	if err != nil {
		return Applied{}, fmt.Errorf("откатить миграцию: %w", err)
	}
	return Applied{Version: result.Source.Version, Name: result.Source.Path, Duration: result.Duration}, nil
}

// MigrationStatus описывает состояние одной миграции.
type MigrationStatus struct {
	Version   int64
	Name      string
	Applied   bool
	AppliedAt time.Time
}

// Status возвращает состояние всех известных миграций.
func (m *Migrator) Status(ctx context.Context) ([]MigrationStatus, error) {
	states, err := m.provider.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("получить статус миграций: %w", err)
	}

	statuses := make([]MigrationStatus, 0, len(states))
	for _, s := range states {
		statuses = append(statuses, MigrationStatus{
			Version:   s.Source.Version,
			Name:      s.Source.Path,
			Applied:   s.State == goose.StateApplied,
			AppliedAt: s.AppliedAt,
		})
	}
	return statuses, nil
}

// Version возвращает текущую версию схемы в базе.
func (m *Migrator) Version(ctx context.Context) (int64, error) {
	version, err := m.provider.GetDBVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("получить версию схемы: %w", err)
	}
	return version, nil
}

func toApplied(results []*goose.MigrationResult) []Applied {
	applied := make([]Applied, 0, len(results))
	for _, r := range results {
		applied = append(applied, Applied{
			Version:  r.Source.Version,
			Name:     r.Source.Path,
			Duration: r.Duration,
		})
	}
	return applied
}
