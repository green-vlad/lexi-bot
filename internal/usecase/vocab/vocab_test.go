package vocab_test

import (
	"context"
	"errors"
	"testing"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/vocab"
)

var (
	langKO = lexicon.MustParseLanguage("ko")
	langRU = lexicon.MustParseLanguage("ru")
)

const owner = user.ID(42)

type fixture struct {
	service *vocab.Service
	decks   *fakeDecks
	lexemes *fakeLexemes
	courses *fakeCourses
	users   *fakeUsers
	builtin lexicon.Deck
	course  study.Course
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	f := &fixture{
		decks:   newFakeDecks(),
		lexemes: newFakeLexemes(),
		courses: &fakeCourses{},
		users:   &fakeUsers{},
	}

	f.builtin = f.decks.add(&lexicon.Deck{Code: "ko-top-2000", Lang: langKO, Title: "Корейский: топ-2000"})

	course, err := f.courses.Ensure(context.Background(), study.Course{
		UserID: int64(owner), DeckID: f.builtin.ID, TranslationLang: langRU, Status: study.CourseActive,
	})
	if err != nil {
		t.Fatalf("Ensure() вернул ошибку: %v", err)
	}
	f.course = course
	f.users.users = append(f.users.users, user.User{ID: owner, UILang: user.UILangRU, CurrentCourse: course.ID})

	service, err := vocab.New(vocab.Deps{
		Users: f.users, Decks: f.decks, Lexemes: f.lexemes, Courses: f.courses,
	})
	if err != nil {
		t.Fatalf("vocab.New() вернул ошибку: %v", err)
	}
	f.service = service
	return f
}

func (f *fixture) add(t *testing.T, word *vocab.Word) vocab.Added {
	t.Helper()

	added, err := f.service.Add(context.Background(), owner, word)
	if err != nil {
		t.Fatalf("Add() вернул ошибку: %v", err)
	}
	return added
}

func TestAddCreatesPersonalDeckAndCourse(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	added := f.add(t, &vocab.Word{
		Term: "냉장고", Translations: []string{"холодильник"},
		Reading: "nengjanggo", Example: "냉장고가 있어요.",
	})

	if added.Outcome != vocab.OutcomeAdded {
		t.Fatalf("исход = %v, ожидалось добавление", added.Outcome)
	}
	if added.Reused {
		t.Error("слова не было во встроенном словаре, брать было нечего")
	}
	if added.Lexeme.ID == 0 || added.Lexeme.OwnerID != int64(owner) {
		t.Errorf("слово = %+v, ожидалось личное с идентификатором", added.Lexeme)
	}
	if added.Lexeme.Lang != langKO {
		t.Errorf("язык слова = %v, ожидался язык изучаемой колоды", added.Lexeme.Lang)
	}

	// Курс личной колоды заведён и учится с тем же языком перевода.
	if added.Course.ID == f.course.ID {
		t.Error("своё слово попало в общую встроенную колоду")
	}
	if added.Course.TranslationLang != langRU {
		t.Errorf("язык перевода = %v, ожидался язык текущего курса", added.Course.TranslationLang)
	}

	deck, err := f.decks.ByID(context.Background(), added.Course.DeckID)
	if err != nil {
		t.Fatalf("ByID() вернул ошибку: %v", err)
	}
	if deck.IsBuiltin() || deck.OwnerID != int64(owner) {
		t.Errorf("колода = %+v, ожидалась личная", deck)
	}
	if deck.Size != 1 {
		t.Errorf("размер колоды = %d, ожидалось одно слово", deck.Size)
	}

	// Перевод сохранён и помечен основным.
	translations, err := f.lexemes.Translations(context.Background(), []lexicon.LexemeID{added.Lexeme.ID}, langRU)
	if err != nil {
		t.Fatalf("Translations() вернул ошибку: %v", err)
	}
	if len(translations[added.Lexeme.ID]) != 1 || !translations[added.Lexeme.ID][0].IsPrimary {
		t.Errorf("переводы = %+v", translations[added.Lexeme.ID])
	}
}

func TestAddKeepsOneDeckForManyWords(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	first := f.add(t, &vocab.Word{Term: "냉장고", Translations: []string{"холодильник"}})
	second := f.add(t, &vocab.Word{Term: "옷장", Translations: []string{"шкаф"}})

	if first.Course.ID != second.Course.ID {
		t.Errorf("курсы разные: %d и %d", first.Course.ID, second.Course.ID)
	}
	if f.decks.ensured != 1 {
		t.Errorf("личная колода заводилась %d раза, ожидался один", f.decks.ensured)
	}

	deck, _ := f.decks.ByID(context.Background(), second.Course.DeckID)
	if deck.Size != 2 {
		t.Errorf("размер колоды = %d, ожидалось два слова", deck.Size)
	}

	// Второе слово встало за первым, а не поверх него.
	items, _ := f.decks.Items(context.Background(), deck.ID, 0, 0)
	if len(items) != 2 || items[1].Position != 1 {
		t.Errorf("состав колоды = %+v", items)
	}
}

func TestAddSameWordTwice(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	first := f.add(t, &vocab.Word{Term: "냉장고", Translations: []string{"холодильник"}})
	again := f.add(t, &vocab.Word{Term: "냉장고", Translations: []string{"морозильник"}})

	if again.Outcome != vocab.OutcomeAlreadyPersonal {
		t.Errorf("исход = %v, ожидалось «уже в словаре»", again.Outcome)
	}
	if again.Lexeme.ID != first.Lexeme.ID {
		t.Errorf("слово %d, ожидалось прежнее %d", again.Lexeme.ID, first.Lexeme.ID)
	}

	// Копии не завелось: ни второй лексемы, ни второй строки в колоде.
	if len(f.lexemes.lexemes) != 1 {
		t.Errorf("слов в словаре %d, ожидалось одно", len(f.lexemes.lexemes))
	}
	deck, _ := f.decks.ByID(context.Background(), first.Course.DeckID)
	if deck.Size != 1 {
		t.Errorf("размер колоды = %d, ожидалось одно слово", deck.Size)
	}
}

func TestAddReusesBuiltinLexeme(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	// Слово есть в общем словаре, но не в колоде, которую человек учит.
	builtin := f.lexemes.add(&lexicon.Lexeme{
		Lang: langKO, Term: "냉장고", Reading: "nengjanggo",
		POS: lexicon.POSNoun, FreqRank: 900,
	})

	added := f.add(t, &vocab.Word{Term: "냉장고", Translations: []string{"холодильник"}})

	if added.Outcome != vocab.OutcomeAdded || !added.Reused {
		t.Fatalf("исход = %v, взято из словаря = %t", added.Outcome, added.Reused)
	}
	// Берём словарное слово целиком: у него есть чтение, часть речи
	// и частотность, которых человек не вводил.
	if added.Lexeme.ID != builtin.ID {
		t.Errorf("слово %d, ожидалось встроенное %d", added.Lexeme.ID, builtin.ID)
	}
	if added.Lexeme.FreqRank != 900 || added.Lexeme.POS != lexicon.POSNoun {
		t.Errorf("слово = %+v: словарные поля потеряны", added.Lexeme)
	}
	if len(f.lexemes.lexemes) != 1 {
		t.Errorf("слов в словаре %d: заведена копия", len(f.lexemes.lexemes))
	}
}

func TestAddRefusesWordAlreadyInCourse(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	// Слово уже лежит во встроенной колоде, которую человек учит: копия
	// в личном словаре означала бы две карточки на одно слово.
	builtin := f.lexemes.add(&lexicon.Lexeme{Lang: langKO, Term: "냉장고", POS: lexicon.POSNoun})
	item, err := lexicon.NewDeckItem(f.builtin.ID, builtin.ID, 0)
	if err != nil {
		t.Fatalf("NewDeckItem() вернул ошибку: %v", err)
	}
	if err := f.decks.AddItems(context.Background(), []lexicon.DeckItem{item}); err != nil {
		t.Fatalf("AddItems() вернул ошибку: %v", err)
	}

	added := f.add(t, &vocab.Word{Term: "냉장고", Translations: []string{"холодильник"}})

	if added.Outcome != vocab.OutcomeInCourse {
		t.Fatalf("исход = %v, ожидалось «уже в курсе»", added.Outcome)
	}
	if added.Lexeme.ID != builtin.ID {
		t.Errorf("слово %d, ожидалось встроенное %d", added.Lexeme.ID, builtin.ID)
	}
	// Ни личной колоды, ни курса под неё не завелось.
	if f.decks.ensured != 0 {
		t.Error("под слово, которое и так учится, заведена личная колода")
	}
	if len(f.courses.courses) != 1 {
		t.Errorf("курсов %d, ожидался один", len(f.courses.courses))
	}
}

func TestAddValidatesWord(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	for _, tt := range []struct {
		name string
		word vocab.Word
	}{
		{"без слова", vocab.Word{Translations: []string{"холодильник"}}},
		{"из одних пробелов", vocab.Word{Term: "   ", Translations: []string{"холодильник"}}},
		{"без перевода", vocab.Word{Term: "냉장고"}},
		{"с пустым переводом", vocab.Word{Term: "냉장고", Translations: []string{"  "}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := f.service.Add(context.Background(), owner, &tt.word); err == nil {
				t.Error("ожидалась ошибка")
			}
		})
	}

	// Ничего не сохранилось: неудачная проверка не оставляет следов.
	if len(f.lexemes.lexemes) != 0 || f.decks.ensured != 0 {
		t.Error("после отклонённого слова в базе что-то осталось")
	}
}

func TestAddWithoutCourse(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.courses.courses = nil
	f.users.users[0].CurrentCourse = 0

	// Человек ещё не выбрал, что учит: языки брать неоткуда, и добавлять
	// слово некуда.
	_, err := f.service.Add(context.Background(), owner, &vocab.Word{
		Term: "냉장고", Translations: []string{"холодильник"},
	})
	if !errors.Is(err, vocab.ErrNoCourse) {
		t.Errorf("ошибка = %v, ожидалась ErrNoCourse", err)
	}
}

func TestAddFallsBackToActiveCourse(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	// Выбранного курса нет — берём первый активный, как это делает занятие.
	f.users.users[0].CurrentCourse = 0

	added := f.add(t, &vocab.Word{Term: "냉장고", Translations: []string{"холодильник"}})
	if added.Outcome != vocab.OutcomeAdded {
		t.Errorf("исход = %v, ожидалось добавление", added.Outcome)
	}
	if added.Course.TranslationLang != langRU {
		t.Errorf("язык перевода = %v", added.Course.TranslationLang)
	}
}

func TestNewNeedsDependencies(t *testing.T) {
	t.Parallel()

	if _, err := vocab.New(vocab.Deps{}); err == nil {
		t.Error("сценарий без зависимостей должен быть ошибкой")
	}
}
