//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"lexi-bot/internal/adapter/storage/postgres"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/test/pgtest"
)

func TestSessionRoundTrip(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewSessionRepo(pool)

	owner := ensureUser(t, pool, 777)

	// Payload у каждого шага свой, и репозиторий не должен ничего о нём знать.
	payload := json.RawMessage(`{"term":"집","step":2,"tags":["ko","noun"]}`)
	want := port.Session{
		UserID:    owner.ID,
		State:     "add:waiting_translation",
		Payload:   payload,
		UpdatedAt: testNow,
	}
	if err := repo.Save(ctx, want); err != nil {
		t.Fatalf("Save() вернул ошибку: %v", err)
	}

	got, err := repo.Get(ctx, owner.ID)
	if err != nil {
		t.Fatalf("Get() вернул ошибку: %v", err)
	}
	if got.State != want.State {
		t.Errorf("State = %q, ожидалось %q", got.State, want.State)
	}
	if !got.UpdatedAt.Equal(testNow) {
		t.Errorf("UpdatedAt = %v, ожидалось %v", got.UpdatedAt, testNow)
	}

	// Сравниваем разобранным: jsonb хранит объект, а не строку, и порядок
	// ключей в нём свой.
	var gotFields, wantFields map[string]any
	if err := json.Unmarshal(got.Payload, &gotFields); err != nil {
		t.Fatalf("payload из базы не разбирается: %v", err)
	}
	if err := json.Unmarshal(payload, &wantFields); err != nil {
		t.Fatalf("исходный payload не разбирается: %v", err)
	}
	if gotFields["term"] != wantFields["term"] || gotFields["step"] != wantFields["step"] {
		t.Errorf("payload = %s, ожидалось %s", got.Payload, payload)
	}
	tags, ok := gotFields["tags"].([]any)
	if !ok || len(tags) != 2 {
		t.Errorf("массив в payload не пережил сохранение: %v", gotFields["tags"])
	}
}

func TestSessionSaveOverwrites(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewSessionRepo(pool)

	owner := ensureUser(t, pool, 777)

	first := port.Session{UserID: owner.ID, State: "add:waiting_term", UpdatedAt: testNow}
	if err := repo.Save(ctx, first); err != nil {
		t.Fatalf("Save() вернул ошибку: %v", err)
	}

	// Диалог у пользователя один: следующий шаг заменяет предыдущий.
	second := port.Session{
		UserID:    owner.ID,
		State:     "add:waiting_translation",
		Payload:   json.RawMessage(`{"term":"집"}`),
		UpdatedAt: testNow.Add(time.Minute),
	}
	if err := repo.Save(ctx, second); err != nil {
		t.Fatalf("повторный Save() вернул ошибку: %v", err)
	}

	got, err := repo.Get(ctx, owner.ID)
	if err != nil {
		t.Fatalf("Get() вернул ошибку: %v", err)
	}
	if got.State != second.State {
		t.Errorf("State = %q, ожидалось %q", got.State, second.State)
	}

	var rows int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM user_sessions").Scan(&rows); err != nil {
		t.Fatalf("подсчёт диалогов не прошёл: %v", err)
	}
	if rows != 1 {
		t.Errorf("строк диалога %d, ожидалась одна", rows)
	}
}

func TestSessionEmptyPayload(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewSessionRepo(pool)

	owner := ensureUser(t, pool, 777)

	// Шаг без данных — нормальное состояние, и колонка NOT NULL этому
	// не должна мешать.
	if err := repo.Save(ctx, port.Session{UserID: owner.ID, State: "onboarding:ui_lang"}); err != nil {
		t.Fatalf("Save() вернул ошибку: %v", err)
	}

	got, err := repo.Get(ctx, owner.ID)
	if err != nil {
		t.Fatalf("Get() вернул ошибку: %v", err)
	}
	if string(got.Payload) != "{}" {
		t.Errorf("payload = %s, ожидался пустой объект", got.Payload)
	}
	// Время проставила база: нулевое значение означает «сейчас».
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt не проставлен")
	}
}

func TestSessionSaveRejectsBrokenInput(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewSessionRepo(pool)

	owner := ensureUser(t, pool, 777)

	if err := repo.Save(ctx, port.Session{UserID: owner.ID, State: ""}); !errors.Is(err, port.ErrInvalidData) {
		t.Errorf("Save() без состояния = %v, ожидалась ErrInvalidData", err)
	}

	// Битый payload — ошибка сценария, и говорить о ней надо на своём языке,
	// а не кодом разбора Postgres.
	broken := port.Session{UserID: owner.ID, State: "add:waiting_term", Payload: json.RawMessage(`{"term":`)}
	if err := repo.Save(ctx, broken); !errors.Is(err, port.ErrInvalidData) {
		t.Errorf("Save() с битым payload = %v, ожидалась ErrInvalidData", err)
	}

	unknown := port.Session{UserID: 99999, State: "add:waiting_term"}
	if err := repo.Save(ctx, unknown); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("Save() для несуществующего пользователя = %v, ожидалась ErrNotFound", err)
	}
}

func TestSessionDeleteIsIdempotent(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewSessionRepo(pool)

	owner := ensureUser(t, pool, 777)

	if err := repo.Save(ctx, port.Session{UserID: owner.ID, State: "add:waiting_term"}); err != nil {
		t.Fatalf("Save() вернул ошибку: %v", err)
	}
	if err := repo.Delete(ctx, owner.ID); err != nil {
		t.Fatalf("Delete() вернул ошибку: %v", err)
	}
	if _, err := repo.Get(ctx, owner.ID); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("Get() после удаления = %v, ожидалась ErrNotFound", err)
	}

	// /cancel без начатого диалога — это «уже нечего отменять», а не авария.
	if err := repo.Delete(ctx, owner.ID); err != nil {
		t.Errorf("повторный Delete() = %v, ожидался успех", err)
	}
}

func TestSessionDeleteStale(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewSessionRepo(pool)

	fresh := ensureUser(t, pool, 777)
	stale := ensureUser(t, pool, 888)

	if err := repo.Save(ctx, port.Session{UserID: fresh.ID, State: "add:waiting_term", UpdatedAt: testNow}); err != nil {
		t.Fatalf("Save() вернул ошибку: %v", err)
	}
	// Диалог, брошенный месяц назад: пользователь давно о нём забыл,
	// а любое его сообщение до сих пор уходило бы в этот шаг.
	old := testNow.AddDate(0, -1, 0)
	if err := repo.Save(ctx, port.Session{UserID: stale.ID, State: "add:waiting_translation", UpdatedAt: old}); err != nil {
		t.Fatalf("Save() вернул ошибку: %v", err)
	}

	removed, err := repo.DeleteStale(ctx, testNow.AddDate(0, 0, -1))
	if err != nil {
		t.Fatalf("DeleteStale() вернул ошибку: %v", err)
	}
	if removed != 1 {
		t.Errorf("удалено %d диалогов, ожидался один", removed)
	}

	if _, err := repo.Get(ctx, stale.ID); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("протухший диалог остался: %v", err)
	}
	if _, err := repo.Get(ctx, fresh.ID); err != nil {
		t.Errorf("свежий диалог удалён: %v", err)
	}

	// Когда чистить нечего, метод не ошибка, а ноль.
	removed, err = repo.DeleteStale(ctx, testNow.AddDate(0, 0, -1))
	if err != nil || removed != 0 {
		t.Errorf("DeleteStale() = %d, %v; ожидался ноль без ошибки", removed, err)
	}
}

func TestSessionGoesAwayWithUser(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewSessionRepo(pool)

	owner := ensureUser(t, pool, 777)
	if err := repo.Save(ctx, port.Session{UserID: owner.ID, State: "add:waiting_term"}); err != nil {
		t.Fatalf("Save() вернул ошибку: %v", err)
	}

	if err := postgres.NewUserRepo(pool).Purge(ctx, owner.ID); err != nil {
		t.Fatalf("Purge() вернул ошибку: %v", err)
	}

	var rows int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM user_sessions").Scan(&rows); err != nil {
		t.Fatalf("подсчёт диалогов не прошёл: %v", err)
	}
	if rows != 0 {
		t.Errorf("после удаления пользователя осталось %d диалогов", rows)
	}
}
