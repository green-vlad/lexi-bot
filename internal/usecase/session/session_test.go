package session_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/internal/usecase/session"
)

var (
	langKO = lexicon.MustParseLanguage("ko")
	langRU = lexicon.MustParseLanguage("ru")
)

const courseID study.CourseID = 1

// fakeCards — CardRepo в памяти: выдача по сроку и ввод новых с дневным
// лимитом, то есть ровно то, на чём стоит очередь.
type fakeCards struct {
	cards []study.Card
	// pool — слова колоды, из которых вводятся новые карточки.
	pool     []lexicon.LexemeID
	counters *fakeCounters
	nextID   study.CardID
	failWith error
}

func (f *fakeCards) Due(_ context.Context, q port.DueQuery) ([]study.Card, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}

	var due []study.Card
	for i := range f.cards {
		if f.cards[i].IsDue(q.Now) {
			due = append(due, f.cards[i])
		}
	}
	// Порядок как в базе: по возрастанию срока.
	for i := 1; i < len(due); i++ {
		for j := i; j > 0 && due[j].DueAt.Before(due[j-1].DueAt); j-- {
			due[j], due[j-1] = due[j-1], due[j]
		}
	}
	if q.Limit > 0 && len(due) > q.Limit {
		due = due[:q.Limit]
	}
	return due, nil
}

func (f *fakeCards) IntroduceNew(_ context.Context, q port.IntroduceQuery) ([]study.Card, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}

	counter := f.counters.get(q.CourseID, q.Day)
	remaining := q.Limit - counter.NewIntroduced
	if q.Batch > 0 && q.Batch < remaining {
		remaining = q.Batch
	}
	if remaining <= 0 {
		return nil, nil
	}

	introduced := make([]study.Card, 0, remaining)
	for _, lexemeID := range f.pool {
		if len(introduced) >= remaining {
			break
		}
		if f.hasCard(lexemeID) {
			continue
		}

		f.nextID++
		card := study.Card{
			ID:           f.nextID,
			CourseID:     q.CourseID,
			LexemeID:     lexemeID,
			CardState:    study.NewCardState(q.Now),
			IntroducedAt: q.Now,
		}
		f.cards = append(f.cards, card)
		introduced = append(introduced, card)
	}

	f.counters.addNew(q.CourseID, q.Day, len(introduced))
	return introduced, nil
}

func (f *fakeCards) hasCard(lexemeID lexicon.LexemeID) bool {
	for i := range f.cards {
		if f.cards[i].LexemeID == lexemeID {
			return true
		}
	}
	return false
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

// fakeCounters — дневные счётчики в памяти.
type fakeCounters struct {
	byDay map[string]port.DailyCounter
}

func newFakeCounters() *fakeCounters {
	return &fakeCounters{byDay: map[string]port.DailyCounter{}}
}

func key(courseID study.CourseID, day time.Time) string {
	return day.Format(time.DateOnly)
}

func (f *fakeCounters) get(courseID study.CourseID, day time.Time) port.DailyCounter {
	return f.byDay[key(courseID, day)]
}

func (f *fakeCounters) addNew(courseID study.CourseID, day time.Time, n int) {
	counter := f.byDay[key(courseID, day)]
	counter.Day = day
	counter.NewIntroduced += n
	f.byDay[key(courseID, day)] = counter
}

func (f *fakeCounters) Get(_ context.Context, courseID study.CourseID, day time.Time) (port.DailyCounter, error) {
	counter := f.get(courseID, day)
	counter.Day = day
	return counter, nil
}

func (f *fakeCounters) AddReview(_ context.Context, courseID study.CourseID, day time.Time) error {
	counter := f.byDay[key(courseID, day)]
	counter.Day = day
	counter.ReviewsDone++
	f.byDay[key(courseID, day)] = counter
	return nil
}

// fakeCourses, fakeSettings, fakeLexemes — минимальные заглушки.
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

func (f *fakeSettings) Get(context.Context, user.ID) (user.Settings, error) {
	return f.settings, nil
}
func (f *fakeSettings) Save(context.Context, user.ID, user.Settings) error { return nil }

type fakeLexemes struct {
	lexemes      map[lexicon.LexemeID]lexicon.Lexeme
	translations map[lexicon.LexemeID][]lexicon.Translation
}

func (f *fakeLexemes) Upsert(context.Context, []lexicon.Lexeme) ([]lexicon.Lexeme, error) {
	return nil, nil
}

func (f *fakeLexemes) ByTerm(context.Context, lexicon.Language, string, int64) (lexicon.Lexeme, error) {
	return lexicon.Lexeme{}, port.ErrNotFound
}

func (f *fakeLexemes) ByIDs(_ context.Context, ids []lexicon.LexemeID) ([]lexicon.Lexeme, error) {
	var out []lexicon.Lexeme
	for _, id := range ids {
		if lex, ok := f.lexemes[id]; ok {
			out = append(out, lex)
		}
	}
	return out, nil
}

func (f *fakeLexemes) SaveTranslations(context.Context, []lexicon.Translation) error { return nil }

func (f *fakeLexemes) Translations(_ context.Context, ids []lexicon.LexemeID, _ lexicon.Language) (map[lexicon.LexemeID][]lexicon.Translation, error) {
	out := map[lexicon.LexemeID][]lexicon.Translation{}
	for _, id := range ids {
		if tr, ok := f.translations[id]; ok {
			out[id] = tr
		}
	}
	return out, nil
}

// fixture — курс с колодой из нескольких слов.
type fixture struct {
	service  *session.Service
	cards    *fakeCards
	counters *fakeCounters
	settings *fakeSettings
	courses  *fakeCourses
	now      time.Time
}

func newFixture(t *testing.T, words int) *fixture {
	t.Helper()

	f := &fixture{
		counters: newFakeCounters(),
		now:      time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC),
	}

	lexemes := map[lexicon.LexemeID]lexicon.Lexeme{}
	translations := map[lexicon.LexemeID][]lexicon.Translation{}
	pool := make([]lexicon.LexemeID, 0, words)
	for i := 1; i <= words; i++ {
		id := lexicon.LexemeID(i)
		pool = append(pool, id)
		lexemes[id] = lexicon.Lexeme{ID: id, Lang: langKO, Term: "слово", POS: lexicon.POSNoun}
		translations[id] = []lexicon.Translation{{LexemeID: id, Lang: langRU, Text: "перевод", IsPrimary: true}}
	}

	f.cards = &fakeCards{pool: pool, counters: f.counters}
	f.courses = &fakeCourses{course: study.Course{
		ID: courseID, UserID: 42, DeckID: 7, TranslationLang: langRU, Status: study.CourseActive,
	}}
	f.settings = &fakeSettings{settings: user.DefaultSettings(user.MustParseTimezone("Asia/Seoul"))}

	service, err := session.New(&session.Deps{
		Cards:    f.cards,
		Counters: f.counters,
		Courses:  f.courses,
		Settings: f.settings,
		Lexemes:  &fakeLexemes{lexemes: lexemes, translations: translations},
		Clock:    port.ClockFunc(func() time.Time { return f.now }),
	})
	if err != nil {
		t.Fatalf("New() вернул ошибку: %v", err)
	}
	f.service = service
	return f
}

func (f *fixture) next(t *testing.T) (session.Item, session.Reason) {
	t.Helper()

	item, reason, err := f.service.Next(context.Background(), courseID)
	if err != nil {
		t.Fatalf("Next() вернул ошибку: %v", err)
	}
	return item, reason
}

func TestNextIntroducesNewOneAtATime(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 5)
	f.settings.settings.NewPerDay = 3

	item, reason := f.next(t)
	if reason != session.ReasonNone {
		t.Fatalf("причина = %v, ожидалась карточка", reason)
	}
	if item.Card.State != study.StateNew {
		t.Errorf("фаза = %v, ожидалась new", item.Card.State)
	}
	if item.Lexeme.ID != item.Card.LexemeID {
		t.Errorf("слово не соответствует карточке: %+v", item)
	}
	if len(item.Translations) == 0 {
		t.Error("карточка без переводов: показывать нечего")
	}

	// Вводится ровно одно слово: человек, бросивший занятие после первой
	// карточки, не должен терять весь дневной лимит.
	counter, err := f.counters.Get(context.Background(), courseID, f.settings.settings.DayStart(f.now))
	if err != nil {
		t.Fatalf("Get() вернул ошибку: %v", err)
	}
	if counter.NewIntroduced != 1 {
		t.Errorf("введено %d слов, ожидалось одно", counter.NewIntroduced)
	}
}

func TestNextHonoursNewPerDay(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 10)
	f.settings.settings.NewPerDay = 3

	// Три новых слова подряд: каждое отвечено, чтобы освободить очередь.
	for i := 0; i < 3; i++ {
		item, reason := f.next(t)
		if reason != session.ReasonNone {
			t.Fatalf("карточка %d: причина = %v", i+1, reason)
		}
		f.answer(t, &item)
	}

	// Четвёртого не будет: дневной лимит выбран, хотя слова в колоде есть.
	_, reason := f.next(t)
	if reason != session.ReasonDailyLimit {
		t.Errorf("причина = %v, ожидался дневной лимит", reason)
	}
}

func TestNextResetsLimitNextDay(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 10)
	f.settings.settings.NewPerDay = 1

	item, _ := f.next(t)
	f.answer(t, &item)

	if _, reason := f.next(t); reason != session.ReasonDailyLimit {
		t.Fatalf("причина = %v, ожидался дневной лимит", reason)
	}

	// Наступили новые сутки — в таймзоне пользователя, а не в UTC.
	// В Сеуле полночь наступает на девять часов раньше.
	f.now = f.now.Add(15 * time.Hour)
	if got := f.settings.settings.DayStart(f.now).Format(time.DateOnly); got != "2026-08-25" {
		t.Fatalf("подготовка теста: сутки пользователя = %s", got)
	}

	if _, reason := f.next(t); reason != session.ReasonNone {
		t.Errorf("причина = %v, ожидалась новая карточка: сутки сменились", reason)
	}
}

func TestNextPrefersOverdueOverNew(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 5)
	f.settings.settings.NewPerDay = 5

	// Просроченная карточка ждёт со вчера, ещё одна подошла минуту назад.
	f.cards.cards = []study.Card{
		{
			ID: 100, CourseID: courseID, LexemeID: 1, IntroducedAt: f.now.AddDate(0, 0, -5),
			CardState: study.CardState{
				State: study.StateReview, DueAt: f.now.Add(-time.Minute),
				IntervalDays: 3, EaseFactor: 2.5, Repetitions: 2,
			},
			LastReviewedAt: f.now.AddDate(0, 0, -3),
		},
		{
			ID: 101, CourseID: courseID, LexemeID: 2, IntroducedAt: f.now.AddDate(0, 0, -9),
			CardState: study.CardState{
				State: study.StateReview, DueAt: f.now.AddDate(0, 0, -1),
				IntervalDays: 5, EaseFactor: 2.5, Repetitions: 3,
			},
			LastReviewedAt: f.now.AddDate(0, 0, -6),
		},
	}
	f.cards.nextID = 200

	// Первой идёт та, что ждёт дольше всех, а не новое слово.
	item, reason := f.next(t)
	if reason != session.ReasonNone {
		t.Fatalf("причина = %v", reason)
	}
	if item.Card.ID != 101 {
		t.Errorf("показана карточка %d, ожидалась просроченная 101", item.Card.ID)
	}

	counter, _ := f.counters.Get(context.Background(), courseID, f.settings.settings.DayStart(f.now))
	if counter.NewIntroduced != 0 {
		t.Error("новое слово введено, пока были непросмотренные повторения")
	}
}

func TestNextStopsOnDailyReviewCap(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 10)
	f.settings.settings.MaxReviewsPerDay = user.MinReviewsPerDay

	day := f.settings.settings.DayStart(f.now)
	for i := 0; i < user.MinReviewsPerDay; i++ {
		if err := f.counters.AddReview(context.Background(), courseID, day); err != nil {
			t.Fatalf("AddReview() вернул ошибку: %v", err)
		}
	}

	// Потолок считает все ответы: настроивший «не больше N карточек в день»
	// имеет в виду N карточек, а не N плюс новые сверху.
	if _, reason := f.next(t); reason != session.ReasonDailyLimit {
		t.Errorf("причина = %v, ожидался дневной лимит", reason)
	}
}

func TestNextWhenDeckIsExhausted(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 2)
	f.settings.settings.NewPerDay = 10

	for i := 0; i < 2; i++ {
		item, reason := f.next(t)
		if reason != session.ReasonNone {
			t.Fatalf("карточка %d: причина = %v", i+1, reason)
		}
		f.answer(t, &item)
	}

	// Лимит не выбран, но слова в колоде кончились — это «на сегодня всё»
	// по другой причине, и сказать об этом надо иначе.
	if _, reason := f.next(t); reason != session.ReasonCaughtUp {
		t.Errorf("причина = %v, ожидалось ReasonCaughtUp", reason)
	}
}

func TestNextSkipsPausedCourse(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 5)
	f.courses.course.Status = study.CoursePaused

	if _, reason := f.next(t); reason != session.ReasonPaused {
		t.Errorf("причина = %v, ожидалась пауза", reason)
	}
}

func TestNextReportsFailures(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 5)
	f.cards.failWith = errors.New("база недоступна")

	if _, _, err := f.service.Next(context.Background(), courseID); err == nil {
		t.Error("недоступная база должна давать ошибку")
	}

	if _, _, err := f.service.Next(context.Background(), 999); !errors.Is(err, port.ErrNotFound) {
		t.Error("несуществующий курс должен давать ErrNotFound")
	}
}

// answer отмечает карточку отвеченной: сдвигает срок и увеличивает счётчик.
// Настоящую запись делает T-030, здесь важно лишь освободить очередь.
func (f *fixture) answer(t *testing.T, item *session.Item) {
	t.Helper()

	for i := range f.cards.cards {
		if f.cards.cards[i].ID == item.Card.ID {
			f.cards.cards[i].DueAt = f.now.AddDate(0, 0, 1)
			f.cards.cards[i].State = study.StateReview
			f.cards.cards[i].Repetitions++
			f.cards.cards[i].LastReviewedAt = f.now
		}
	}
	if err := f.counters.AddReview(context.Background(), courseID, f.settings.settings.DayStart(f.now)); err != nil {
		t.Fatalf("AddReview() вернул ошибку: %v", err)
	}
}

func TestPickMode(t *testing.T) {
	t.Parallel()

	all := study.Modes()

	// Новое слово всегда спрашивается узнаванием: напечатать перевод слова,
	// которого человек ни разу не видел, невозможно.
	newCard := study.Card{ID: 7, CardState: study.CardState{State: study.StateNew}}
	if got := session.PickMode(all, &newCard); got != study.ModeRecall {
		t.Errorf("новое слово = %v, ожидалось recall", got)
	}

	// Выученная карточка чередует режимы, но детерминированно: одно и то же
	// состояние всегда даёт один и тот же режим.
	card := study.Card{
		ID:             7,
		CardState:      study.CardState{State: study.StateReview, Repetitions: 2},
		LastReviewedAt: time.Now(),
	}
	first := session.PickMode(all, &card)
	if second := session.PickMode(all, &card); first != second {
		t.Errorf("режим меняется между вызовами: %v и %v", first, second)
	}

	// Следующее повторение той же карточки даёт другой режим — иначе
	// чередования не было бы вовсе.
	card.Repetitions++
	if next := session.PickMode(all, &card); next == first {
		t.Errorf("после повторения режим остался %v", next)
	}

	// Из включённых режимов выбирается только включённый.
	only := []study.Mode{study.ModeTyping}
	if got := session.PickMode(only, &card); got != study.ModeTyping {
		t.Errorf("режим = %v, ожидался единственный включённый", got)
	}
	if got := session.PickMode(only, &newCard); got != study.ModeTyping {
		t.Errorf("новое слово при единственном режиме = %v", got)
	}

	// Пустой набор — это ошибка настроек, но сессия из-за неё не встаёт.
	if got := session.PickMode(nil, &card); got != study.ModeRecall {
		t.Errorf("без включённых режимов = %v, ожидалось recall", got)
	}
}
