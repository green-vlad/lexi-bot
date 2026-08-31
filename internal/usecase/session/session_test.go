package session_test

import (
	"context"
	"errors"
	"sort"
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
	applied  applied
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

func (f *fakeCards) CountDue(ctx context.Context, courseID study.CourseID, now time.Time) (int, error) {
	due, err := f.Due(ctx, port.DueQuery{CourseID: courseID, Now: now, Limit: len(f.cards) + 1})
	return len(due), err
}

// NewWords отдаёт слова, для которых карточки ещё нет, и отложенные, чей срок
// возврата уже прошёл, — в порядке колоды.
func (f *fakeCards) NewWords(_ context.Context, q port.NewWordQuery) ([]lexicon.LexemeID, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
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
	if f.failWith != nil {
		return study.Card{}, false, f.failWith
	}
	if q.Limit <= 0 || f.counters.get(q.CourseID, q.Day).NewIntroduced >= q.Limit {
		return study.Card{}, false, nil
	}

	card := f.upsert(q.CourseID, q.LexemeID, q.State, q.Now)
	f.counters.addNew(q.CourseID, q.Day, 1)
	return card, true, nil
}

func (f *fakeCards) MarkKnown(_ context.Context, courseID study.CourseID, lexemeID lexicon.LexemeID, now time.Time) error {
	if f.failWith != nil {
		return f.failWith
	}
	f.upsert(courseID, lexemeID, study.CardState{
		State: study.StateKnown, DueAt: now, EaseFactor: study.DefaultEaseFactor,
	}, now)
	return nil
}

func (f *fakeCards) PostponeNew(_ context.Context, courseID study.CourseID, lexemeID lexicon.LexemeID, now, until time.Time) error {
	if f.failWith != nil {
		return f.failWith
	}
	f.upsert(courseID, lexemeID, study.CardState{
		State: study.StateNew, DueAt: until, EaseFactor: study.DefaultEaseFactor,
	}, now)
	return nil
}

// upsert заводит карточку или переводит существующую в новое состояние.
// Как и в базе, тронуть можно только карточку, которую ещё не начали учить.
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

func (f *fakeCards) Apply(_ context.Context, outcome *port.ReviewOutcome) error {
	return f.applyOutcome(outcome)
}

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

func (f *fakeLexemes) Upsert(context.Context, []lexicon.Lexeme) ([]port.Upserted, error) {
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
	decks    *fakeDecks
	rand     *fakeRand
	reviews  *switchableReviews
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
	// Слова уже начаты: повторяются только они, а знакомство с новыми —
	// отдельный сценарий (usecase/intro). Первое слово ждёт дольше всех:
	// очередь идёт по сроку, и тесты рассчитывают на порядок из пула.
	for i, id := range pool {
		f.cards.nextID++
		f.cards.cards = append(f.cards.cards, study.Card{
			ID: f.cards.nextID, CourseID: courseID, LexemeID: id,
			CardState: study.CardState{
				State:        study.StateReview,
				DueAt:        f.now.Add(-time.Duration(len(pool)-i) * time.Minute),
				IntervalDays: 1, EaseFactor: 2.5, Repetitions: 2,
			},
			IntroducedAt:   f.now.AddDate(0, 0, -3),
			LastReviewedAt: f.now.AddDate(0, 0, -1),
		})
	}
	f.decks = &fakeDecks{}
	f.rand = &fakeRand{}
	f.reviews = &switchableReviews{}
	f.courses = &fakeCourses{course: study.Course{
		ID: courseID, UserID: 42, DeckID: 7, TranslationLang: langRU, Status: study.CourseActive,
	}}
	f.settings = &fakeSettings{settings: user.DefaultSettings(user.MustParseTimezone("Asia/Seoul"))}

	service, err := session.New(&session.Deps{
		Decks:     f.decks,
		Rand:      f.rand,
		Reviews:   f.reviews,
		Scheduler: mustScheduler(t),
		Resolver:  study.DefaultRatingResolver(),
		Cards:     f.cards,
		Counters:  f.counters,
		Courses:   f.courses,
		Settings:  f.settings,
		Lexemes:   &fakeLexemes{lexemes: lexemes, translations: translations},
		Clock:     port.ClockFunc(func() time.Time { return f.now }),
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

func TestNextTakesMostOverdueFirst(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 5)

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

func TestNextWhenEverythingIsReviewed(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 2)

	for i := 0; i < 2; i++ {
		item, reason := f.next(t)
		if reason != session.ReasonNone {
			t.Fatalf("карточка %d: причина = %v", i+1, reason)
		}
		f.answer(t, &item)
	}

	// Лимит не выбран, но повторять больше нечего.
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

	newCard := study.Card{ID: 7, CardState: study.CardState{State: study.StateNew}}
	card := study.Card{
		ID:             7,
		CardState:      study.CardState{State: study.StateReview, Repetitions: 2},
		LastReviewedAt: time.Now(),
	}

	// Выбор из вариантов — основной способ спросить слово, и режимы больше
	// не чередуются: печатать или выбирать, решает сам человек кнопкой
	// «напишу сам».
	for _, tt := range []struct {
		name string
		card *study.Card
	}{{"новое слово", &newCard}, {"знакомое слово", &card}} {
		if got := session.PickMode(all, tt.card); got != study.ModeChoice {
			t.Errorf("%s: режим = %v, ожидался выбор из вариантов", tt.name, got)
		}
	}

	// Ввод текстом назначается сам, только если выбор из вариантов выключен.
	only := []study.Mode{study.ModeTyping}
	if got := session.PickMode(only, &card); got != study.ModeTyping {
		t.Errorf("режим = %v, ожидался единственный включённый", got)
	}
	// А новому слову он не годится и тогда: напечатать перевод слова,
	// которое видишь впервые, нельзя — это гарантированный провал.
	if got := session.PickMode(only, &newCard); got != study.ModeChoice {
		t.Errorf("новое слово при единственном режиме = %v, ожидался выбор", got)
	}

	// Пустой набор — это ошибка настроек, но сессия из-за неё не встаёт.
	if got := session.PickMode(nil, &card); got != study.ModeChoice {
		t.Errorf("без включённых режимов = %v, ожидался выбор", got)
	}
}

// mustScheduler — планировщик без джиттера: интервалы в тестах должны быть
// точными, иначе проверять их пришлось бы с допуском.
func mustScheduler(t *testing.T) study.Scheduler {
	t.Helper()

	scheduler, err := study.NewSM2(study.DefaultSM2Config(), nil)
	if err != nil {
		t.Fatalf("NewSM2() вернул ошибку: %v", err)
	}
	return scheduler
}

// fakeDecks — колода для подбора ложных вариантов. Пустой список означает
// «в колоде больше ничего нет», и это отдельный случай, который сессия
// обязана пережить.
type fakeDecks struct {
	distractors []lexicon.Translation
	terms       []lexicon.Lexeme
}

func (f *fakeDecks) Distractors(_ context.Context, q port.DistractorQuery) ([]lexicon.Translation, error) {
	out := make([]lexicon.Translation, 0, len(f.distractors))
	for _, tr := range f.distractors {
		if tr.LexemeID == q.Exclude {
			continue
		}
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
		out = append(out, tr)
	}
	return out, nil
}

// DistractorTerms отдаёт слова колоды: ими проверяется обратное направление.
func (f *fakeDecks) DistractorTerms(_ context.Context, q port.DistractorQuery) ([]lexicon.Lexeme, error) {
	out := make([]lexicon.Lexeme, 0, len(f.terms))
	for i := range f.terms {
		if f.terms[i].ID == q.Exclude {
			continue
		}
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
		out = append(out, f.terms[i])
	}
	return out, nil
}

func (f *fakeDecks) Languages(context.Context) ([]lexicon.Language, error) { return nil, nil }
func (f *fakeDecks) TranslationLanguages(context.Context, lexicon.DeckID) ([]lexicon.Language, error) {
	return nil, nil
}
func (f *fakeDecks) Builtin(context.Context, lexicon.Language) ([]lexicon.Deck, error) {
	return nil, nil
}
func (f *fakeDecks) ByID(context.Context, lexicon.DeckID) (lexicon.Deck, error) {
	return lexicon.Deck{}, port.ErrNotFound
}
func (f *fakeDecks) ByCode(context.Context, string) (lexicon.Deck, error) {
	return lexicon.Deck{}, port.ErrNotFound
}
func (f *fakeDecks) EnsureBuiltin(context.Context, *lexicon.Deck) (lexicon.Deck, error) {
	// Сессия колод не создаёт.
	return lexicon.Deck{}, nil
}

func (f *fakeDecks) EnsurePersonal(context.Context, int64, lexicon.Language, string) (lexicon.Deck, error) {
	return lexicon.Deck{}, nil
}
func (f *fakeDecks) AddItems(context.Context, []lexicon.DeckItem) error { return nil }
func (f *fakeDecks) Items(context.Context, lexicon.DeckID, int, int) ([]lexicon.DeckItem, error) {
	return nil, nil
}

func TestOptionsForChoiceMode(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 5)
	f.settings.settings.QuizModes = []study.Mode{study.ModeChoice}
	f.decks.distractors = []lexicon.Translation{
		{LexemeID: 2, Lang: langRU, Text: "собака", IsPrimary: true},
		{LexemeID: 3, Lang: langRU, Text: "вода", IsPrimary: true},
		{LexemeID: 4, Lang: langRU, Text: "огонь", IsPrimary: true},
		{LexemeID: 5, Lang: langRU, Text: "гора", IsPrimary: true},
	}

	item, reason := f.next(t)
	if reason != session.ReasonNone {
		t.Fatalf("причина = %v", reason)
	}
	if item.Mode != study.ModeChoice {
		t.Fatalf("режим = %v, ожидался выбор", item.Mode)
	}
	if len(item.Options) != session.ChoiceOptions {
		t.Fatalf("вариантов %d, ожидалось %d: %v", len(item.Options), session.ChoiceOptions, item.Options)
	}

	// Правильный ответ на месте и ровно один.
	if item.Correct < 0 || item.Correct >= len(item.Options) {
		t.Fatalf("указатель на правильный вариант = %d", item.Correct)
	}
	if item.Options[item.Correct] != "перевод" {
		t.Errorf("правильный вариант = %q", item.Options[item.Correct])
	}

	seen := map[string]int{}
	for _, option := range item.Options {
		seen[option]++
	}
	if seen["перевод"] != 1 {
		t.Errorf("правильный ответ встречается %d раз", seen["перевод"])
	}
	for option, count := range seen {
		if count > 1 {
			t.Errorf("вариант %q повторяется %d раз", option, count)
		}
	}
}

func TestOptionsSkipDuplicatesOfCorrectAnswer(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 5)
	f.settings.settings.QuizModes = []study.Mode{study.ModeChoice}
	// Ложные варианты, совпадающие с правильным после нормализации:
	// показать их рядом значило бы предложить два правильных ответа.
	f.decks.distractors = []lexicon.Translation{
		{LexemeID: 2, Lang: langRU, Text: "Перевод", IsPrimary: true},
		{LexemeID: 3, Lang: langRU, Text: "перевод.", IsPrimary: true},
		{LexemeID: 4, Lang: langRU, Text: "вода", IsPrimary: true},
		{LexemeID: 5, Lang: langRU, Text: "огонь", IsPrimary: true},
	}

	item, _ := f.next(t)
	// Годных ложных вариантов осталось два, и вариантов вышло три вместо
	// четырёх: это лучше, чем показать один и тот же ответ дважды.
	if len(item.Options) != 3 {
		t.Fatalf("вариантов %d: %v", len(item.Options), item.Options)
	}
	if item.Mode != study.ModeChoice {
		t.Errorf("режим = %v: трёх вариантов достаточно для выбора", item.Mode)
	}
	for i, option := range item.Options {
		if i == item.Correct {
			continue
		}
		if lexicon.Normalize(option, langRU) == "перевод" {
			t.Errorf("вариант %q совпадает с правильным ответом", option)
		}
	}
}

func TestOptionsFallBackToTyping(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 5)
	f.settings.settings.QuizModes = []study.Mode{study.ModeChoice}
	// Ложных вариантов почти нет: колода бедная или переводов не хватает.
	f.decks.distractors = []lexicon.Translation{
		{LexemeID: 2, Lang: langRU, Text: "собака", IsPrimary: true},
	}

	item, _ := f.next(t)
	// Выбор из двух — подбрасывание монетки, а не проверка. Остаётся ввод
	// текстом, даже в сторону родного языка: там засчитывается любое
	// из значений слова.
	if item.Mode != study.ModeTyping {
		t.Errorf("режим = %v, ожидался откат к вводу текстом", item.Mode)
	}
	if len(item.Options) != 0 {
		t.Errorf("варианты = %v, при откате их быть не должно", item.Options)
	}
	if item.OfferTyping {
		t.Error("кнопка «напишу сам» на экране без вариантов не нужна")
	}
}

func TestOptionsAreShuffled(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 5)
	f.settings.settings.QuizModes = []study.Mode{study.ModeChoice}
	f.decks.distractors = []lexicon.Translation{
		{LexemeID: 2, Lang: langRU, Text: "собака", IsPrimary: true},
		{LexemeID: 3, Lang: langRU, Text: "вода", IsPrimary: true},
		{LexemeID: 4, Lang: langRU, Text: "огонь", IsPrimary: true},
	}
	// Источник случайности переворачивает список: правильный ответ,
	// который собирается первым, обязан уехать с первого места вместе
	// с указателем на него.
	f.rand.shuffle = func(n int, swap func(i, j int)) {
		for i, j := 0, n-1; i < j; i, j = i+1, j-1 {
			swap(i, j)
		}
	}

	item, _ := f.next(t)
	if item.Correct == 0 {
		t.Error("правильный вариант остался первым: перемешивание не сработало")
	}
	if item.Options[item.Correct] != "перевод" {
		t.Errorf("указатель на правильный вариант разошёлся с самим вариантом: %+v", item)
	}
}

// fakeRand — управляемый источник случайности: по умолчанию не перемешивает
// ничего, чтобы тесты знали, где что лежит.
type fakeRand struct {
	shuffle func(n int, swap func(i, j int))
}

func (f *fakeRand) Float64() float64 { return 0.5 }
func (f *fakeRand) IntN(int) int     { return 0 }

func (f *fakeRand) Shuffle(n int, swap func(i, j int)) {
	if f.shuffle != nil {
		f.shuffle(n, swap)
	}
}

// NextDue возвращает ближайший срок повторения — как и в базе, с учётом
// того, что отложенные карточки не в счёт.
func (f *fakeCards) NextDue(_ context.Context, courseID study.CourseID) (time.Time, bool, error) {
	var (
		next  time.Time
		found bool
	)
	for i := range f.cards {
		card := &f.cards[i]
		if card.CourseID != courseID || card.State == study.StateSuspended {
			continue
		}
		if !found || card.DueAt.Before(next) {
			next, found = card.DueAt, true
		}
	}
	return next, found, nil
}

// switchableReviews позволяет тесту подставить свой журнал, не пересобирая
// всю сессию.
type switchableReviews struct {
	inner port.ReviewRepo
}

func (s *switchableReviews) Add(ctx context.Context, userID user.ID, review *study.Review) error {
	if s.inner == nil {
		return nil
	}
	return s.inner.Add(ctx, userID, review)
}

func (s *switchableReviews) Stats(ctx context.Context, q port.StatsQuery) (port.ReviewStats, error) {
	if s.inner == nil {
		return port.ReviewStats{}, nil
	}
	return s.inner.Stats(ctx, q)
}

func (s *switchableReviews) ActiveDays(ctx context.Context, userID user.ID, tz user.Timezone, since time.Time) ([]time.Time, error) {
	if s.inner == nil {
		return nil, nil
	}
	return s.inner.ActiveDays(ctx, userID, tz, since)
}

func TestDirectionChangesQuestionAndAnswer(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 3)

	// По умолчанию спрашиваем на узнавание: показываем слово, ждём перевод.
	item, _ := f.next(t)
	if item.Direction != study.DirectionRecognize {
		t.Fatalf("направление = %v", item.Direction)
	}
	if item.Question != "слово" {
		t.Errorf("вопрос = %q, ожидалось слово изучаемого языка", item.Question)
	}
	if len(item.Answer) != 1 || item.Answer[0] != "перевод" {
		t.Errorf("ответ = %v, ожидался перевод", item.Answer)
	}
	if item.AnswerLang != langRU {
		t.Errorf("язык ответа = %v, ожидался язык перевода", item.AnswerLang)
	}

	// Обратное направление: показываем перевод, ждём слово.
	f.settings.settings.ReverseDirection = true
	item, _ = f.next(t)
	if item.Direction != study.DirectionProduce {
		t.Fatalf("направление = %v", item.Direction)
	}
	if item.Question != "перевод" {
		t.Errorf("вопрос = %q, ожидался перевод", item.Question)
	}
	if len(item.Answer) != 1 || item.Answer[0] != "слово" {
		t.Errorf("ответ = %v, ожидалось слово изучаемого языка", item.Answer)
	}
	if item.AnswerLang != langKO {
		t.Errorf("язык ответа = %v, ожидался изучаемый язык", item.AnswerLang)
	}
}

func TestTypingOfferedOnlyTowardsStudiedLanguage(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 5)
	f.decks.distractors = []lexicon.Translation{
		{LexemeID: 2, Lang: langRU, Text: "собака", IsPrimary: true},
		{LexemeID: 3, Lang: langRU, Text: "вода", IsPrimary: true},
		{LexemeID: 4, Lang: langRU, Text: "огонь", IsPrimary: true},
	}
	f.decks.terms = []lexicon.Lexeme{
		{ID: 2, Lang: langKO, Term: "개", POS: lexicon.POSNoun},
		{ID: 3, Lang: langKO, Term: "물", POS: lexicon.POSNoun},
		{ID: 4, Lang: langKO, Term: "불", POS: lexicon.POSNoun},
	}

	// Печатать перевод на родной язык бессмысленно: у слова несколько
	// равноправных значений, и свободный ввод превращается в угадывание
	// того, какое из них мы сочли основным. Кнопки «напишу сам» здесь нет.
	item, _ := f.next(t)
	if item.Mode != study.ModeChoice {
		t.Fatalf("режим = %v, ожидался выбор из вариантов", item.Mode)
	}
	if item.OfferTyping {
		t.Error("в сторону родного языка печатать ответ не предлагаем")
	}

	// В обратную сторону слово одно, и напечатать его — настоящая проверка.
	f.settings.settings.ReverseDirection = true
	item, _ = f.next(t)
	if item.Mode != study.ModeChoice {
		t.Fatalf("режим = %v, ожидался выбор из вариантов", item.Mode)
	}
	if !item.OfferTyping {
		t.Error("в сторону изучаемого языка нужна кнопка «напишу сам»")
	}

	// Выключенный в настройках ввод текстом кнопку не показывает.
	f.settings.settings.QuizModes = []study.Mode{study.ModeChoice}
	item, _ = f.next(t)
	if item.OfferTyping {
		t.Error("ввод текстом выключен в настройках, а кнопка осталась")
	}
}

func TestChoiceOptionsMatchDirection(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 5)
	f.settings.settings.QuizModes = []study.Mode{study.ModeChoice}
	f.settings.settings.ReverseDirection = true
	f.decks.terms = []lexicon.Lexeme{
		{ID: 2, Lang: langKO, Term: "개"},
		{ID: 3, Lang: langKO, Term: "물"},
		{ID: 4, Lang: langKO, Term: "불"},
	}
	f.decks.distractors = []lexicon.Translation{
		{LexemeID: 2, Lang: langRU, Text: "собака", IsPrimary: true},
	}

	// Выбирать нужно из того же, что и ответ: раз ждём слово изучаемого
	// языка, то и варианты должны быть словами, а не переводами.
	item, _ := f.next(t)
	if item.Mode != study.ModeChoice {
		t.Fatalf("режим = %v", item.Mode)
	}
	if len(item.Options) != session.ChoiceOptions {
		t.Fatalf("вариантов %d: %v", len(item.Options), item.Options)
	}
	if item.Options[item.Correct] != "слово" {
		t.Errorf("правильный вариант = %q", item.Options[item.Correct])
	}
	for _, option := range item.Options {
		if option == "собака" {
			t.Errorf("среди вариантов оказался перевод: %v", item.Options)
		}
	}
}

// Contains сообщает, есть ли слово в колоде. Личный словарь заглушке
// не нужен: она обслуживает сценарии, которые своих слов не заводят.
func (*fakeDecks) Contains(context.Context, lexicon.DeckID, lexicon.LexemeID) (bool, error) {
	return false, nil
}

// DueBefore отдаёт сроки карточек, которые подойдут до указанного момента.
// Отбор здесь настоящий: заглушка, отдающая всё подряд, скрыла бы ошибку
// в границах прогноза.
func (f *fakeCards) DueBefore(_ context.Context, courseID study.CourseID, until time.Time) ([]time.Time, error) {
	var out []time.Time
	for i := range f.cards {
		card := &f.cards[i]
		if card.CourseID == courseID && card.State.InRepetition() && card.DueAt.Before(until) {
			out = append(out, card.DueAt)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out, nil
}
