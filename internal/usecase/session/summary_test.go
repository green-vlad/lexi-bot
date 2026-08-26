package session_test

import (
	"context"
	"testing"
	"time"

	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// fakeReviews считает точность по тому, что записали.
type fakeReviews struct {
	total   int
	correct int
	queries []port.StatsQuery
}

func (f *fakeReviews) Add(context.Context, user.ID, *study.Review) error { return nil }

func (f *fakeReviews) Stats(_ context.Context, q port.StatsQuery) (port.ReviewStats, error) {
	f.queries = append(f.queries, q)
	return port.ReviewStats{Total: f.total, Correct: f.correct}, nil
}

func (f *fakeReviews) ActiveDays(context.Context, user.ID, user.Timezone, time.Time) ([]time.Time, error) {
	return nil, nil
}

func TestSummaryCountsToday(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 5)
	reviews := &fakeReviews{total: 6, correct: 5}
	f.reviews.inner = reviews

	// Три ответа, из них два новых слова.
	day := f.settings.settings.DayStart(f.now)
	f.counters.addNew(courseID, day, 2)
	for i := 0; i < 3; i++ {
		if err := f.counters.AddReview(context.Background(), courseID, day); err != nil {
			t.Fatalf("AddReview() вернул ошибку: %v", err)
		}
	}

	summary, err := f.service.Summary(context.Background(), courseID)
	if err != nil {
		t.Fatalf("Summary() вернул ошибку: %v", err)
	}

	if summary.Reviewed != 3 {
		t.Errorf("повторено %d, ожидалось три", summary.Reviewed)
	}
	if summary.New != 2 {
		t.Errorf("новых %d, ожидалось два", summary.New)
	}
	if got := summary.Accuracy; got < 0.83 || got > 0.84 {
		t.Errorf("точность = %v, ожидалось около 0.833", got)
	}

	// Точность считается по этому курсу и с начала суток пользователя,
	// а не по всем ответам за всё время.
	if len(reviews.queries) != 1 {
		t.Fatalf("запросов к журналу %d, ожидался один", len(reviews.queries))
	}
	q := reviews.queries[0]
	if q.CourseID != courseID {
		t.Errorf("сводка спросила журнал по курсу %d", q.CourseID)
	}
	if !q.Since.Equal(day) {
		t.Errorf("период с %v, ожидалось начало суток пользователя %v", q.Since, day)
	}
}

func TestSummaryNextReview(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 5)

	// Карточка ждёт через два часа, отложенная — через минуту: она не в счёт.
	f.cards.cards = []study.Card{
		{
			ID: 1, CourseID: courseID, LexemeID: 1, IntroducedAt: f.now,
			CardState: study.CardState{
				State: study.StateReview, DueAt: f.now.Add(2 * time.Hour),
				IntervalDays: 1, EaseFactor: 2.5, Repetitions: 1,
			},
		},
		{
			ID: 2, CourseID: courseID, LexemeID: 2, IntroducedAt: f.now,
			CardState: study.CardState{
				State: study.StateSuspended, DueAt: f.now.Add(time.Minute),
				IntervalDays: 1, EaseFactor: 2.5,
			},
		},
	}

	summary, err := f.service.Summary(context.Background(), courseID)
	if err != nil {
		t.Fatalf("Summary() вернул ошибку: %v", err)
	}
	if !summary.HasNext {
		t.Fatal("ближайшее повторение не найдено")
	}
	if !summary.NextReview.Equal(f.now.Add(2 * time.Hour)) {
		t.Errorf("ближайшее повторение = %v", summary.NextReview)
	}
}

func TestSummaryWithoutCards(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 5)
	// Карточек нет вовсе: курс только что заведён, слова ещё не начинали.
	f.cards.cards = nil

	// Повторять нечего, и сводка не должна выдумывать срок.
	summary, err := f.service.Summary(context.Background(), courseID)
	if err != nil {
		t.Fatalf("Summary() вернул ошибку: %v", err)
	}
	if summary.HasNext {
		t.Errorf("сводка нашла повторение там, где карточек нет: %v", summary.NextReview)
	}
	if summary.Reviewed != 0 || summary.New != 0 {
		t.Errorf("сводка = %+v, ожидались нули", summary)
	}
	if summary.Accuracy != 0 {
		t.Errorf("точность = %v, ожидался ноль", summary.Accuracy)
	}
}
