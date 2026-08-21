//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"lexi-bot/internal/adapter/storage/postgres"
	"lexi-bot/internal/domain/user"
	"lexi-bot/test/pgtest"
)

var errBoom = errors.New("сценарий передумал")

func countUsers(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()

	var count int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM users").Scan(&count); err != nil {
		t.Fatalf("подсчёт пользователей не прошёл: %v", err)
	}
	return count
}

func TestInTxCommits(t *testing.T) {
	pool := pgtest.New(t)
	tm := postgres.NewTxManager(pool)
	users := postgres.NewUserRepo(pool)
	settings := postgres.NewSettingsRepo(pool)
	ctx := context.Background()

	err := tm.InTx(ctx, func(ctx context.Context) error {
		saved, _, err := users.Ensure(ctx, newUser(t, 777, "durov", user.UILangRU))
		if err != nil {
			return err
		}
		return settings.Save(ctx, saved.ID, user.DefaultSettings(user.UTCTimezone()))
	})
	if err != nil {
		t.Fatalf("InTx() вернул ошибку: %v", err)
	}

	if got := countUsers(t, pool); got != 1 {
		t.Errorf("после коммита пользователей %d, ожидался один", got)
	}
}

func TestInTxRollsBackEverything(t *testing.T) {
	pool := pgtest.New(t)
	tm := postgres.NewTxManager(pool)
	users := postgres.NewUserRepo(pool)
	settings := postgres.NewSettingsRepo(pool)
	ctx := context.Background()

	// Правило: если функция вернула ошибку, не изменилось ничего —
	// ни пользователь, ни настройки, записанные до неё.
	err := tm.InTx(ctx, func(ctx context.Context) error {
		saved, _, err := users.Ensure(ctx, newUser(t, 777, "durov", user.UILangRU))
		if err != nil {
			return err
		}
		if err := settings.Save(ctx, saved.ID, user.DefaultSettings(user.UTCTimezone())); err != nil {
			return err
		}
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("InTx() = %v, ожидалась ошибка сценария", err)
	}

	if got := countUsers(t, pool); got != 0 {
		t.Errorf("после отката пользователей %d, ожидался ноль", got)
	}

	var rows int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM user_settings").Scan(&rows); err != nil {
		t.Fatalf("подсчёт настроек не прошёл: %v", err)
	}
	if rows != 0 {
		t.Errorf("после отката осталось %d строк настроек", rows)
	}
}

func TestInTxSeesOwnWrites(t *testing.T) {
	pool := pgtest.New(t)
	tm := postgres.NewTxManager(pool)
	users := postgres.NewUserRepo(pool)
	ctx := context.Background()

	err := tm.InTx(ctx, func(ctx context.Context) error {
		saved, _, err := users.Ensure(ctx, newUser(t, 777, "durov", user.UILangRU))
		if err != nil {
			return err
		}

		// Чтение внутри транзакции обязано видеть незакоммиченную запись:
		// иначе «ввести новые слова, затем показать их» не собрать.
		found, err := users.ByID(ctx, saved.ID)
		if err != nil {
			return err
		}
		if found.TelegramID != saved.TelegramID {
			t.Errorf("внутри транзакции прочитан не тот пользователь: %+v", found)
		}

		// А снаружи её ещё не видно.
		if got := countUsers(t, pool); got != 0 {
			t.Errorf("снаружи транзакции видно %d пользователей, ожидался ноль", got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("InTx() вернул ошибку: %v", err)
	}
}

func TestNestedInTxJoinsOuter(t *testing.T) {
	pool := pgtest.New(t)
	tm := postgres.NewTxManager(pool)
	users := postgres.NewUserRepo(pool)
	ctx := context.Background()

	// Вложенный вызов не открывает вторую транзакцию и не ставит SAVEPOINT:
	// ошибка внутри откатывает всё, включая работу внешнего вызова.
	err := tm.InTx(ctx, func(ctx context.Context) error {
		if _, _, err := users.Ensure(ctx, newUser(t, 777, "durov", user.UILangRU)); err != nil {
			return err
		}

		return tm.InTx(ctx, func(ctx context.Context) error {
			if _, _, err := users.Ensure(ctx, newUser(t, 888, "pavel", user.UILangEN)); err != nil {
				return err
			}
			return errBoom
		})
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("InTx() = %v, ожидалась ошибка вложенного вызова", err)
	}

	if got := countUsers(t, pool); got != 0 {
		t.Errorf("после отката пользователей %d, ожидался ноль: вложенный вызов должен откатывать внешний", got)
	}
}

func TestNestedInTxCommitsOnce(t *testing.T) {
	pool := pgtest.New(t)
	tm := postgres.NewTxManager(pool)
	users := postgres.NewUserRepo(pool)
	ctx := context.Background()

	err := tm.InTx(ctx, func(ctx context.Context) error {
		if _, _, err := users.Ensure(ctx, newUser(t, 777, "durov", user.UILangRU)); err != nil {
			return err
		}
		return tm.InTx(ctx, func(ctx context.Context) error {
			_, _, err := users.Ensure(ctx, newUser(t, 888, "pavel", user.UILangEN))
			return err
		})
	})
	if err != nil {
		t.Fatalf("InTx() вернул ошибку: %v", err)
	}

	if got := countUsers(t, pool); got != 2 {
		t.Errorf("пользователей %d, ожидалось двое", got)
	}
}

func TestInTxRollsBackOnPanic(t *testing.T) {
	pool := pgtest.New(t)
	tm := postgres.NewTxManager(pool)
	users := postgres.NewUserRepo(pool)
	ctx := context.Background()

	// Паника в хендлере ловится middleware выше (T-023), но транзакция
	// не должна оставаться висеть до таймаута: её закрывает defer.
	func() {
		defer func() {
			if recover() == nil {
				t.Error("паника не дошла до вызывающего")
			}
		}()

		_ = tm.InTx(ctx, func(ctx context.Context) error {
			if _, _, err := users.Ensure(ctx, newUser(t, 777, "durov", user.UILangRU)); err != nil {
				return err
			}
			panic("что-то пошло не так")
		})
	}()

	if got := countUsers(t, pool); got != 0 {
		t.Errorf("после паники пользователей %d, ожидался ноль", got)
	}
}
