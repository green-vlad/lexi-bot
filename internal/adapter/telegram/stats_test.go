package telegram_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"lexi-bot/internal/adapter/telegram"
	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/internal/usecase/stats"
)

type statsFixture struct {
	router    *telegram.Router
	messenger *fakeMessenger
	cards     *stubCards
	courses   *stubCourses
	reviews   *stubReviews
	settings  *stubSettings
	users     *fakeUsers
	now       time.Time
}

func newStatsFixture(t *testing.T) *statsFixture {
	t.Helper()

	f := &statsFixture{
		messenger: &fakeMessenger{},
		courses:   newStubCourses(),
		reviews:   &stubReviews{},
		settings:  newStubSettings(),
		users:     newFakeUsers(),
		now:       time.Date(2026, 8, 31, 3, 0, 0, 0, time.UTC),
	}

	owner := mustUser(t, 555, user.UILangRU)
	saved, _, err := f.users.Ensure(context.Background(), &owner)
	if err != nil {
		t.Fatalf("Ensure() вернул ошибку: %v", err)
	}
	f.settings.byUser[saved.ID] = user.DefaultSettings(user.MustParseTimezone("Asia/Seoul"))

	course, err := f.courses.Ensure(context.Background(), study.Course{
		UserID: int64(saved.ID), DeckID: 1, TranslationLang: langRU, Status: study.CourseActive,
	})
	if err != nil {
		t.Fatalf("Ensure() вернул ошибку: %v", err)
	}
	f.cards = newStubCards(nil, course.ID)

	decks := newStubDecks()
	service, err := stats.New(&stats.Deps{
		Users: f.users, Courses: f.courses, Decks: decks, Cards: f.cards,
		Reviews: f.reviews, Settings: f.settings,
		Clock: port.ClockFunc(func() time.Time { return f.now }),
	})
	if err != nil {
		t.Fatalf("stats.New() вернул ошибку: %v", err)
	}

	handler, err := telegram.NewStats(service, f.messenger)
	if err != nil {
		t.Fatalf("NewStats() вернул ошибку: %v", err)
	}

	f.router = telegram.NewRouter()
	f.router.Use(
		telegram.Identify(f.users, quietLogger()),
		telegram.Localize(testCatalog(t)),
	)
	handler.Register(f.router)
	return f
}

func (f *statsFixture) ask(t *testing.T) string {
	t.Helper()

	if err := f.router.Handle(context.Background(), message("/stats")); err != nil {
		t.Fatalf("Handle() вернул ошибку: %v", err)
	}
	return f.messenger.last(t).Text
}

// card кладёт в стенд карточку в заданной фазе и с заданным сроком.
func (f *statsFixture) card(state study.State, due time.Time) {
	f.cards.nextID++
	f.cards.cards = append(f.cards.cards, study.Card{
		ID:        f.cards.nextID,
		CourseID:  f.cards.courseID,
		LexemeID:  lexicon.LexemeID(f.cards.nextID),
		CardState: study.CardState{State: state, DueAt: due, EaseFactor: study.DefaultEaseFactor},
	})
}

func TestStatsShowsProgress(t *testing.T) {
	t.Parallel()

	f := newStatsFixture(t)
	for i := 0; i < 3; i++ {
		f.card(study.StateReview, f.now.Add(time.Hour))
	}
	f.card(study.StateLearning, f.now.Add(time.Minute))
	f.card(study.StateKnown, f.now)

	text := f.ask(t)

	if !strings.Contains(text, "Выучено: 3") {
		t.Errorf("сводка = %q", text)
	}
	if !strings.Contains(text, "В работе: 1") {
		t.Errorf("сводка = %q", text)
	}
	// Колода на 2000 слов, пройдено пять — впереди остальное.
	if !strings.Contains(text, "Впереди: 1995") {
		t.Errorf("сводка = %q", text)
	}
	if !strings.Contains(text, "уже знаком") {
		t.Errorf("сводка = %q, ожидалась строка про знакомые слова", text)
	}
}

func TestStatsOmitsWhatIsNotThere(t *testing.T) {
	t.Parallel()

	f := newStatsFixture(t)
	f.card(study.StateReview, f.now.Add(365*24*time.Hour))

	text := f.ask(t)

	// «Серия 0 дней» и «точность 0%» у того, кто ещё не отвечал, —
	// не факты, а упрёки.
	for _, unwanted := range []string{"подряд", "Точность", "Повторений по дням"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("сводка = %q, в ней лишнее %q", text, unwanted)
		}
	}
}

func TestStatsShowsAccuracyAndStreak(t *testing.T) {
	t.Parallel()

	f := newStatsFixture(t)
	f.card(study.StateReview, f.now.Add(time.Hour))
	f.reviews.total, f.reviews.correct = 20, 15
	f.reviews.days = []time.Time{
		f.settings.byUser[1].DayStart(f.now),
		f.settings.byUser[1].DayStart(f.now.AddDate(0, 0, -1)),
	}

	text := f.ask(t)

	if !strings.Contains(text, "75%") {
		t.Errorf("сводка = %q, ожидалась точность", text)
	}
	if !strings.Contains(text, "2 дня подряд") {
		t.Errorf("сводка = %q, ожидалась серия", text)
	}
}

func TestStatsShowsForecast(t *testing.T) {
	t.Parallel()

	f := newStatsFixture(t)
	settings := f.settings.byUser[1]
	today := settings.DayStart(f.now)

	f.card(study.StateReview, today.Add(-2*time.Hour)) // просрочено
	f.card(study.StateReview, today.Add(5*time.Hour))
	f.card(study.StateReview, settings.NextDayStart(today).Add(time.Hour))

	text := f.ask(t)

	if !strings.Contains(text, "Повторений по дням") {
		t.Fatalf("сводка = %q", text)
	}
	// Просроченное и сегодняшнее в одном дне: обе карточки ждут сейчас.
	if !strings.Contains(text, "2 · 1 · 0") {
		t.Errorf("прогноз = %q", text)
	}
	if !strings.Contains(text, "неделю — 3") {
		t.Errorf("сводка = %q, ожидался итог за неделю", text)
	}
}

func TestStatsWithoutCourse(t *testing.T) {
	t.Parallel()

	f := newStatsFixture(t)
	f.courses.byID = map[study.CourseID]study.Course{}

	if text := f.ask(t); !strings.Contains(text, "/start") {
		t.Errorf("ответ = %q, ожидалась подсказка про /start", text)
	}
}

func TestStatsNeedsDependencies(t *testing.T) {
	t.Parallel()

	if _, err := telegram.NewStats(nil, nil); err == nil {
		t.Error("хендлер без зависимостей должен быть ошибкой")
	}
}
