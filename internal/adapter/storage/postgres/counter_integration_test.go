//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"lexi-bot/internal/adapter/storage/postgres"
	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/test/pgtest"
)

func TestCounterGetOfEmptyDayIsZero(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewCounterRepo(pool)

	f := newCourse(t, pool, 1)

	// В день, когда пользователь ещё не занимался, счётчиков нет — и это
	// нули, а не ошибка: иначе показ меню превращался бы в запись в базу.
	got, err := repo.Get(ctx, f.course.ID, testDay)
	if err != nil {
		t.Fatalf("Get() вернул ошибку: %v", err)
	}
	if got.NewIntroduced != 0 || got.ReviewsDone != 0 {
		t.Errorf("счётчики = %+v, ожидались нули", got)
	}
	if !got.Day.Equal(testDay) {
		t.Errorf("Day = %v, ожидалось %v", got.Day, testDay)
	}

	var rows int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM daily_counters").Scan(&rows); err != nil {
		t.Fatalf("подсчёт счётчиков не прошёл: %v", err)
	}
	if rows != 0 {
		t.Errorf("чтение завело %d строк, ожидалось ноль", rows)
	}
}

func TestCounterAddReview(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewCounterRepo(pool)

	f := newCourse(t, pool, 1)

	for i := 1; i <= 3; i++ {
		if err := repo.AddReview(ctx, f.course.ID, testDay); err != nil {
			t.Fatalf("AddReview() вернул ошибку: %v", err)
		}

		got, err := repo.Get(ctx, f.course.ID, testDay)
		if err != nil {
			t.Fatalf("Get() вернул ошибку: %v", err)
		}
		if got.ReviewsDone != i {
			t.Errorf("после %d ответов счётчик = %d", i, got.ReviewsDone)
		}
	}

	// Соседние сутки считаются отдельно.
	tomorrow := testDay.AddDate(0, 0, 1)
	if err := repo.AddReview(ctx, f.course.ID, tomorrow); err != nil {
		t.Fatalf("AddReview() вернул ошибку: %v", err)
	}
	got, err := repo.Get(ctx, f.course.ID, tomorrow)
	if err != nil {
		t.Fatalf("Get() вернул ошибку: %v", err)
	}
	if got.ReviewsDone != 1 {
		t.Errorf("счётчик следующих суток = %d, ожидалась единица", got.ReviewsDone)
	}
}

func TestCounterAddReviewRejectsUnknownCourse(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()

	err := postgres.NewCounterRepo(pool).AddReview(ctx, 99999, testDay)
	if !errors.Is(err, port.ErrNotFound) {
		t.Errorf("AddReview() = %v, ожидалась ErrNotFound", err)
	}
}

func TestCounterFollowsUserTimezone(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewCounterRepo(pool)

	f := newCourse(t, pool, 1)

	// Один и тот же момент попадает в разные календарные сутки у разных
	// пользователей. Дневной лимит обязан считаться по суткам пользователя,
	// иначе занимающийся вечером в Сеуле получал бы двойную порцию новых
	// слов в полночь UTC.
	moment := time.Date(2026, 8, 21, 20, 0, 0, 0, time.UTC)

	seoul := user.DefaultSettings(user.MustParseTimezone("Asia/Seoul"))         // уже 22 августа
	newYork := user.DefaultSettings(user.MustParseTimezone("America/New_York")) // ещё 21 августа

	seoulDay := seoul.DayStart(moment)
	newYorkDay := newYork.DayStart(moment)

	if err := repo.AddReview(ctx, f.course.ID, seoulDay); err != nil {
		t.Fatalf("AddReview() вернул ошибку: %v", err)
	}

	got, err := repo.Get(ctx, f.course.ID, seoulDay)
	if err != nil {
		t.Fatalf("Get() вернул ошибку: %v", err)
	}
	if got.ReviewsDone != 1 {
		t.Errorf("счётчик сеульских суток = %d, ожидалась единица", got.ReviewsDone)
	}

	// А для пользователя из Нью-Йорка это ещё вчерашние сутки, и счётчик
	// у них свой.
	got, err = repo.Get(ctx, f.course.ID, newYorkDay)
	if err != nil {
		t.Fatalf("Get() вернул ошибку: %v", err)
	}
	if got.ReviewsDone != 0 {
		t.Errorf("счётчик нью-йоркских суток = %d, ожидался ноль", got.ReviewsDone)
	}

	var day time.Time
	err = pool.QueryRow(ctx, "SELECT day FROM daily_counters WHERE user_course_id = $1", int64(f.course.ID)).Scan(&day)
	if err != nil {
		t.Fatalf("чтение даты счётчика не прошло: %v", err)
	}
	if got, want := day.Format(time.DateOnly), "2026-08-22"; got != want {
		t.Errorf("в базе дата %q, ожидалась %q — дата суток пользователя, а не UTC", got, want)
	}
}

func TestCounterCountsNewAndReviewsSeparately(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	counters := postgres.NewCounterRepo(pool)
	cards := postgres.NewCardRepo(pool)

	f := newCourse(t, pool, 3)

	start(t, cards, f.course.ID, f.lexemes, 2)
	if err := counters.AddReview(ctx, f.course.ID, testDay); err != nil {
		t.Fatalf("AddReview() вернул ошибку: %v", err)
	}

	// Новые слова и повторения живут в одной строке, но считаются порознь:
	// у них разные лимиты и разный смысл.
	got, err := counters.Get(ctx, f.course.ID, testDay)
	if err != nil {
		t.Fatalf("Get() вернул ошибку: %v", err)
	}
	if got.NewIntroduced != 2 {
		t.Errorf("новых = %d, ожидалось два", got.NewIntroduced)
	}
	if got.ReviewsDone != 1 {
		t.Errorf("повторений = %d, ожидалось одно", got.ReviewsDone)
	}
}

// makeReview собирает запись журнала для теста.
func makeReview(t *testing.T, cardID study.CardID, at time.Time, correct bool) study.Review {
	t.Helper()

	rating := study.RatingGood
	if !correct {
		rating = study.RatingAgain
	}
	review, err := study.NewReview(study.ReviewParams{
		CardID:    cardID,
		RatedAt:   at,
		Rating:    rating,
		Mode:      study.ModeChoice,
		IsCorrect: correct,
		Prev:      study.CardState{State: study.StateReview, DueAt: at, IntervalDays: 1, EaseFactor: 2.5},
		Next:      study.CardState{State: study.StateReview, DueAt: at, IntervalDays: 2, EaseFactor: 2.5},
	})
	if err != nil {
		t.Fatalf("NewReview() вернул ошибку: %v", err)
	}
	return review
}

func TestReviewAddAndStats(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewReviewRepo(pool)

	f := newCourse(t, pool, 1)
	card := start(t, postgres.NewCardRepo(pool), f.course.ID, f.lexemes, 1)[0]

	for i, correct := range []bool{true, true, false, true} {
		review := makeReview(t, card.ID, testNow.Add(time.Duration(i)*time.Minute), correct)
		if err := repo.Add(ctx, f.user.ID, &review); err != nil {
			t.Fatalf("Add() вернул ошибку: %v", err)
		}
	}

	stats, err := repo.Stats(ctx, port.StatsQuery{UserID: f.user.ID, Since: testNow.Add(-time.Hour)})
	if err != nil {
		t.Fatalf("Stats() вернул ошибку: %v", err)
	}
	if stats.Total != 4 || stats.Correct != 3 {
		t.Errorf("сводка = %+v, ожидалось 4 ответа и 3 верных", stats)
	}
	if got := stats.Accuracy(); got < 0.74 || got > 0.76 {
		t.Errorf("точность = %v, ожидалось около 0.75", got)
	}

	// Ответы до начала периода в сводку не попадают.
	recent, err := repo.Stats(ctx, port.StatsQuery{UserID: f.user.ID, Since: testNow.Add(2 * time.Minute)})
	if err != nil {
		t.Fatalf("Stats() вернул ошибку: %v", err)
	}
	if recent.Total != 2 {
		t.Errorf("за период получено %d ответов, ожидалось два", recent.Total)
	}

	// У пользователя без ответов точность нулевая, а не стопроцентная.
	empty, err := repo.Stats(ctx, port.StatsQuery{UserID: f.user.ID, Since: testNow.AddDate(0, 0, 1)})
	if err != nil {
		t.Fatalf("Stats() вернул ошибку: %v", err)
	}
	if empty.Total != 0 || empty.Accuracy() != 0 {
		t.Errorf("пустая сводка = %+v, точность %v", empty, empty.Accuracy())
	}
}

func TestReviewActiveDaysUseUserTimezone(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewReviewRepo(pool)

	f := newCourse(t, pool, 1)
	card := start(t, postgres.NewCardRepo(pool), f.course.ID, f.lexemes, 1)[0]

	// Все три ответа приходятся на 21 августа по UTC, но по Сеулу это два
	// разных дня: занимавшийся два вечера подряд не должен терять серию
	// из-за того, что сутки UTC ещё не кончились.
	moments := []time.Time{
		time.Date(2026, 8, 21, 3, 0, 0, 0, time.UTC),   // 21 августа, 12:00 в Сеуле
		time.Date(2026, 8, 21, 15, 30, 0, 0, time.UTC), // 22 августа, 00:30 в Сеуле
		time.Date(2026, 8, 21, 20, 0, 0, 0, time.UTC),  // 22 августа, 05:00 в Сеуле
	}
	for _, at := range moments {
		review := makeReview(t, card.ID, at, true)
		if err := repo.Add(ctx, f.user.ID, &review); err != nil {
			t.Fatalf("Add() вернул ошибку: %v", err)
		}
	}

	seoul := user.MustParseTimezone("Asia/Seoul")
	days, err := repo.ActiveDays(ctx, f.user.ID, seoul, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("ActiveDays() вернул ошибку: %v", err)
	}
	if len(days) != 2 {
		t.Fatalf("дней занятий %d, ожидалось два: %v", len(days), days)
	}
	// От свежего к старому.
	if got, want := days[0].Format(time.DateOnly), "2026-08-22"; got != want {
		t.Errorf("первый день = %q, ожидался %q", got, want)
	}
	if got, want := days[1].Format(time.DateOnly), "2026-08-21"; got != want {
		t.Errorf("второй день = %q, ожидался %q", got, want)
	}

	// Те же самые ответы в UTC складываются в один день вместо двух —
	// ровно та потеря серии, ради которой зона и приводится.
	utcDays, err := repo.ActiveDays(ctx, f.user.ID, user.UTCTimezone(), time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("ActiveDays() вернул ошибку: %v", err)
	}
	if len(utcDays) != 1 {
		t.Fatalf("дней в UTC %d, ожидался один: %v", len(utcDays), utcDays)
	}
	if got, want := utcDays[0].Format(time.DateOnly), "2026-08-21"; got != want {
		t.Errorf("день в UTC = %q, ожидался %q", got, want)
	}
}

func TestReviewStatsByCourse(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewReviewRepo(pool)

	f := newCourse(t, pool, 2)
	cards := start(t, postgres.NewCardRepo(pool), f.course.ID, f.lexemes, 2)

	// Второй курс того же пользователя: его ответы в сводку первого
	// попадать не должны.
	otherDeck := builtinDeck(t, pool, "en-demo", langEN)
	other, err := postgres.NewCourseRepo(pool).Ensure(ctx, study.Course{
		UserID: int64(f.user.ID), DeckID: otherDeck, TranslationLang: langRU, Status: study.CourseActive,
	})
	if err != nil {
		t.Fatalf("Ensure() вернул ошибку: %v", err)
	}
	otherLexemes := saveLexemes(t, pool, newLexeme(t, "새"))
	if err := postgres.NewDeckRepo(pool).AddItems(ctx, []lexicon.DeckItem{
		{DeckID: otherDeck, LexemeID: otherLexemes[0].ID, Position: 0},
	}); err != nil {
		t.Fatalf("AddItems() вернул ошибку: %v", err)
	}
	otherCards := start(t, postgres.NewCardRepo(pool), other.ID, otherLexemes, 1)

	for _, item := range []struct {
		card    study.CardID
		correct bool
	}{
		{cards[0].ID, true},
		{cards[1].ID, false},
		{otherCards[0].ID, true},
	} {
		review := makeReview(t, item.card, testNow, item.correct)
		if err := repo.Add(ctx, f.user.ID, &review); err != nil {
			t.Fatalf("Add() вернул ошибку: %v", err)
		}
	}

	// По одному курсу: два ответа, один верный.
	byCourse, err := repo.Stats(ctx, port.StatsQuery{
		UserID: f.user.ID, CourseID: f.course.ID, Since: testNow.Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("Stats() вернул ошибку: %v", err)
	}
	if byCourse.Total != 2 || byCourse.Correct != 1 {
		t.Errorf("сводка курса = %+v, ожидалось 2 ответа и 1 верный", byCourse)
	}

	// По пользователю целиком: все три.
	byUser, err := repo.Stats(ctx, port.StatsQuery{UserID: f.user.ID, Since: testNow.Add(-time.Hour)})
	if err != nil {
		t.Fatalf("Stats() вернул ошибку: %v", err)
	}
	if byUser.Total != 3 {
		t.Errorf("сводка пользователя = %+v, ожидалось три ответа", byUser)
	}
}

func TestCardNextDue(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewCardRepo(pool)

	f := newCourse(t, pool, 3)

	// У курса без карточек повторять нечего, и это не ошибка.
	if _, ok, err := repo.NextDue(ctx, f.course.ID); err != nil || ok {
		t.Fatalf("NextDue() = %t, %v; ожидалось «нечего»", ok, err)
	}

	cards := start(t, repo, f.course.ID, f.lexemes, 3)

	set := func(id study.CardID, due time.Time, state string) {
		t.Helper()
		if _, err := pool.Exec(ctx, "UPDATE cards SET due_at = $2, state = $3 WHERE id = $1",
			int64(id), due, state); err != nil {
			t.Fatalf("обновление карточки не прошло: %v", err)
		}
	}
	set(cards[0].ID, testNow.Add(3*time.Hour), "review")
	set(cards[1].ID, testNow.Add(time.Hour), "review")
	// Отложенная ближе всех, но её не выдают — значит и в сводке её нет.
	set(cards[2].ID, testNow.Add(time.Minute), "suspended")

	next, ok, err := repo.NextDue(ctx, f.course.ID)
	if err != nil {
		t.Fatalf("NextDue() вернул ошибку: %v", err)
	}
	if !ok {
		t.Fatal("ближайшее повторение не найдено")
	}
	if !next.Equal(testNow.Add(time.Hour)) {
		t.Errorf("ближайшее повторение = %v, ожидалось через час", next)
	}
}
