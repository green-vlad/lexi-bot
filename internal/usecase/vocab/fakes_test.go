package vocab_test

import (
	"context"
	"time"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// fakeDecks хранит колоды и их состав так же, как база: слово в колоде
// либо есть, либо нет, а размер пересчитывается при добавлении.
type fakeDecks struct {
	decks  map[lexicon.DeckID]lexicon.Deck
	items  map[lexicon.DeckID][]lexicon.DeckItem
	nextID lexicon.DeckID
	// ensured считает, сколько раз заводилась личная колода: повторное
	// добавление не должно плодить их.
	ensured int
}

func newFakeDecks() *fakeDecks {
	return &fakeDecks{
		decks: map[lexicon.DeckID]lexicon.Deck{},
		items: map[lexicon.DeckID][]lexicon.DeckItem{},
	}
}

func (f *fakeDecks) add(deck *lexicon.Deck) lexicon.Deck {
	f.nextID++
	deck.ID = f.nextID
	f.decks[deck.ID] = *deck
	return *deck
}

func (f *fakeDecks) ByID(_ context.Context, id lexicon.DeckID) (lexicon.Deck, error) {
	if deck, ok := f.decks[id]; ok {
		return deck, nil
	}
	return lexicon.Deck{}, port.ErrNotFound
}

func (f *fakeDecks) EnsurePersonal(_ context.Context, ownerID int64, lang lexicon.Language, title string) (lexicon.Deck, error) {
	for _, deck := range f.decks {
		if deck.OwnerID == ownerID && deck.Lang == lang {
			return deck, nil
		}
	}
	f.ensured++
	return f.add(&lexicon.Deck{OwnerID: ownerID, Lang: lang, Title: title}), nil
}

func (f *fakeDecks) AddItems(_ context.Context, items []lexicon.DeckItem) error {
	for _, item := range items {
		if err := item.Validate(); err != nil {
			return err
		}
		if ok, _ := f.Contains(context.Background(), item.DeckID, item.LexemeID); ok {
			continue
		}
		f.items[item.DeckID] = append(f.items[item.DeckID], item)

		deck := f.decks[item.DeckID]
		deck.Size = len(f.items[item.DeckID])
		f.decks[item.DeckID] = deck
	}
	return nil
}

func (f *fakeDecks) Contains(_ context.Context, deckID lexicon.DeckID, lexemeID lexicon.LexemeID) (bool, error) {
	for _, item := range f.items[deckID] {
		if item.LexemeID == lexemeID {
			return true, nil
		}
	}
	return false, nil
}

// Остальное личному словарю не нужно.
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
func (f *fakeDecks) Items(_ context.Context, deckID lexicon.DeckID, _, _ int) ([]lexicon.DeckItem, error) {
	return f.items[deckID], nil
}

// fakeLexemes ведёт словарь: встроенные слова лежат с нулевым владельцем,
// личные — с идентификатором человека.
type fakeLexemes struct {
	lexemes      []lexicon.Lexeme
	translations map[lexicon.LexemeID][]lexicon.Translation
	nextID       lexicon.LexemeID
}

func newFakeLexemes() *fakeLexemes {
	return &fakeLexemes{translations: map[lexicon.LexemeID][]lexicon.Translation{}}
}

func (f *fakeLexemes) add(lexeme *lexicon.Lexeme) lexicon.Lexeme {
	f.nextID++
	lexeme.ID = f.nextID
	f.lexemes = append(f.lexemes, *lexeme)
	return *lexeme
}

func (f *fakeLexemes) ByTerm(_ context.Context, lang lexicon.Language, term string, ownerID int64) (lexicon.Lexeme, error) {
	for _, lexeme := range f.lexemes {
		if lexeme.Lang == lang && lexeme.Term == term && lexeme.OwnerID == ownerID {
			return lexeme, nil
		}
	}
	return lexicon.Lexeme{}, port.ErrNotFound
}

func (f *fakeLexemes) Upsert(_ context.Context, lexemes []lexicon.Lexeme) ([]port.Upserted, error) {
	out := make([]port.Upserted, 0, len(lexemes))
	for i := range lexemes {
		out = append(out, port.Upserted{Lexeme: f.add(&lexemes[i]), Created: true})
	}
	return out, nil
}

func (f *fakeLexemes) SaveTranslations(_ context.Context, translations []lexicon.Translation) error {
	for _, tr := range translations {
		f.translations[tr.LexemeID] = append(f.translations[tr.LexemeID], tr)
	}
	return nil
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

func (f *fakeLexemes) ByIDs(_ context.Context, ids []lexicon.LexemeID) ([]lexicon.Lexeme, error) {
	out := make([]lexicon.Lexeme, 0, len(ids))
	for _, id := range ids {
		for _, lexeme := range f.lexemes {
			if lexeme.ID == id {
				out = append(out, lexeme)
			}
		}
	}
	return out, nil
}

// fakeCourses заводит курс на пару «колода и язык перевода», как база:
// повторный Ensure возвращает тот же курс, а не второй.
type fakeCourses struct {
	courses []study.Course
	nextID  study.CourseID
}

func (f *fakeCourses) Ensure(_ context.Context, c study.Course) (study.Course, error) {
	for _, course := range f.courses {
		if course.UserID == c.UserID && course.DeckID == c.DeckID && course.TranslationLang == c.TranslationLang {
			return course, nil
		}
	}
	f.nextID++
	c.ID = f.nextID
	f.courses = append(f.courses, c)
	return c, nil
}

func (f *fakeCourses) ByID(_ context.Context, id study.CourseID) (study.Course, error) {
	for _, course := range f.courses {
		if course.ID == id {
			return course, nil
		}
	}
	return study.Course{}, port.ErrNotFound
}

func (f *fakeCourses) ByUser(_ context.Context, userID user.ID) ([]study.Course, error) {
	var out []study.Course
	for _, course := range f.courses {
		if course.UserID == int64(userID) {
			out = append(out, course)
		}
	}
	return out, nil
}

func (f *fakeCourses) SetStatus(_ context.Context, id study.CourseID, status study.CourseStatus) error {
	for i := range f.courses {
		if f.courses[i].ID == id {
			f.courses[i].Status = status
			return nil
		}
	}
	return port.ErrNotFound
}

// fakeUsers помнит выбранный курс: без этого сценарий не узнал бы, языки
// какого курса брать.
type fakeUsers struct {
	users []user.User
}

func (f *fakeUsers) ByID(_ context.Context, id user.ID) (user.User, error) {
	for _, u := range f.users {
		if u.ID == id {
			return u, nil
		}
	}
	return user.User{}, port.ErrNotFound
}

func (f *fakeUsers) SetCurrentCourse(_ context.Context, id user.ID, courseID study.CourseID) error {
	for i := range f.users {
		if f.users[i].ID == id {
			f.users[i].CurrentCourse = courseID
			return nil
		}
	}
	return port.ErrNotFound
}

func (f *fakeUsers) Ensure(_ context.Context, u *user.User) (user.User, bool, error) {
	return *u, false, nil
}

func (f *fakeUsers) ByTelegramID(context.Context, user.TelegramID) (user.User, error) {
	return user.User{}, port.ErrNotFound
}
func (f *fakeUsers) SetUILang(context.Context, user.ID, user.UILang) error { return nil }
func (f *fakeUsers) SoftDelete(context.Context, user.ID, time.Time) error  { return nil }
func (f *fakeUsers) Purge(context.Context, user.ID) error                  { return nil }
