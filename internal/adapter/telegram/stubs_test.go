package telegram_test

import (
	"context"
	"time"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// stubCards — CardRepo в памяти. Он повторяет две вещи, на которых стоит
// сессия: выдачу по сроку и проверку версии при записи ответа.
type stubCards struct {
	cards    []study.Card
	pool     []lexicon.LexemeID
	courseID study.CourseID
	counters *stubCounters
	nextID   study.CardID
}

func newStubCards(pool []lexicon.LexemeID, courseID study.CourseID) *stubCards {
	return &stubCards{pool: pool, courseID: courseID, counters: newStubCounters()}
}

func (s *stubCards) Due(_ context.Context, q port.DueQuery) ([]study.Card, error) {
	var due []study.Card
	for i := range s.cards {
		if s.cards[i].CourseID == q.CourseID && s.cards[i].IsDue(q.Now) {
			due = append(due, s.cards[i])
		}
	}
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

// CountDue считает то же, что отдаёт Due: расхождение между числом в меню
// и числом карточек в занятии — как раз то, что тест обязан ловить.
func (s *stubCards) CountDue(ctx context.Context, courseID study.CourseID, now time.Time) (int, error) {
	due, err := s.Due(ctx, port.DueQuery{CourseID: courseID, Now: now, Limit: len(s.cards) + 1})
	return len(due), err
}

// NewWords отдаёт слова колоды в порядке пула: те, для которых карточки ещё
// нет, и те, что отложены кнопкой «пропустить» до уже прошедшего момента.
func (s *stubCards) NewWords(_ context.Context, q port.NewWordQuery) ([]lexicon.LexemeID, error) {
	if q.Limit <= 0 {
		return nil, nil
	}

	var out []lexicon.LexemeID
	for _, lexemeID := range s.pool {
		if len(out) >= q.Limit {
			break
		}
		card, ok := s.byLexeme(lexemeID)
		if ok && (card.State != study.StateNew || card.DueAt.After(q.Now)) {
			continue
		}
		out = append(out, lexemeID)
	}
	return out, nil
}

// StartLearning заводит карточку и тратит место в дневной норме. Заглушка,
// которая молча соглашалась бы на любое число слов, сделала бы бессмысленным
// тест про исчерпанную норму.
func (s *stubCards) StartLearning(_ context.Context, q *port.StartLearningQuery) (study.Card, bool, error) {
	if q.Limit <= 0 || s.counters.value(q.Day).NewIntroduced >= q.Limit {
		return study.Card{}, false, nil
	}

	card := s.upsert(q.CourseID, q.LexemeID, q.State, q.Now)
	s.counters.addNew(q.Day, 1)
	return card, true, nil
}

func (s *stubCards) MarkKnown(_ context.Context, courseID study.CourseID, lexemeID lexicon.LexemeID, now time.Time) error {
	s.upsert(courseID, lexemeID, study.CardState{
		State: study.StateKnown, DueAt: now, EaseFactor: study.DefaultEaseFactor,
	}, now)
	return nil
}

func (s *stubCards) PostponeNew(_ context.Context, courseID study.CourseID, lexemeID lexicon.LexemeID, now, until time.Time) error {
	s.upsert(courseID, lexemeID, study.CardState{
		State: study.StateNew, DueAt: until, EaseFactor: study.DefaultEaseFactor,
	}, now)
	return nil
}

// upsert заводит карточку или переводит существующую в новое состояние.
// Как и в базе, тронуть можно только карточку, которую ещё не начали учить.
func (s *stubCards) upsert(courseID study.CourseID, lexemeID lexicon.LexemeID, state study.CardState, now time.Time) study.Card {
	for i := range s.cards {
		if s.cards[i].LexemeID != lexemeID || s.cards[i].CourseID != courseID {
			continue
		}
		if s.cards[i].State == study.StateNew {
			s.cards[i].CardState = state
			s.cards[i].IntroducedAt = now
		}
		return s.cards[i]
	}

	s.nextID++
	card := study.Card{
		ID: s.nextID, CourseID: courseID, LexemeID: lexemeID,
		CardState: state, IntroducedAt: now,
	}
	s.cards = append(s.cards, card)
	return card
}

func (s *stubCards) byLexeme(lexemeID lexicon.LexemeID) (study.Card, bool) {
	for i := range s.cards {
		if s.cards[i].LexemeID == lexemeID {
			return s.cards[i], true
		}
	}
	return study.Card{}, false
}

func (s *stubCards) Apply(_ context.Context, outcome *port.ReviewOutcome) error {
	for i := range s.cards {
		if s.cards[i].ID != outcome.CardID {
			continue
		}
		// Та же проверка версии, что в базе: ответ на устаревшую карточку
		// не применяется.
		if !s.cards[i].LastReviewedAt.Equal(outcome.ExpectedLastReviewedAt) {
			return port.ErrConflict
		}

		s.cards[i].CardState = outcome.State
		s.cards[i].LastReviewedAt = outcome.Review.RatedAt
		s.counters.addReview(outcome.Day)
		return nil
	}
	return port.ErrNotFound
}

func (s *stubCards) ByID(_ context.Context, id study.CardID) (study.Card, error) {
	for i := range s.cards {
		if s.cards[i].ID == id {
			return s.cards[i], nil
		}
	}
	return study.Card{}, port.ErrNotFound
}

func (s *stubCards) CountsByState(_ context.Context, courseID study.CourseID) (map[study.State]int, error) {
	counts := map[study.State]int{}
	for i := range s.cards {
		if s.cards[i].CourseID == courseID {
			counts[s.cards[i].State]++
		}
	}
	return counts, nil
}

// stubCounters — дневные счётчики в памяти.
type stubCounters struct {
	byDay map[string]port.DailyCounter
}

func newStubCounters() *stubCounters {
	return &stubCounters{byDay: map[string]port.DailyCounter{}}
}

func (s *stubCounters) value(day time.Time) port.DailyCounter {
	return s.byDay[day.Format(time.DateOnly)]
}

func (s *stubCounters) addNew(day time.Time, n int) {
	counter := s.value(day)
	counter.Day = day
	counter.NewIntroduced += n
	s.byDay[day.Format(time.DateOnly)] = counter
}

func (s *stubCounters) addReview(day time.Time) {
	counter := s.value(day)
	counter.Day = day
	counter.ReviewsDone++
	s.byDay[day.Format(time.DateOnly)] = counter
}

func (s *stubCounters) Get(_ context.Context, _ study.CourseID, day time.Time) (port.DailyCounter, error) {
	counter := s.value(day)
	counter.Day = day
	return counter, nil
}

func (s *stubCounters) AddReview(_ context.Context, _ study.CourseID, day time.Time) error {
	s.addReview(day)
	return nil
}

// stubLexemes — словарь в памяти.
type stubLexemes struct {
	lexemes      map[lexicon.LexemeID]lexicon.Lexeme
	translations map[lexicon.LexemeID][]lexicon.Translation
	nextID       lexicon.LexemeID
}

func newStubLexemes() *stubLexemes {
	return &stubLexemes{
		lexemes:      map[lexicon.LexemeID]lexicon.Lexeme{},
		translations: map[lexicon.LexemeID][]lexicon.Translation{},
	}
}

// addLexeme кладёт слово в словарь и возвращает его идентификатор.
func (s *stubLexemes) addLexeme(lexeme *lexicon.Lexeme) lexicon.LexemeID {
	s.nextID++
	lexeme.ID = s.nextID
	s.lexemes[lexeme.ID] = *lexeme
	return lexeme.ID
}

// Upsert сохраняет слова. Заглушка, возвращавшая пустоту, оставляла бы
// добавленное слово без идентификатора — и всё, что за ним, разваливалось бы.
func (s *stubLexemes) Upsert(_ context.Context, lexemes []lexicon.Lexeme) ([]port.Upserted, error) {
	out := make([]port.Upserted, 0, len(lexemes))
	for i := range lexemes {
		id := s.addLexeme(&lexemes[i])
		out = append(out, port.Upserted{Lexeme: s.lexemes[id], Created: true})
	}
	return out, nil
}

// ByTerm ищет слово по написанию среди встроенных или личных.
func (s *stubLexemes) ByTerm(_ context.Context, lang lexicon.Language, term string, ownerID int64) (lexicon.Lexeme, error) {
	for _, lexeme := range s.lexemes {
		if lexeme.Lang == lang && lexeme.Term == term && lexeme.OwnerID == ownerID {
			return lexeme, nil
		}
	}
	return lexicon.Lexeme{}, port.ErrNotFound
}

func (s *stubLexemes) ByIDs(_ context.Context, ids []lexicon.LexemeID) ([]lexicon.Lexeme, error) {
	var out []lexicon.Lexeme
	for _, id := range ids {
		if lex, ok := s.lexemes[id]; ok {
			out = append(out, lex)
		}
	}
	return out, nil
}

func (s *stubLexemes) SaveTranslations(_ context.Context, translations []lexicon.Translation) error {
	for _, tr := range translations {
		s.translations[tr.LexemeID] = append(s.translations[tr.LexemeID], tr)
	}
	return nil
}

func (s *stubLexemes) Translations(_ context.Context, ids []lexicon.LexemeID, _ lexicon.Language) (map[lexicon.LexemeID][]lexicon.Translation, error) {
	out := map[lexicon.LexemeID][]lexicon.Translation{}
	for _, id := range ids {
		if tr, ok := s.translations[id]; ok {
			out[id] = tr
		}
	}
	return out, nil
}

// stubDeckSource отдаёт слова колоды и их переводы как ложные варианты.
type stubDeckSource struct {
	lexemes      map[lexicon.LexemeID]lexicon.Lexeme
	translations map[lexicon.LexemeID][]lexicon.Translation
}

// DistractorTerms отдаёт слова изучаемого языка: ими собираются варианты
// в сторону «перевод → слово». Заглушка, молча отвечающая «ничего нет»,
// делала бы это направление непроверяемым: сессия откатывалась бы к вводу
// текстом, и тест видел бы не тот экран, который проверяет.
func (s *stubDeckSource) DistractorTerms(_ context.Context, q port.DistractorQuery) ([]lexicon.Lexeme, error) {
	var out []lexicon.Lexeme
	for id, lexeme := range s.lexemes {
		if id == q.Exclude {
			continue
		}
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
		out = append(out, lexeme)
	}
	return out, nil
}

func (s *stubDeckSource) Distractors(_ context.Context, q port.DistractorQuery) ([]lexicon.Translation, error) {
	var out []lexicon.Translation
	for id, list := range s.translations {
		if id == q.Exclude || len(list) == 0 {
			continue
		}
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
		out = append(out, list[0])
	}
	return out, nil
}

func (s *stubDeckSource) Languages(context.Context) ([]lexicon.Language, error) { return nil, nil }
func (s *stubDeckSource) TranslationLanguages(context.Context, lexicon.DeckID) ([]lexicon.Language, error) {
	return nil, nil
}
func (s *stubDeckSource) Builtin(context.Context, lexicon.Language) ([]lexicon.Deck, error) {
	return nil, nil
}
func (s *stubDeckSource) ByID(context.Context, lexicon.DeckID) (lexicon.Deck, error) {
	return lexicon.Deck{}, port.ErrNotFound
}
func (s *stubDeckSource) ByCode(context.Context, string) (lexicon.Deck, error) {
	return lexicon.Deck{}, port.ErrNotFound
}
func (s *stubDeckSource) EnsureBuiltin(context.Context, *lexicon.Deck) (lexicon.Deck, error) {
	// Заглушке нечего заводить: онбординг и сессия колоды не создают.
	return lexicon.Deck{}, nil
}

func (s *stubDeckSource) EnsurePersonal(context.Context, int64, lexicon.Language, string) (lexicon.Deck, error) {
	return lexicon.Deck{}, nil
}
func (s *stubDeckSource) AddItems(context.Context, []lexicon.DeckItem) error { return nil }
func (s *stubDeckSource) Items(context.Context, lexicon.DeckID, int, int) ([]lexicon.DeckItem, error) {
	return nil, nil
}

// stubRand ничего не перемешивает: в тестах важно знать, где правильный
// вариант, а перемешивание проверяется отдельно.
type stubRand struct{}

func (stubRand) Float64() float64            { return 0.5 }
func (stubRand) IntN(n int) int              { return 0 }
func (stubRand) Shuffle(int, func(i, j int)) {}

// NextDue возвращает ближайший срок повторения — как и в базе, с учётом
// того, что отложенные карточки не в счёт.
func (s *stubCards) NextDue(_ context.Context, courseID study.CourseID) (time.Time, bool, error) {
	var (
		next  time.Time
		found bool
	)
	for i := range s.cards {
		card := &s.cards[i]
		if card.CourseID != courseID || card.State == study.StateSuspended {
			continue
		}
		if !found || card.DueAt.Before(next) {
			next, found = card.DueAt, true
		}
	}
	return next, found, nil
}

// stubReviews — журнал повторений в памяти: сводке от него нужна только
// точность.
type stubReviews struct {
	total   int
	correct int
}

func (s *stubReviews) Add(context.Context, user.ID, *study.Review) error { return nil }

func (s *stubReviews) Stats(context.Context, port.StatsQuery) (port.ReviewStats, error) {
	return port.ReviewStats{Total: s.total, Correct: s.correct}, nil
}

func (s *stubReviews) ActiveDays(context.Context, user.ID, user.Timezone, time.Time) ([]time.Time, error) {
	return nil, nil
}

// Contains сообщает, есть ли слово в колоде. Личный словарь заглушке
// не нужен: она обслуживает сценарии, которые своих слов не заводят.
func (*stubDeckSource) Contains(context.Context, lexicon.DeckID, lexicon.LexemeID) (bool, error) {
	return false, nil
}
