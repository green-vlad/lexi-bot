package stats_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/internal/usecase/stats"
)

const owner = user.ID(42)

var langRU = lexicon.MustParseLanguage("ru")

type fixture struct {
	service  *stats.Service
	cards    *fakeCards
	courses  *fakeCourses
	decks    *fakeDecks
	reviews  *fakeReviews
	settings *fakeSettings
	now      time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	f := &fixture{
		cards: &fakeCards{counts: map[study.CourseID]map[study.State]int{}, due: map[study.CourseID][]time.Time{}},
		decks: &fakeDecks{sizes: map[lexicon.DeckID]int{1: 100}},

		// Полдень в Сеуле: граница суток пользователя не совпадает с UTC,
		// и подмена одной другой сразу видна.
		now: time.Date(2026, 8, 31, 3, 0, 0, 0, time.UTC),
	}
	f.reviews = &fakeReviews{now: f.now}
	f.settings = &fakeSettings{settings: user.DefaultSettings(user.MustParseTimezone("Asia/Seoul"))}
	f.courses = &fakeCourses{courses: []study.Course{
		{ID: 1, UserID: int64(owner), DeckID: 1, TranslationLang: langRU, Status: study.CourseActive},
	}}

	service, err := stats.New(&stats.Deps{
		Users: &fakeUsers{}, Courses: f.courses, Decks: f.decks, Cards: f.cards,
		Reviews: f.reviews, Settings: f.settings,
		Clock: port.ClockFunc(func() time.Time { return f.now }),
	})
	if err != nil {
		t.Fatalf("stats.New() вернул ошибку: %v", err)
	}
	f.service = service
	return f
}

func (f *fixture) of(t *testing.T) stats.Summary {
	t.Helper()

	summary, err := f.service.Of(context.Background(), owner)
	if err != nil {
		t.Fatalf("Of() вернул ошибку: %v", err)
	}
	return summary
}

// day возвращает момент внутри суток пользователя со сдвигом в n дней.
func (f *fixture) day(n int) time.Time {
	start := f.settings.settings.DayStart(f.now)
	for i := 0; i < n; i++ {
		start = f.settings.settings.NextDayStart(start)
	}
	for i := 0; i > n; i-- {
		start = f.settings.settings.DayStart(start.AddDate(0, 0, -1))
	}
	return start.Add(2 * time.Hour)
}

func TestSummaryCountsCardsByState(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.cards.counts[1] = map[study.State]int{
		study.StateReview:     30,
		study.StateLearning:   4,
		study.StateRelearning: 2,
		study.StateKnown:      10,
		study.StateNew:        3, // отложенные — они ещё вернутся
	}

	summary := f.of(t)

	if summary.Learned != 30 {
		t.Errorf("выучено = %d, ожидалось 30", summary.Learned)
	}
	// Забытые слова считаются вместе с теми, что на шагах обучения:
	// человеку они одинаково «в работе».
	if summary.Learning != 6 {
		t.Errorf("в работе = %d, ожидалось 6", summary.Learning)
	}
	if summary.Known != 10 {
		t.Errorf("«уже знаю» = %d, ожидалось 10", summary.Known)
	}
	// Осталось = размер колоды минус начатое. Отложенные слова начатыми
	// не считаются: они вернутся в знакомство.
	if summary.NewRemaining != 100-46 {
		t.Errorf("осталось = %d, ожидалось %d", summary.NewRemaining, 100-46)
	}
	if summary.Total() != 46 {
		t.Errorf("всего пройдено = %d, ожидалось 46", summary.Total())
	}
}

func TestSummarySkipsPausedCourses(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.courses.courses = append(f.courses.courses, study.Course{
		ID: 2, UserID: int64(owner), DeckID: 1, TranslationLang: langRU, Status: study.CoursePaused,
	})
	f.cards.counts[1] = map[study.State]int{study.StateReview: 10}
	f.cards.counts[2] = map[study.State]int{study.StateReview: 500}

	// Курс на паузе убран из занятий сознательно, и его прогресс —
	// чужие цифры в сводке.
	if got := f.of(t).Learned; got != 10 {
		t.Errorf("выучено = %d, ожидалось 10", got)
	}
}

func TestSummaryWithoutCourses(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.courses.courses = nil

	summary := f.of(t)
	if summary.HasCourses {
		t.Error("курсов нет, а сводка говорит обратное")
	}
	if summary.Total() != 0 || summary.NewRemaining != 0 {
		t.Errorf("сводка = %+v, ожидались нули", summary)
	}
}

func TestForecastBucketsByUserDays(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.cards.due[1] = []time.Time{
		f.day(-2), // просрочено — идёт в сегодняшний день
		f.day(0), f.day(0),
		f.day(1),
		f.day(6),
	}

	forecast := f.of(t).Forecast
	if len(forecast) != stats.ForecastDays {
		t.Fatalf("прогноз на %d дней, ожидалось %d", len(forecast), stats.ForecastDays)
	}
	// Просроченное попадает в сегодня: эти карточки ждут прямо сейчас.
	if forecast[0] != 3 {
		t.Errorf("сегодня = %d, ожидалось 3", forecast[0])
	}
	if forecast[1] != 1 || forecast[6] != 1 {
		t.Errorf("прогноз = %v", forecast)
	}
	if forecast[2] != 0 {
		t.Errorf("прогноз = %v, третий день должен быть пуст", forecast)
	}
}

func TestAccuracyOverTwoPeriods(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.reviews.byDays = map[int]port.ReviewStats{
		stats.WeekDays:  {Total: 20, Correct: 15},
		stats.MonthDays: {Total: 100, Correct: 60},
	}

	summary := f.of(t)
	if summary.Week.Total != 20 || summary.Week.Correct != 15 {
		t.Errorf("неделя = %+v", summary.Week)
	}
	if got := summary.Week.Share(); got != 0.75 {
		t.Errorf("доля за неделю = %v, ожидалось 0.75", got)
	}
	if got := summary.Month.Share(); got != 0.6 {
		t.Errorf("доля за месяц = %v, ожидалось 0.6", got)
	}
}

func TestAccuracyWithoutAnswers(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	summary := f.of(t)
	// Ноль ответов и ноль процентов — разные вещи: показывать «0% верных»
	// тому, кто ещё не отвечал, значит соврать.
	if summary.Week.Known() {
		t.Error("сводка считает, что есть по чему считать точность")
	}
	if summary.Week.Share() != 0 {
		t.Errorf("доля = %v", summary.Week.Share())
	}
}

func TestStreakCountsConsecutiveDays(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		days []int
		want int
	}{
		{"занимался сегодня и два дня до", []int{0, -1, -2}, 3},
		{"сегодня ещё не садился, но вчера и позавчера был", []int{-1, -2}, 2},
		{"пропустил вчера", []int{-2, -3}, 0},
		{"только сегодня", []int{0}, 1},
		{"разрыв в середине", []int{0, -1, -3}, 2},
		{"не занимался вовсе", nil, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			for _, offset := range tt.days {
				f.reviews.days = append(f.reviews.days, f.settings.settings.DayStart(f.day(offset)))
			}

			if got := f.of(t).Streak; got != tt.want {
				t.Errorf("серия = %d, ожидалось %d", got, tt.want)
			}
		})
	}
}

func TestSummaryReportsFailures(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.cards.failWith = errors.New("база недоступна")

	if _, err := f.service.Of(context.Background(), owner); err == nil {
		t.Error("недоступная база должна быть ошибкой")
	}
}

func TestNewNeedsDependencies(t *testing.T) {
	t.Parallel()

	if _, err := stats.New(&stats.Deps{}); err == nil {
		t.Error("сценарий без зависимостей должен быть ошибкой")
	}
}
