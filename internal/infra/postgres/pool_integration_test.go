//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"lexi-bot/test/pgtest"
)

func TestPoolWorksAgainstRealDatabase(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()

	var answer int
	if err := pool.QueryRow(ctx, "SELECT 1").Scan(&answer); err != nil {
		t.Fatalf("запрос к базе не прошёл: %v", err)
	}
	if answer != 1 {
		t.Errorf("SELECT 1 вернул %d", answer)
	}
}

func TestSchemaIsMigrated(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()

	// Справочник языков заполняет сама миграция: без него не создать
	// ни пользователя, ни колоду.
	var languages int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM languages").Scan(&languages); err != nil {
		t.Fatalf("справочник языков недоступен: %v", err)
	}
	if languages < 4 {
		t.Errorf("в справочнике %d языков, ожидалось не меньше четырёх", languages)
	}

	// И главный индекс приложения на месте.
	var indexes int
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_indexes
		WHERE schemaname = 'public' AND indexname = 'cards_due_idx'`).Scan(&indexes)
	if err != nil {
		t.Fatalf("не удалось проверить индексы: %v", err)
	}
	if indexes != 1 {
		t.Error("индекс cards_due_idx не найден")
	}
}

func TestRuntimeParamsApplied(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()

	// Запрос, заблокированный чужой транзакцией, должен обрываться сервером,
	// иначе он держит подключение и пул выедается слот за слотом.
	var timeout string
	if err := pool.QueryRow(ctx, "SHOW statement_timeout").Scan(&timeout); err != nil {
		t.Fatalf("не удалось прочитать statement_timeout: %v", err)
	}
	if timeout == "0" {
		t.Error("statement_timeout не задан")
	}

	var appName string
	if err := pool.QueryRow(ctx, "SHOW application_name").Scan(&appName); err != nil {
		t.Fatalf("не удалось прочитать application_name: %v", err)
	}
	if appName != "lexi-bot" {
		t.Errorf("application_name = %q, ожидалось lexi-bot", appName)
	}
}

// Следующие два теста проверяют саму обвязку: данные первого не должны
// достаться второму, иначе интеграционные тесты начнут зависеть от порядка.

func TestCleanupLeavesDataBehind(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()

	_, err := pool.Exec(ctx, "INSERT INTO users (tg_user_id, ui_lang) VALUES (777, 'ru')")
	if err != nil {
		t.Fatalf("вставка пользователя не прошла: %v", err)
	}

	var users int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&users); err != nil {
		t.Fatalf("подсчёт пользователей не прошёл: %v", err)
	}
	if users != 1 {
		t.Errorf("пользователей в базе %d, ожидался один", users)
	}
}

func TestCleanupRunsBeforeEachTest(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()

	var users int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&users); err != nil {
		t.Fatalf("подсчёт пользователей не прошёл: %v", err)
	}
	if users != 0 {
		t.Errorf("в базе %d пользователей от прошлого теста, ожидалась пустая база", users)
	}

	// А справочник языков очистка не трогает: он часть схемы, а не данные теста.
	var languages int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM languages").Scan(&languages); err != nil {
		t.Fatalf("справочник языков недоступен: %v", err)
	}
	if languages < 4 {
		t.Error("очистка вымыла справочник языков")
	}
}
