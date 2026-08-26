package intro_test

import (
	"context"
	"time"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// fakeCards ведёт себя как настоящий репозиторий: карточки заводятся тем
// решением, которое человек принял, а дневную норму тратит только «запомнил».
type fakeCards struct {
	pool     []lexicon.LexemeID
	cards    []study.Card
	counters *fakeCounters
	nextID   study.CardID
}

func (f *fakeCards) NewWords(_ context.Context, q port.NewWordQuery) ([]lexicon.LexemeID, error) {
	if q.Limit <= 0 {
		return nil, nil
	}

	var out []lexicon.LexemeID
	for _, lexemeID := range f.pool {
		if len(out) >= q.Limit {
			break
		}
		card, ok := f.byLexeme(lexemeID)
		if ok && (card.State != study.StateNew || card.DueAt.After(q.Now)) {
			continue
		}
		out = append(out, lexemeID)
	}
	return out, nil
}

func (f *fakeCards) StartLearning(_ context.Context, q *port.StartLearningQuery) (study.Card, bool, error) {
	if q.Limit <= 0 || f.counters.get(q.Day).NewIntroduced >= q.Limit {
		return study.Card{}, false, nil
	}

	card := f.upsert(q.CourseID, q.LexemeID, q.State, q.Now)
	f.counters.addNew(q.Day, 1)
	return card, true, nil
}

func (f *fakeCards) MarkKnown(_ context.Context, courseID study.CourseID, lexemeID lexicon.LexemeID, now time.Time) error {
	f.upsert(courseID, lexemeID, study.CardState{
		State: study.StateKnown, DueAt: now, EaseFactor: study.DefaultEaseFactor,
	}, now)
	return nil
}

func (f *fakeCards) PostponeNew(_ context.Context, courseID study.CourseID, lexemeID lexicon.LexemeID, now, until time.Time) error {
	f.upsert(courseID, lexemeID, study.CardState{
		State: study.StateNew, DueAt: until, EaseFactor: study.DefaultEaseFactor,
	}, now)
	return nil
}

// upsert заводит карточку или переводит существующую в новое состояние.
// Как и в базе, тронуть можно только ту, которую ещё не начали учить.
func (f *fakeCards) upsert(courseID study.CourseID, lexemeID lexicon.LexemeID, state study.CardState, now time.Time) study.Card {
	for i := range f.cards {
		if f.cards[i].LexemeID != lexemeID || f.cards[i].CourseID != courseID {
			continue
		}
		if f.cards[i].State == study.StateNew {
			f.cards[i].CardState = state
			f.cards[i].IntroducedAt = now
		}
		return f.cards[i]
	}

	f.nextID++
	card := study.Card{
		ID: f.nextID, CourseID: courseID, LexemeID: lexemeID,
		CardState: state, IntroducedAt: now,
	}
	f.cards = append(f.cards, card)
	return card
}

func (f *fakeCards) byLexeme(lexemeID lexicon.LexemeID) (study.Card, bool) {
	for i := range f.cards {
		if f.cards[i].LexemeID == lexemeID {
			return f.cards[i], true
		}
	}
	return study.Card{}, false
}

// Остальное знакомству не нужно: оно не спрашивает и не записывает ответы.
func (f *fakeCards) Due(context.Context, port.DueQuery) ([]study.Card, error) { return nil, nil }
func (f *fakeCards) CountDue(context.Context, study.CourseID, time.Time) (int, error) {
	return 0, nil
}
func (f *fakeCards) Apply(context.Context, *port.ReviewOutcome) error { return nil }
func (f *fakeCards) ByID(_ context.Context, id study.CardID) (study.Card, error) {
	for i := range f.cards {
		if f.cards[i].ID == id {
			return f.cards[i], nil
		}
	}
	return study.Card{}, port.ErrNotFound
}
func (f *fakeCards) CountsByState(context.Context, study.CourseID) (map[study.State]int, error) {
	return nil, nil
}
func (f *fakeCards) NextDue(context.Context, study.CourseID) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

type fakeCounters struct {
	byDay map[string]port.DailyCounter
}

func (f *fakeCounters) get(day time.Time) port.DailyCounter {
	return f.byDay[day.Format(time.DateOnly)]
}

func (f *fakeCounters) addNew(day time.Time, n int) {
	counter := f.byDay[day.Format(time.DateOnly)]
	counter.Day = day
	counter.NewIntroduced += n
	f.byDay[day.Format(time.DateOnly)] = counter
}

func (f *fakeCounters) Get(_ context.Context, _ study.CourseID, day time.Time) (port.DailyCounter, error) {
	counter := f.get(day)
	counter.Day = day
	return counter, nil
}

func (f *fakeCounters) AddReview(_ context.Context, _ study.CourseID, day time.Time) error {
	counter := f.byDay[day.Format(time.DateOnly)]
	counter.Day = day
	counter.ReviewsDone++
	f.byDay[day.Format(time.DateOnly)] = counter
	return nil
}

type fakeCourses struct{ course study.Course }

func (f *fakeCourses) Ensure(_ context.Context, c study.Course) (study.Course, error) { return c, nil }

func (f *fakeCourses) ByID(_ context.Context, id study.CourseID) (study.Course, error) {
	if f.course.ID == id {
		return f.course, nil
	}
	return study.Course{}, port.ErrNotFound
}

func (f *fakeCourses) ByUser(context.Context, user.ID) ([]study.Course, error) { return nil, nil }
func (f *fakeCourses) SetStatus(context.Context, study.CourseID, study.CourseStatus) error {
	return nil
}

type fakeSettings struct{ settings user.Settings }

func (f *fakeSettings) Get(context.Context, user.ID) (user.Settings, error) { return f.settings, nil }
func (f *fakeSettings) Save(context.Context, user.ID, user.Settings) error  { return nil }

type fakeLexemes struct {
	lexemes      map[lexicon.LexemeID]lexicon.Lexeme
	translations map[lexicon.LexemeID][]lexicon.Translation
}

func (f *fakeLexemes) ByIDs(_ context.Context, ids []lexicon.LexemeID) ([]lexicon.Lexeme, error) {
	out := make([]lexicon.Lexeme, 0, len(ids))
	for _, id := range ids {
		if lexeme, ok := f.lexemes[id]; ok {
			out = append(out, lexeme)
		}
	}
	return out, nil
}

func (f *fakeLexemes) Translations(_ context.Context, ids []lexicon.LexemeID, lang lexicon.Language) (map[lexicon.LexemeID][]lexicon.Translation, error) {
	out := map[lexicon.LexemeID][]lexicon.Translation{}
	for _, id := range ids {
		for _, tr := range f.translations[id] {
			if tr.Lang == lang {
				out[id] = append(out[id], tr)
			}
		}
	}
	return out, nil
}

func (f *fakeLexemes) Upsert(context.Context, []lexicon.Lexeme) ([]port.Upserted, error) {
	return nil, nil
}

func (f *fakeLexemes) ByTerm(context.Context, lexicon.Language, string, int64) (lexicon.Lexeme, error) {
	return lexicon.Lexeme{}, port.ErrNotFound
}

func (f *fakeLexemes) SaveTranslations(context.Context, []lexicon.Translation) error { return nil }
