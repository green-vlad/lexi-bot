package telegram_test

import (
	"context"
	"time"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
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

func (s *stubCards) IntroduceNew(_ context.Context, q port.IntroduceQuery) ([]study.Card, error) {
	counter := s.counters.value(q.Day)
	remaining := q.Limit - counter.NewIntroduced
	if q.Batch > 0 && q.Batch < remaining {
		remaining = q.Batch
	}
	if remaining <= 0 {
		return nil, nil
	}

	var introduced []study.Card
	for _, lexemeID := range s.pool {
		if len(introduced) >= remaining {
			break
		}
		if s.hasCard(lexemeID) {
			continue
		}

		s.nextID++
		card := study.Card{
			ID:           s.nextID,
			CourseID:     q.CourseID,
			LexemeID:     lexemeID,
			CardState:    study.NewCardState(q.Now),
			IntroducedAt: q.Now,
		}
		s.cards = append(s.cards, card)
		introduced = append(introduced, card)
	}

	s.counters.addNew(q.Day, len(introduced))
	return introduced, nil
}

func (s *stubCards) hasCard(lexemeID lexicon.LexemeID) bool {
	for i := range s.cards {
		if s.cards[i].LexemeID == lexemeID {
			return true
		}
	}
	return false
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
}

func (s *stubLexemes) Upsert(context.Context, []lexicon.Lexeme) ([]lexicon.Lexeme, error) {
	return nil, nil
}

func (s *stubLexemes) ByTerm(context.Context, lexicon.Language, string, int64) (lexicon.Lexeme, error) {
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

func (s *stubLexemes) SaveTranslations(context.Context, []lexicon.Translation) error { return nil }

func (s *stubLexemes) Translations(_ context.Context, ids []lexicon.LexemeID, _ lexicon.Language) (map[lexicon.LexemeID][]lexicon.Translation, error) {
	out := map[lexicon.LexemeID][]lexicon.Translation{}
	for _, id := range ids {
		if tr, ok := s.translations[id]; ok {
			out[id] = tr
		}
	}
	return out, nil
}

// stubDeckSource отдаёт переводы других слов колоды как ложные варианты.
type stubDeckSource struct {
	translations map[lexicon.LexemeID][]lexicon.Translation
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
