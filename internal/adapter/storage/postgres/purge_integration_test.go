//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"lexi-bot/internal/adapter/storage/postgres"
	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/test/pgtest"
)

// userTables — всё, что ссылается на пользователя. Список сверяется
// с реальностью тем же тестом: если в схеме появится таблица с ссылкой
// на users, а здесь её не будет, проверка каскада её просто не заметит.
var userTables = []string{
	"user_settings", "user_courses", "cards", "reviews",
	"daily_counters", "user_sessions", "import_jobs", "outbox_notifications",
}

func TestPurgeLeavesNothingBehind(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	users := postgres.NewUserRepo(pool)

	f := newCourse(t, pool, 3)
	fill(t, pool, &f)

	// До удаления следы есть во всех таблицах — иначе проверка после
	// удаления ничего не значила бы.
	for _, table := range userTables {
		if got := countFor(t, pool, table, f.user.ID); got == 0 {
			t.Fatalf("до удаления в %s нет строк: тест не проверяет каскад", table)
		}
	}
	// И личное слово в словаре тоже.
	if got := countOwnedLexemes(t, pool, f.user.ID); got == 0 {
		t.Fatal("до удаления нет личных слов: тест не проверяет их каскад")
	}

	if err := users.Purge(ctx, f.user.ID); err != nil {
		t.Fatalf("Purge() вернул ошибку: %v", err)
	}

	for _, table := range userTables {
		if got := countFor(t, pool, table, f.user.ID); got != 0 {
			t.Errorf("после удаления в %s осталось %d строк", table, got)
		}
	}
	if got := countOwnedLexemes(t, pool, f.user.ID); got != 0 {
		t.Errorf("после удаления осталось %d личных слов", got)
	}

	// Самой записи тоже нет: удаление здесь настоящее, а не пометка.
	if _, err := users.ByID(ctx, f.user.ID); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("ByID() = %v, ожидалась ErrNotFound", err)
	}

	// А общий словарь на месте: встроенные слова принадлежат всем.
	var shared int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM lexemes WHERE owner_user_id IS NULL").Scan(&shared); err != nil {
		t.Fatalf("подсчёт словаря не прошёл: %v", err)
	}
	if shared == 0 {
		t.Error("встроенный словарь удалён вместе с пользователем")
	}
}

func TestPurgeOfMissingUser(t *testing.T) {
	pool := pgtest.New(t)

	// Удалять нечего — и это ошибка, а не тихий успех: молчание тут
	// означало бы, что кнопка сработала, хотя данные остались.
	if err := postgres.NewUserRepo(pool).Purge(context.Background(), 99999); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("Purge() = %v, ожидалась ErrNotFound", err)
	}
}

// fill оставляет следы пользователя во всех связанных таблицах.
func fill(t *testing.T, pool *pgxpool.Pool, f *courseFixture) {
	t.Helper()

	ctx := context.Background()
	repo := postgres.NewCardRepo(pool)

	// Настройки, карточка, журнал и дневной счётчик.
	if err := postgres.NewSettingsRepo(pool).Save(ctx, f.user.ID,
		user.DefaultSettings(user.UTCTimezone())); err != nil {
		t.Fatalf("Save() вернул ошибку: %v", err)
	}

	card := start(t, repo, f.course.ID, f.lexemes, 1)[0]
	next := study.CardState{State: study.StateReview, DueAt: testNow, IntervalDays: 1, EaseFactor: 2.5}
	review, err := study.NewReview(study.ReviewParams{
		CardID: card.ID, RatedAt: testNow, Rating: study.RatingGood,
		Mode: study.ModeChoice, IsCorrect: true, Prev: card.CardState, Next: next,
	})
	if err != nil {
		t.Fatalf("NewReview() вернул ошибку: %v", err)
	}
	if err := repo.Apply(ctx, &port.ReviewOutcome{
		CardID: card.ID, State: next, Review: review,
		UserID: f.user.ID, Day: testDay,
	}); err != nil {
		t.Fatalf("Apply() вернул ошибку: %v", err)
	}

	// Диалог, задание импорта и очередь отправки.
	if err := postgres.NewSessionRepo(pool).Save(ctx, port.Session{
		UserID: f.user.ID, State: "learn:typing", Payload: []byte(`{}`), UpdatedAt: testNow,
	}); err != nil {
		t.Fatalf("Save() вернул ошибку: %v", err)
	}
	if _, err := postgres.NewImportRepo(pool).Create(ctx, &port.ImportJob{
		UserID: f.user.ID, FileName: "words.csv", Status: port.ImportDone,
	}); err != nil {
		t.Fatalf("Create() вернул ошибку: %v", err)
	}
	if _, err := postgres.NewOutboxRepo(pool).Schedule(ctx, []port.Notification{{
		UserID: f.user.ID, Kind: port.NotificationReminder, ScheduledFor: testNow,
	}}); err != nil {
		t.Fatalf("Schedule() вернул ошибку: %v", err)
	}

	// И личное слово с переводом.
	personal, err := postgres.NewDeckRepo(pool).EnsurePersonal(ctx, int64(f.user.ID),
		lexicon.MustParseLanguage("ko"), "Мои слова")
	if err != nil {
		t.Fatalf("EnsurePersonal() вернул ошибку: %v", err)
	}
	own, err := lexicon.NewLexeme(lexicon.LexemeParams{
		Lang: lexicon.MustParseLanguage("ko"), Term: "냉장고", OwnerID: int64(f.user.ID),
	})
	if err != nil {
		t.Fatalf("NewLexeme() вернул ошибку: %v", err)
	}
	saved, err := postgres.NewLexemeRepo(pool).Upsert(ctx, []lexicon.Lexeme{own})
	if err != nil || len(saved) == 0 {
		t.Fatalf("Upsert() = %v, %v", saved, err)
	}
	if err := postgres.NewDeckRepo(pool).AddItems(ctx, []lexicon.DeckItem{
		{DeckID: personal.ID, LexemeID: saved[0].Lexeme.ID, Position: 0},
	}); err != nil {
		t.Fatalf("AddItems() вернул ошибку: %v", err)
	}
}

func countFor(t *testing.T, pool *pgxpool.Pool, table string, id user.ID) int {
	t.Helper()

	var count int
	// Имя таблицы приходит из константы в этом же файле, не из внешнего
	// мира: подставлять его строкой безопасно, а параметром нельзя.
	query := "SELECT count(*) FROM " + table + " WHERE user_id = $1"
	if table == "cards" || table == "daily_counters" {
		query = "SELECT count(*) FROM " + table + ` c
			JOIN user_courses uc ON uc.id = c.user_course_id WHERE uc.user_id = $1`
	}
	if err := pool.QueryRow(context.Background(), query, int64(id)).Scan(&count); err != nil {
		t.Fatalf("подсчёт %s не прошёл: %v", table, err)
	}
	return count
}

func countOwnedLexemes(t *testing.T, pool *pgxpool.Pool, id user.ID) int {
	t.Helper()

	var count int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM lexemes WHERE owner_user_id = $1", int64(id)).Scan(&count); err != nil {
		t.Fatalf("подсчёт личных слов не прошёл: %v", err)
	}
	return count
}
