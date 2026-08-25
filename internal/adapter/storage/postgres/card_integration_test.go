//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"lexi-bot/internal/adapter/storage/postgres"
	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/test/pgtest"
)

var testNow = time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)

// testDay — календарные сутки пользователя для дневных счётчиков.
var testDay = time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)

// courseFixture — курс со словами в колоде: то, без чего карточек не бывает.
type courseFixture struct {
	user    user.User
	deck    lexicon.DeckID
	course  study.Course
	lexemes []lexicon.Lexeme
}

// newCourse собирает пользователя, колоду с words словами и курс по ней.
func newCourse(t *testing.T, pool *pgxpool.Pool, words int) courseFixture {
	t.Helper()

	ctx := context.Background()
	owner := ensureUser(t, pool, 777)
	deck := builtinDeck(t, pool, "ko-top-2000", langKO)

	terms := []string{"집", "개", "사람", "물", "불", "산", "강", "하늘", "땅", "밥"}
	if words > len(terms) {
		t.Fatalf("тест просит %d слов, в наборе есть %d", words, len(terms))
	}

	lexemes := make([]lexicon.Lexeme, 0, words)
	for i := 0; i < words; i++ {
		lexemes = append(lexemes, newLexeme(t, terms[i], func(p *lexicon.LexemeParams) { p.FreqRank = i + 1 }))
	}
	saved := saveLexemes(t, pool, lexemes...)

	items := make([]lexicon.DeckItem, 0, len(saved))
	for i, lex := range saved {
		items = append(items, lexicon.DeckItem{DeckID: deck, LexemeID: lex.ID, Position: i})
	}
	if err := postgres.NewDeckRepo(pool).AddItems(ctx, items); err != nil {
		t.Fatalf("AddItems() вернул ошибку: %v", err)
	}

	wanted, err := study.NewCourse(int64(owner.ID), deck, langRU)
	if err != nil {
		t.Fatalf("NewCourse() вернул ошибку: %v", err)
	}
	course, err := postgres.NewCourseRepo(pool).Ensure(ctx, wanted)
	if err != nil {
		t.Fatalf("Ensure() вернул ошибку: %v", err)
	}

	return courseFixture{user: owner, deck: deck, course: course, lexemes: saved}
}

func counters(t *testing.T, pool *pgxpool.Pool, courseID study.CourseID) (newIntroduced, reviewsDone int) {
	t.Helper()

	err := pool.QueryRow(context.Background(),
		"SELECT new_introduced, reviews_done FROM daily_counters WHERE user_course_id = $1 AND day = $2",
		int64(courseID), testDay).Scan(&newIntroduced, &reviewsDone)
	if err != nil {
		t.Fatalf("чтение дневных счётчиков не прошло: %v", err)
	}
	return newIntroduced, reviewsDone
}

func TestIntroduceNewRespectsDeckOrder(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewCardRepo(pool)

	f := newCourse(t, pool, 5)

	cards, err := repo.IntroduceNew(ctx, port.IntroduceQuery{
		CourseID: f.course.ID, Now: testNow, Day: testDay, Limit: 3,
	})
	if err != nil {
		t.Fatalf("IntroduceNew() вернул ошибку: %v", err)
	}
	if len(cards) != 3 {
		t.Fatalf("введено %d слов, ожидалось три", len(cards))
	}

	// Слова берутся по порядку колоды — у встроенной это частотность.
	for i, card := range cards {
		if card.LexemeID != f.lexemes[i].ID {
			t.Errorf("позиция %d: слово %d, ожидалось %d", i, card.LexemeID, f.lexemes[i].ID)
		}
		if card.State != study.StateNew {
			t.Errorf("позиция %d: фаза %v, ожидалась new", i, card.State)
		}
		if !card.DueAt.Equal(testNow) {
			t.Errorf("позиция %d: DueAt = %v, новую карточку показываем сразу", i, card.DueAt)
		}
		if card.EaseFactor != study.DefaultEaseFactor {
			t.Errorf("позиция %d: EaseFactor = %v", i, card.EaseFactor)
		}
		if err := card.Validate(); err != nil {
			t.Errorf("позиция %d: карточка не проходит валидацию: %v", i, err)
		}
	}

	if introduced, _ := counters(t, pool, f.course.ID); introduced != 3 {
		t.Errorf("счётчик новых = %d, ожидалось три", introduced)
	}
}

func TestIntroduceNewHonoursDailyLimit(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewCardRepo(pool)

	f := newCourse(t, pool, 8)
	query := port.IntroduceQuery{CourseID: f.course.ID, Now: testNow, Day: testDay, Limit: 3}

	if _, err := repo.IntroduceNew(ctx, query); err != nil {
		t.Fatalf("IntroduceNew() вернул ошибку: %v", err)
	}

	// Лимит на сутки уже выбран: второй заход не даёт ничего, хотя слова
	// в колоде остались.
	more, err := repo.IntroduceNew(ctx, query)
	if err != nil {
		t.Fatalf("повторный IntroduceNew() вернул ошибку: %v", err)
	}
	if len(more) != 0 {
		t.Errorf("введено ещё %d слов, ожидался ноль: дневной лимит выбран", len(more))
	}

	// Назавтра лимит начинается заново — счётчик привязан к дате.
	tomorrow := testDay.AddDate(0, 0, 1)
	next, err := repo.IntroduceNew(ctx, port.IntroduceQuery{
		CourseID: f.course.ID, Now: testNow.AddDate(0, 0, 1), Day: tomorrow, Limit: 3,
	})
	if err != nil {
		t.Fatalf("IntroduceNew() на следующий день вернул ошибку: %v", err)
	}
	if len(next) != 3 {
		t.Errorf("назавтра введено %d слов, ожидалось три", len(next))
	}
	if next[0].LexemeID != f.lexemes[3].ID {
		t.Errorf("назавтра выданы слова с начала колоды: %d", next[0].LexemeID)
	}
}

func TestIntroduceNewStopsWhenDeckIsExhausted(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewCardRepo(pool)

	f := newCourse(t, pool, 2)

	cards, err := repo.IntroduceNew(ctx, port.IntroduceQuery{
		CourseID: f.course.ID, Now: testNow, Day: testDay, Limit: 10,
	})
	if err != nil {
		t.Fatalf("IntroduceNew() вернул ошибку: %v", err)
	}
	if len(cards) != 2 {
		t.Fatalf("введено %d слов, ожидалось два: в колоде больше нет", len(cards))
	}

	// Счётчик увеличился ровно на введённое, а не на запрошенное.
	if introduced, _ := counters(t, pool, f.course.ID); introduced != 2 {
		t.Errorf("счётчик новых = %d, ожидалось два", introduced)
	}
}

func TestIntroduceNewIsSafeUnderConcurrency(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewCardRepo(pool)

	f := newCourse(t, pool, 10)
	const limit = 4

	// Два одновременных нажатия «учить» — самый обычный случай при
	// двойном тапе по кнопке. Дневной лимит не должен пробиваться.
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		total int
		errs  []error
	)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			cards, err := repo.IntroduceNew(ctx, port.IntroduceQuery{
				CourseID: f.course.ID, Now: testNow, Day: testDay, Limit: limit,
			})

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			total += len(cards)
		}()
	}
	wg.Wait()

	for _, err := range errs {
		t.Errorf("IntroduceNew() вернул ошибку: %v", err)
	}
	if total != limit {
		t.Errorf("введено %d слов, ожидалось ровно %d", total, limit)
	}

	var cards int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM cards").Scan(&cards); err != nil {
		t.Fatalf("подсчёт карточек не прошёл: %v", err)
	}
	if cards != limit {
		t.Errorf("карточек в базе %d, ожидалось %d", cards, limit)
	}

	if introduced, _ := counters(t, pool, f.course.ID); introduced != limit {
		t.Errorf("счётчик новых = %d, ожидалось %d", introduced, limit)
	}
}

func TestDueReturnsOnlyRipeCards(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewCardRepo(pool)

	f := newCourse(t, pool, 4)
	cards, err := repo.IntroduceNew(ctx, port.IntroduceQuery{
		CourseID: f.course.ID, Now: testNow, Day: testDay, Limit: 4,
	})
	if err != nil {
		t.Fatalf("IntroduceNew() вернул ошибку: %v", err)
	}

	// Раскладываем карточки по срокам: просрочена, ровно сейчас, завтра,
	// и одна отложена.
	set := func(id study.CardID, due time.Time, state string) {
		t.Helper()
		_, err := pool.Exec(ctx, "UPDATE cards SET due_at = $2, state = $3 WHERE id = $1",
			int64(id), due, state)
		if err != nil {
			t.Fatalf("обновление карточки не прошло: %v", err)
		}
	}
	set(cards[0].ID, testNow.Add(-2*time.Hour), "review")
	set(cards[1].ID, testNow, "learning")
	set(cards[2].ID, testNow.Add(time.Hour), "review")
	set(cards[3].ID, testNow.Add(-time.Hour), "suspended")

	got, err := repo.Due(ctx, port.DueQuery{CourseID: f.course.ID, Now: testNow, Limit: 10})
	if err != nil {
		t.Fatalf("Due() вернул ошибку: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("к повторению %d карточек, ожидалось две", len(got))
	}
	// Порядок — по возрастанию срока: сначала то, что ждёт дольше всех.
	if got[0].ID != cards[0].ID || got[1].ID != cards[1].ID {
		t.Errorf("порядок выдачи = %d, %d; ожидался %d, %d",
			got[0].ID, got[1].ID, cards[0].ID, cards[1].ID)
	}

	limited, err := repo.Due(ctx, port.DueQuery{CourseID: f.course.ID, Now: testNow, Limit: 1})
	if err != nil {
		t.Fatalf("Due() вернул ошибку: %v", err)
	}
	if len(limited) != 1 || limited[0].ID != cards[0].ID {
		t.Errorf("с лимитом 1 получено %+v", limited)
	}

	if got, err := repo.Due(ctx, port.DueQuery{CourseID: f.course.ID, Now: testNow, Limit: 0}); err != nil || got != nil {
		t.Errorf("Due() с нулевым лимитом = %v, %v; ожидался пустой ответ", got, err)
	}
}

func TestApplyWritesEverythingAtOnce(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewCardRepo(pool)

	f := newCourse(t, pool, 1)
	cards, err := repo.IntroduceNew(ctx, port.IntroduceQuery{
		CourseID: f.course.ID, Now: testNow, Day: testDay, Limit: 1,
	})
	if err != nil {
		t.Fatalf("IntroduceNew() вернул ошибку: %v", err)
	}
	card := cards[0]

	next := study.CardState{
		State:        study.StateReview,
		DueAt:        testNow.AddDate(0, 0, 1),
		IntervalDays: 1,
		EaseFactor:   2.5,
		Repetitions:  1,
	}
	review, err := study.NewReview(study.ReviewParams{
		CardID:    card.ID,
		RatedAt:   testNow,
		Rating:    study.RatingGood,
		Mode:      study.ModeChoice,
		IsCorrect: true,
		Duration:  1500 * time.Millisecond,
		Prev:      card.CardState,
		Next:      next,
	})
	if err != nil {
		t.Fatalf("NewReview() вернул ошибку: %v", err)
	}

	err = repo.Apply(ctx, &port.ReviewOutcome{
		CardID: card.ID, State: next, Review: review, UserID: f.user.ID, Day: testDay,
	})
	if err != nil {
		t.Fatalf("Apply() вернул ошибку: %v", err)
	}

	saved, err := repo.ByID(ctx, card.ID)
	if err != nil {
		t.Fatalf("ByID() вернул ошибку: %v", err)
	}
	if saved.State != study.StateReview || saved.IntervalDays != 1 || saved.Repetitions != 1 {
		t.Errorf("состояние карточки не сохранилось: %+v", saved)
	}
	if !saved.DueAt.Equal(next.DueAt) {
		t.Errorf("DueAt = %v, ожидалось %v", saved.DueAt, next.DueAt)
	}
	if saved.IsNew() {
		t.Error("после ответа карточка перестаёт быть новой")
	}

	var (
		reviews  int
		rating   string
		duration int
	)
	err = pool.QueryRow(ctx,
		"SELECT count(*), max(rating), max(duration_ms) FROM reviews WHERE card_id = $1",
		int64(card.ID)).Scan(&reviews, &rating, &duration)
	if err != nil {
		t.Fatalf("чтение журнала не прошло: %v", err)
	}
	if reviews != 1 || rating != "good" || duration != 1500 {
		t.Errorf("в журнале %d записей (%q, %d мс)", reviews, rating, duration)
	}

	if _, done := counters(t, pool, f.course.ID); done != 1 {
		t.Errorf("счётчик повторений = %d, ожидалась единица", done)
	}
}

func TestApplyRejectsBrokenOutcome(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewCardRepo(pool)

	f := newCourse(t, pool, 1)
	cards, err := repo.IntroduceNew(ctx, port.IntroduceQuery{
		CourseID: f.course.ID, Now: testNow, Day: testDay, Limit: 1,
	})
	if err != nil {
		t.Fatalf("IntroduceNew() вернул ошибку: %v", err)
	}
	card := cards[0]

	next := study.CardState{State: study.StateReview, DueAt: testNow, IntervalDays: 1, EaseFactor: 2.5}
	review, err := study.NewReview(study.ReviewParams{
		CardID: card.ID, RatedAt: testNow, Rating: study.RatingGood,
		Mode: study.ModeChoice, IsCorrect: true, Prev: card.CardState, Next: next,
	})
	if err != nil {
		t.Fatalf("NewReview() вернул ошибку: %v", err)
	}

	// Запись журнала не о той карточке — это ошибка в сценарии, и она
	// не должна дойти до базы.
	err = repo.Apply(ctx, &port.ReviewOutcome{
		CardID: card.ID + 1, State: next, Review: review, UserID: f.user.ID, Day: testDay,
	})
	if !errors.Is(err, port.ErrInvalidData) {
		t.Errorf("Apply() = %v, ожидалась ErrInvalidData", err)
	}

	// Несуществующая карточка — ErrNotFound, и журнал остаётся пустым.
	missing := review
	missing.CardID = 99999
	err = repo.Apply(ctx, &port.ReviewOutcome{
		CardID: 99999, State: next, Review: missing, UserID: f.user.ID, Day: testDay,
	})
	if !errors.Is(err, port.ErrNotFound) {
		t.Errorf("Apply() = %v, ожидалась ErrNotFound", err)
	}

	var reviews int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM reviews").Scan(&reviews); err != nil {
		t.Fatalf("подсчёт журнала не прошёл: %v", err)
	}
	if reviews != 0 {
		t.Errorf("в журнале %d записей, ожидался пустой журнал", reviews)
	}
}

func TestCountsByState(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewCardRepo(pool)

	f := newCourse(t, pool, 4)
	cards, err := repo.IntroduceNew(ctx, port.IntroduceQuery{
		CourseID: f.course.ID, Now: testNow, Day: testDay, Limit: 4,
	})
	if err != nil {
		t.Fatalf("IntroduceNew() вернул ошибку: %v", err)
	}

	for i, state := range []string{"review", "review", "learning"} {
		if _, err := pool.Exec(ctx, "UPDATE cards SET state = $2 WHERE id = $1", int64(cards[i].ID), state); err != nil {
			t.Fatalf("обновление карточки не прошло: %v", err)
		}
	}

	counts, err := repo.CountsByState(ctx, f.course.ID)
	if err != nil {
		t.Fatalf("CountsByState() вернул ошибку: %v", err)
	}
	if counts[study.StateReview] != 2 {
		t.Errorf("на повторении %d, ожидалось две", counts[study.StateReview])
	}
	if counts[study.StateLearning] != 1 {
		t.Errorf("в обучении %d, ожидалась одна", counts[study.StateLearning])
	}
	if counts[study.StateNew] != 1 {
		t.Errorf("новых %d, ожидалась одна", counts[study.StateNew])
	}
	// Фазы без карточек в ответе не появляются: ноль и отсутствие для
	// статистики одно и то же.
	if _, ok := counts[study.StateSuspended]; ok {
		t.Error("в ответе появилась фаза без карточек")
	}
}

func TestApplyRejectsStaleVersion(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewCardRepo(pool)

	f := newCourse(t, pool, 1)
	cards, err := repo.IntroduceNew(ctx, port.IntroduceQuery{
		CourseID: f.course.ID, Now: testNow, Day: testDay, Limit: 1,
	})
	if err != nil {
		t.Fatalf("IntroduceNew() вернул ошибку: %v", err)
	}
	card := cards[0]

	outcome := func(at time.Time, expected time.Time) *port.ReviewOutcome {
		t.Helper()

		next := study.CardState{
			State: study.StateReview, DueAt: at.AddDate(0, 0, 1),
			IntervalDays: 1, EaseFactor: 2.5, Repetitions: 1,
		}
		review, err := study.NewReview(study.ReviewParams{
			CardID: card.ID, RatedAt: at, Rating: study.RatingGood,
			Mode: study.ModeChoice, IsCorrect: true, Prev: card.CardState, Next: next,
		})
		if err != nil {
			t.Fatalf("NewReview() вернул ошибку: %v", err)
		}
		return &port.ReviewOutcome{
			CardID: card.ID, State: next, Review: review,
			UserID: f.user.ID, Day: testDay, ExpectedLastReviewedAt: expected,
		}
	}

	// Первый ответ: карточку ещё не отвечали, версия нулевая.
	if err := repo.Apply(ctx, outcome(testNow, time.Time{})); err != nil {
		t.Fatalf("Apply() вернул ошибку: %v", err)
	}

	// Второе нажатие той же кнопки: версия та же, а карточка уже уехала.
	// Условие по last_reviewed_at не даёт применить ответ дважды.
	err = repo.Apply(ctx, outcome(testNow.Add(time.Second), time.Time{}))
	if !errors.Is(err, port.ErrConflict) {
		t.Errorf("повторный Apply() = %v, ожидалась ErrConflict", err)
	}

	var reviews int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM reviews").Scan(&reviews); err != nil {
		t.Fatalf("подсчёт журнала не прошёл: %v", err)
	}
	if reviews != 1 {
		t.Errorf("записей в журнале %d, ожидалась одна", reviews)
	}
	if _, done := counters(t, pool, f.course.ID); done != 1 {
		t.Errorf("счётчик повторений = %d, ожидалась единица", done)
	}

	// А ответ на актуальную версию проходит.
	saved, err := repo.ByID(ctx, card.ID)
	if err != nil {
		t.Fatalf("ByID() вернул ошибку: %v", err)
	}
	if err := repo.Apply(ctx, outcome(testNow.Add(time.Minute), saved.LastReviewedAt)); err != nil {
		t.Errorf("Apply() с актуальной версией = %v", err)
	}
}
