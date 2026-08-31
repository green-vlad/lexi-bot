package stats_test

import (
	"context"
	"time"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

type fakeCards struct {
	counts   map[study.CourseID]map[study.State]int
	due      map[study.CourseID][]time.Time
	failWith error
}

func (f *fakeCards) CountsByState(_ context.Context, courseID study.CourseID) (map[study.State]int, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	return f.counts[courseID], nil
}

// DueBefore отдаёт только те сроки, что попадают в окно: так же ведёт себя
// запрос, и заглушка, отдающая всё подряд, скрыла бы ошибку в границах.
func (f *fakeCards) DueBefore(_ context.Context, courseID study.CourseID, until time.Time) ([]time.Time, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	var out []time.Time
	for _, at := range f.due[courseID] {
		if at.Before(until) {
			out = append(out, at)
		}
	}
	return out, nil
}

// Остальное сводке не нужно: она ничего не спрашивает и не записывает.
func (f *fakeCards) Due(context.Context, port.DueQuery) ([]study.Card, error) { return nil, nil }
func (f *fakeCards) CountDue(context.Context, study.CourseID, time.Time) (int, error) {
	return 0, nil
}
func (f *fakeCards) NewWords(context.Context, port.NewWordQuery) ([]lexicon.LexemeID, error) {
	return nil, nil
}
func (f *fakeCards) StartLearning(context.Context, *port.StartLearningQuery) (study.Card, bool, error) {
	return study.Card{}, false, nil
}
func (f *fakeCards) MarkKnown(context.Context, study.CourseID, lexicon.LexemeID, time.Time) error {
	return nil
}
func (f *fakeCards) PostponeNew(context.Context, study.CourseID, lexicon.LexemeID, time.Time, time.Time) error {
	return nil
}
func (f *fakeCards) Apply(context.Context, *port.ReviewOutcome) error { return nil }
func (f *fakeCards) ByID(context.Context, study.CardID) (study.Card, error) {
	return study.Card{}, port.ErrNotFound
}
func (f *fakeCards) NextDue(context.Context, study.CourseID) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

type fakeCourses struct{ courses []study.Course }

func (f *fakeCourses) ByUser(context.Context, user.ID) ([]study.Course, error) {
	return f.courses, nil
}
func (f *fakeCourses) Ensure(_ context.Context, c study.Course) (study.Course, error) { return c, nil }
func (f *fakeCourses) ByID(_ context.Context, id study.CourseID) (study.Course, error) {
	for _, course := range f.courses {
		if course.ID == id {
			return course, nil
		}
	}
	return study.Course{}, port.ErrNotFound
}
func (f *fakeCourses) SetStatus(context.Context, study.CourseID, study.CourseStatus) error {
	return nil
}

type fakeDecks struct{ sizes map[lexicon.DeckID]int }

func (f *fakeDecks) ByID(_ context.Context, id lexicon.DeckID) (lexicon.Deck, error) {
	return lexicon.Deck{ID: id, Size: f.sizes[id]}, nil
}
func (f *fakeDecks) Distractors(context.Context, port.DistractorQuery) ([]lexicon.Translation, error) {
	return nil, nil
}
func (f *fakeDecks) DistractorTerms(context.Context, port.DistractorQuery) ([]lexicon.Lexeme, error) {
	return nil, nil
}
func (f *fakeDecks) Languages(context.Context) ([]lexicon.Language, error) { return nil, nil }
func (f *fakeDecks) TranslationLanguages(context.Context, lexicon.DeckID) ([]lexicon.Language, error) {
	return nil, nil
}
func (f *fakeDecks) Builtin(context.Context, lexicon.Language) ([]lexicon.Deck, error) {
	return nil, nil
}
func (f *fakeDecks) ByCode(context.Context, string) (lexicon.Deck, error) {
	return lexicon.Deck{}, port.ErrNotFound
}
func (f *fakeDecks) EnsureBuiltin(_ context.Context, deck *lexicon.Deck) (lexicon.Deck, error) {
	return *deck, nil
}
func (f *fakeDecks) EnsurePersonal(context.Context, int64, lexicon.Language, string) (lexicon.Deck, error) {
	return lexicon.Deck{}, nil
}
func (f *fakeDecks) AddItems(context.Context, []lexicon.DeckItem) error { return nil }
func (f *fakeDecks) Contains(context.Context, lexicon.DeckID, lexicon.LexemeID) (bool, error) {
	return false, nil
}
func (f *fakeDecks) Items(context.Context, lexicon.DeckID, int, int) ([]lexicon.DeckItem, error) {
	return nil, nil
}

// fakeReviews отдаёт заранее заданные числа. Сводка спрашивает точность
// за два разных периода, и заглушка обязана их различать: иначе тест
// на «неделя и месяц считаются порознь» ничего бы не проверял.
type fakeReviews struct {
	byDays map[int]port.ReviewStats
	days   []time.Time
	now    time.Time
}

// Stats различает запросы по длине периода: сводка спрашивает точность
// за неделю и за месяц, и заглушка, отвечающая одним и тем же, сделала бы
// проверку «периоды считаются порознь» бессмысленной.
func (f *fakeReviews) Stats(_ context.Context, q port.StatsQuery) (port.ReviewStats, error) {
	days := int(f.now.Sub(q.Since).Hours()/24 + 0.5)
	return f.byDays[days], nil
}

func (f *fakeReviews) ActiveDays(context.Context, user.ID, user.Timezone, time.Time) ([]time.Time, error) {
	return f.days, nil
}

func (f *fakeReviews) Add(context.Context, user.ID, *study.Review) error { return nil }

type fakeSettings struct{ settings user.Settings }

func (f *fakeSettings) Get(context.Context, user.ID) (user.Settings, error) { return f.settings, nil }
func (f *fakeSettings) Save(context.Context, user.ID, user.Settings) error  { return nil }

type fakeUsers struct{}

func (f *fakeUsers) Ensure(_ context.Context, u *user.User) (user.User, bool, error) {
	return *u, false, nil
}
func (f *fakeUsers) ByTelegramID(context.Context, user.TelegramID) (user.User, error) {
	return user.User{}, port.ErrNotFound
}
func (f *fakeUsers) ByID(_ context.Context, id user.ID) (user.User, error) {
	return user.User{ID: id}, nil
}
func (f *fakeUsers) SetUILang(context.Context, user.ID, user.UILang) error           { return nil }
func (f *fakeUsers) SetCurrentCourse(context.Context, user.ID, study.CourseID) error { return nil }
func (f *fakeUsers) SoftDelete(context.Context, user.ID, time.Time) error            { return nil }
func (f *fakeUsers) Purge(context.Context, user.ID) error                            { return nil }
