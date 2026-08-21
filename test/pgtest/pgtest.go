//go:build integration

// Package pgtest поднимает базу для интеграционных тестов.
//
// Источник базы выбирается сам: если задан TEST_DATABASE_URL, берётся он —
// так работает CI, где Postgres поднят сервис-контейнером; иначе поднимается
// контейнер через testcontainers — так работает разработчик, которому
// не нужно ничего готовить руками.
//
// Пакет собирается только с тегом integration, поэтому обычный go test
// не тянет за собой ни docker-клиент, ни сам testcontainers.
package pgtest

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"lexi-bot/internal/infra/postgres"
)

// EnvDatabaseURL — переменная окружения с готовой тестовой базой.
const EnvDatabaseURL = "TEST_DATABASE_URL"

// Образ должен совпадать с тем, что стоит в docker-compose.yml и в CI:
// расхождение версий Postgres между тестами и продом — источник находок,
// которые обнаруживаются в самый неподходящий момент.
const postgresImage = "postgres:16-alpine"

const (
	startupTimeout = 90 * time.Second
	setupTimeout   = 60 * time.Second
)

// Таблицы, которые чистить нельзя: служебная таблица goose и справочник
// языков, заполненный самой миграцией.
var keepTables = map[string]bool{
	"goose_db_version": true,
	"languages":        true,
}

var (
	once   sync.Once
	shared *database
	setErr error
)

type database struct {
	pool   *pgxpool.Pool
	tables []string
}

// New возвращает пул к чистой базе: перед каждым тестом данные предыдущего
// удаляются, справочник языков и схема остаются.
//
// База одна на весь процесс: поднимать контейнер на каждый тест — минуты
// вместо секунд. Поэтому тесты, работающие с базой, нельзя пускать
// параллельно друг с другом (t.Parallel в них не место).
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()

	once.Do(func() { shared, setErr = setup() })
	if setErr != nil {
		t.Fatalf("подготовить тестовую базу: %v", setErr)
	}

	truncate(t, shared)
	return shared.pool
}

// setup поднимает базу и накатывает схему. Вызывается один раз на процесс.
func setup() (*database, error) {
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()

	dsn, err := resolveDSN(ctx)
	if err != nil {
		return nil, err
	}

	if err := migrateSchema(ctx, dsn); err != nil {
		return nil, err
	}

	pool, err := postgres.NewPool(ctx, postgres.DefaultPoolConfig(dsn))
	if err != nil {
		return nil, err
	}

	tables, err := listTables(ctx, pool)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &database{pool: pool, tables: tables}, nil
}

// resolveDSN возвращает строку подключения к тестовой базе, поднимая
// контейнер, если готовой базы не задано.
func resolveDSN(ctx context.Context) (string, error) {
	if dsn := os.Getenv(EnvDatabaseURL); dsn != "" {
		return dsn, nil
	}
	return startContainer(ctx)
}

// startContainer поднимает Postgres в контейнере.
//
// Останавливать его самим не нужно: testcontainers запускает рядом сторожа
// (ryuk), который убирает контейнеры после завершения процесса тестов —
// в том числе если тесты упали или их прервали.
func startContainer(ctx context.Context) (string, error) {
	const (
		user     = "lexi"
		password = "lexi"
		database = "lexi_test"
	)

	req := testcontainers.ContainerRequest{
		Image:        postgresImage,
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_USER":     user,
			"POSTGRES_PASSWORD": password,
			"POSTGRES_DB":       database,
			// Тестовой базе долговечность не нужна, а старт заметно быстрее.
			"POSTGRES_INITDB_ARGS": "--no-sync",
		},
		Cmd: []string{"postgres", "-c", "fsync=off", "-c", "full_page_writes=off"},
		// Одного сообщения о готовности мало: Postgres пишет его и при первом,
		// служебном запуске, когда база ещё создаётся и порт закроется снова.
		WaitingFor: wait.ForAll(
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
			wait.ForListeningPort("5432/tcp"),
		).WithDeadline(startupTimeout),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return "", fmt.Errorf("поднять контейнер с Postgres: %w", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		return "", fmt.Errorf("узнать адрес контейнера: %w", err)
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		return "", fmt.Errorf("узнать порт контейнера: %w", err)
	}

	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		user, password, host, port.Port(), database), nil
}

// migrateSchema накатывает те же миграции, что и приложение при старте:
// тесты обязаны работать против настоящей схемы, а не против её копии.
func migrateSchema(ctx context.Context, dsn string) error {
	migrator, err := postgres.NewMigrator(dsn)
	if err != nil {
		return err
	}
	defer func() { _ = migrator.Close() }()

	if _, err := migrator.Up(ctx); err != nil {
		return fmt.Errorf("накатить схему: %w", err)
	}
	return nil
}

// listTables собирает список таблиц, которые чистятся между тестами.
func listTables(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT tablename
		FROM pg_tables
		WHERE schemaname = 'public'
		ORDER BY tablename`)
	if err != nil {
		return nil, fmt.Errorf("получить список таблиц: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("прочитать имя таблицы: %w", err)
		}
		if !keepTables[name] {
			tables = append(tables, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("обойти список таблиц: %w", err)
	}
	if len(tables) == 0 {
		return nil, fmt.Errorf("в схеме нет таблиц: миграции не накатились")
	}
	return tables, nil
}

// truncate чистит данные перед тестом.
//
// Чистим до, а не после: если предыдущий тест упал, его данные останутся
// в базе, и с ними можно разобраться руками, а следующий тест всё равно
// начнёт с пустого места.
func truncate(t *testing.T, db *database) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// RESTART IDENTITY возвращает счётчики в начало: тесты, которые ждут
	// определённых идентификаторов, не должны зависеть от порядка запуска.
	stmt := "TRUNCATE " + join(db.tables) + " RESTART IDENTITY CASCADE"
	if _, err := db.pool.Exec(ctx, stmt); err != nil {
		t.Fatalf("очистить базу: %v", err)
	}
}

func join(tables []string) string {
	quoted := make([]string, 0, len(tables))
	for _, name := range tables {
		quoted = append(quoted, `"`+name+`"`)
	}
	return strings.Join(quoted, ", ")
}
