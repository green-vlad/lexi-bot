//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"lexi-bot/internal/adapter/storage/postgres"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/test/pgtest"
)

// newUser собирает пользователя для теста.
func newUser(t *testing.T, tgID user.TelegramID, name string, lang user.UILang) user.User {
	t.Helper()

	u, err := user.NewUser(tgID, name, lang)
	if err != nil {
		t.Fatalf("NewUser() вернул ошибку: %v", err)
	}
	return u
}

// ensureUser заводит пользователя и возвращает его.
func ensureUser(t *testing.T, pool *pgxpool.Pool, tgID user.TelegramID) user.User {
	t.Helper()

	repo := postgres.NewUserRepo(pool)
	saved, _, err := repo.Ensure(context.Background(), newUser(t, tgID, "durov", user.UILangRU))
	if err != nil {
		t.Fatalf("Ensure() вернул ошибку: %v", err)
	}
	return saved
}

func TestUserEnsureCreates(t *testing.T) {
	pool := pgtest.New(t)
	repo := postgres.NewUserRepo(pool)
	ctx := context.Background()

	saved, created, err := repo.Ensure(ctx, newUser(t, 777, "@durov", user.UILangRU))
	if err != nil {
		t.Fatalf("Ensure() вернул ошибку: %v", err)
	}
	if !created {
		t.Error("первый Ensure() должен сообщать о создании")
	}
	if saved.ID == 0 {
		t.Error("идентификатор не присвоен")
	}
	if saved.TelegramID != 777 {
		t.Errorf("TelegramID = %d, ожидалось 777", saved.TelegramID)
	}
	if saved.Username != "durov" {
		t.Errorf("Username = %q, ожидалось durov (собачка отбрасывается доменом)", saved.Username)
	}
	if !saved.IsActive() {
		t.Error("новый пользователь должен быть активным")
	}
}

func TestUserEnsureIsIdempotent(t *testing.T) {
	pool := pgtest.New(t)
	repo := postgres.NewUserRepo(pool)
	ctx := context.Background()

	first, _, err := repo.Ensure(ctx, newUser(t, 777, "durov", user.UILangRU))
	if err != nil {
		t.Fatalf("Ensure() вернул ошибку: %v", err)
	}

	// Повторный /start: тот же пользователь, а не второй.
	second, created, err := repo.Ensure(ctx, newUser(t, 777, "pavel", user.UILangEN))
	if err != nil {
		t.Fatalf("повторный Ensure() вернул ошибку: %v", err)
	}
	if created {
		t.Error("повторный Ensure() не должен сообщать о создании")
	}
	if second.ID != first.ID {
		t.Errorf("идентификатор сменился: %d против %d", second.ID, first.ID)
	}

	// Имя в Telegram обновляется — его пользователь мог сменить.
	if second.Username != "pavel" {
		t.Errorf("Username = %q, ожидалось pavel", second.Username)
	}
	// А язык интерфейса нет: его выбирают в боте, и повторный /start
	// не должен откатывать этот выбор.
	if second.UILang != user.UILangRU {
		t.Errorf("UILang = %q, ожидалось ru: повторный /start не меняет язык", second.UILang)
	}

	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&count); err != nil {
		t.Fatalf("подсчёт пользователей не прошёл: %v", err)
	}
	if count != 1 {
		t.Errorf("в базе %d пользователей, ожидался один", count)
	}
}

func TestUserEnsureRevivesDeleted(t *testing.T) {
	pool := pgtest.New(t)
	repo := postgres.NewUserRepo(pool)
	ctx := context.Background()

	saved := ensureUser(t, pool, 777)
	if err := repo.SoftDelete(ctx, saved.ID, time.Now()); err != nil {
		t.Fatalf("SoftDelete() вернул ошибку: %v", err)
	}

	// Пользователь, заблокировавший бота и вернувшийся, продолжает с того же
	// места, а не начинает заново.
	revived, created, err := repo.Ensure(ctx, newUser(t, 777, "durov", user.UILangRU))
	if err != nil {
		t.Fatalf("Ensure() вернул ошибку: %v", err)
	}
	if created {
		t.Error("вернувшийся пользователь не создаётся заново")
	}
	if revived.ID != saved.ID {
		t.Errorf("идентификатор сменился: %d против %d", revived.ID, saved.ID)
	}
	if !revived.IsActive() {
		t.Error("после возвращения мягкое удаление должно сниматься")
	}
}

func TestUserLookup(t *testing.T) {
	pool := pgtest.New(t)
	repo := postgres.NewUserRepo(pool)
	ctx := context.Background()

	saved := ensureUser(t, pool, 777)

	byTg, err := repo.ByTelegramID(ctx, 777)
	if err != nil {
		t.Fatalf("ByTelegramID() вернул ошибку: %v", err)
	}
	if byTg.ID != saved.ID {
		t.Errorf("ByTelegramID() вернул пользователя %d, ожидался %d", byTg.ID, saved.ID)
	}

	byID, err := repo.ByID(ctx, saved.ID)
	if err != nil {
		t.Fatalf("ByID() вернул ошибку: %v", err)
	}
	if byID.TelegramID != saved.TelegramID {
		t.Errorf("ByID() вернул пользователя %d, ожидался %d", byID.TelegramID, saved.TelegramID)
	}

	if _, err := repo.ByTelegramID(ctx, 999); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("ByTelegramID() для несуществующего = %v, ожидалась ErrNotFound", err)
	}
	if _, err := repo.ByID(ctx, 12345); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("ByID() для несуществующего = %v, ожидалась ErrNotFound", err)
	}
}

func TestUserSetUILang(t *testing.T) {
	pool := pgtest.New(t)
	repo := postgres.NewUserRepo(pool)
	ctx := context.Background()

	saved := ensureUser(t, pool, 777)
	if err := repo.SetUILang(ctx, saved.ID, user.UILangEN); err != nil {
		t.Fatalf("SetUILang() вернул ошибку: %v", err)
	}

	updated, err := repo.ByID(ctx, saved.ID)
	if err != nil {
		t.Fatalf("ByID() вернул ошибку: %v", err)
	}
	if updated.UILang != user.UILangEN {
		t.Errorf("UILang = %q, ожидалось en", updated.UILang)
	}

	// Обновление несуществующей строки — не успех.
	if err := repo.SetUILang(ctx, 12345, user.UILangEN); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("SetUILang() для несуществующего = %v, ожидалась ErrNotFound", err)
	}
}

func TestUserSoftDeleteKeepsRow(t *testing.T) {
	pool := pgtest.New(t)
	repo := postgres.NewUserRepo(pool)
	ctx := context.Background()

	saved := ensureUser(t, pool, 777)
	deletedAt := time.Now().UTC().Truncate(time.Millisecond)

	if err := repo.SoftDelete(ctx, saved.ID, deletedAt); err != nil {
		t.Fatalf("SoftDelete() вернул ошибку: %v", err)
	}

	// Строка остаётся: на неё ссылается журнал повторений.
	found, err := repo.ByID(ctx, saved.ID)
	if err != nil {
		t.Fatalf("ByID() после мягкого удаления вернул ошибку: %v", err)
	}
	if found.IsActive() {
		t.Error("после SoftDelete() пользователь не должен считаться активным")
	}
	if !found.DeletedAt.Equal(deletedAt) {
		t.Errorf("DeletedAt = %v, ожидалось %v", found.DeletedAt, deletedAt)
	}
}

func TestUserPurgeCascades(t *testing.T) {
	pool := pgtest.New(t)
	users := postgres.NewUserRepo(pool)
	settings := postgres.NewSettingsRepo(pool)
	ctx := context.Background()

	saved := ensureUser(t, pool, 777)
	if err := settings.Save(ctx, saved.ID, user.DefaultSettings(user.UTCTimezone())); err != nil {
		t.Fatalf("Save() вернул ошибку: %v", err)
	}

	// Личное слово пользователя — оно тоже должно уйти.
	_, err := pool.Exec(ctx,
		"INSERT INTO lexemes (lang_code, term, owner_user_id) VALUES ('ko', '집', $1)", int64(saved.ID))
	if err != nil {
		t.Fatalf("вставка личного слова не прошла: %v", err)
	}

	if err := users.Purge(ctx, saved.ID); err != nil {
		t.Fatalf("Purge() вернул ошибку: %v", err)
	}

	for _, check := range []struct {
		name  string
		query string
	}{
		{"пользователи", "SELECT count(*) FROM users"},
		{"настройки", "SELECT count(*) FROM user_settings"},
		{"личные слова", "SELECT count(*) FROM lexemes WHERE owner_user_id IS NOT NULL"},
	} {
		var count int
		if err := pool.QueryRow(ctx, check.query).Scan(&count); err != nil {
			t.Fatalf("подсчёт (%s) не прошёл: %v", check.name, err)
		}
		if count != 0 {
			t.Errorf("после удаления осталось %d записей (%s)", count, check.name)
		}
	}

	if err := users.Purge(ctx, saved.ID); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("повторный Purge() = %v, ожидалась ErrNotFound", err)
	}
}
