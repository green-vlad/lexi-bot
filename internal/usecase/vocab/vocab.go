// Package vocab ведёт личный словарь: слова, которые человек добавил сам.
//
// Личные слова живут в колоде «Мои слова: <язык>», заводимой при первом
// добавлении, и учатся отдельным курсом. Отдельным потому, что курс — это
// пара «колода и язык перевода», а дописать своё слово в общую встроенную
// колоду нельзя: она одна на всех.
package vocab

import (
	"context"
	"errors"
	"fmt"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// ErrNoCourse означает, что человеку некуда добавить слово: он ещё не выбрал,
// что учит, и языки брать неоткуда.
var ErrNoCourse = errors.New("нет курса, к которому добавить слово")

// Deps — зависимости личного словаря.
type Deps struct {
	Users   port.UserRepo
	Decks   port.DeckRepo
	Lexemes port.LexemeRepo
	Courses port.CourseRepo
}

// Service — сценарий личного словаря.
type Service struct {
	deps Deps
}

// New создаёт сценарий.
func New(deps Deps) (*Service, error) {
	switch {
	case deps.Users == nil:
		return nil, errors.New("личному словарю нужен UserRepo")
	case deps.Decks == nil:
		return nil, errors.New("личному словарю нужен DeckRepo")
	case deps.Lexemes == nil:
		return nil, errors.New("личному словарю нужен LexemeRepo")
	case deps.Courses == nil:
		return nil, errors.New("личному словарю нужен CourseRepo")
	}
	return &Service{deps: deps}, nil
}

// Word — что человек добавляет.
type Word struct {
	Term string
	// Translations — переводы, основной первым. Пустой список — ошибка:
	// слово без перевода не карточка.
	Translations []string
	Reading      string
	Example      string
	POS          lexicon.PartOfSpeech
}

// Outcome — чем кончилось добавление.
type Outcome uint8

// Исходы добавления.
const (
	// OutcomeAdded — слово добавлено в личную колоду.
	OutcomeAdded Outcome = iota
	// OutcomeAlreadyPersonal — оно уже там лежит.
	OutcomeAlreadyPersonal
	// OutcomeInCourse — слово нашлось во встроенной колоде, которую человек
	// и так учит. Копия в личном словаре означала бы две карточки на одно
	// слово и два срока повторения.
	OutcomeInCourse
)

// Added — результат добавления.
type Added struct {
	Outcome Outcome
	Lexeme  lexicon.Lexeme
	// Course — курс личной колоды: в нём слово и появится.
	Course study.Course
	// Reused означает, что слово нашлось во встроенном словаре и в личную
	// колоду попало оно само, а не копия. Так у слова остаются чтение,
	// часть речи и примеры из словаря.
	Reused bool
}

// Add добавляет слово в личный словарь пользователя.
//
// Языки берутся из курса, который человек учит сейчас: изучаемый — из его
// колоды, язык перевода — из самого курса. Спрашивать их отдельно значило бы
// разводить диалог там, где ответ уже известен.
func (s *Service) Add(ctx context.Context, userID user.ID, word *Word) (Added, error) {
	current, err := s.currentCourse(ctx, userID)
	if err != nil {
		return Added{}, err
	}

	deck, err := s.deps.Decks.ByID(ctx, current.DeckID)
	if err != nil {
		return Added{}, fmt.Errorf("найти колоду курса: %w", err)
	}

	// Проверка идёт целиком и до первой записи: слово с пустым переводом
	// не должно оставлять за собой заведённую колоду и курс.
	prepared, texts, err := prepare(word, deck.Lang, current.TranslationLang, int64(userID))
	if err != nil {
		return Added{}, err
	}

	lexeme, reused, err := s.lexeme(ctx, int64(userID), deck.Lang, &prepared)
	if err != nil {
		return Added{}, err
	}

	// Слово из встроенной колоды, которую человек уже учит, добавлять
	// незачем: оно и так придёт своим чередом.
	if reused && lexeme.ID != 0 {
		inCourse, err := s.deps.Decks.Contains(ctx, deck.ID, lexeme.ID)
		if err != nil {
			return Added{}, fmt.Errorf("проверить колоду курса: %w", err)
		}
		if inCourse {
			return Added{Outcome: OutcomeInCourse, Lexeme: lexeme, Reused: true}, nil
		}
	}

	personal, err := s.deps.Decks.EnsurePersonal(ctx, int64(userID), deck.Lang, personalTitle(deck.Lang))
	if err != nil {
		return Added{}, fmt.Errorf("завести личную колоду: %w", err)
	}

	course, err := s.deps.Courses.Ensure(ctx, study.Course{
		UserID:          int64(userID),
		DeckID:          personal.ID,
		TranslationLang: current.TranslationLang,
		Status:          study.CourseActive,
	})
	if err != nil {
		return Added{}, fmt.Errorf("завести курс личной колоды: %w", err)
	}

	if lexeme.ID != 0 {
		known, err := s.deps.Decks.Contains(ctx, personal.ID, lexeme.ID)
		if err != nil {
			return Added{}, fmt.Errorf("проверить личную колоду: %w", err)
		}
		if known {
			return Added{
				Outcome: OutcomeAlreadyPersonal, Lexeme: lexeme,
				Course: course, Reused: reused,
			}, nil
		}
	}

	saved, err := s.save(ctx, &lexeme, current.TranslationLang, texts)
	if err != nil {
		return Added{}, err
	}

	// Позиция — текущий размер колоды: своё слово встаёт в конец очереди,
	// за теми, что добавлены раньше. Два одновременных добавления могут
	// занять одну позицию, и это не беда: состав колоды доупорядочен
	// по идентификатору слова.
	item, err := lexicon.NewDeckItem(personal.ID, saved.ID, personal.Size)
	if err != nil {
		return Added{}, err
	}
	if err := s.deps.Decks.AddItems(ctx, []lexicon.DeckItem{item}); err != nil {
		return Added{}, fmt.Errorf("добавить слово в личную колоду: %w", err)
	}

	return Added{Outcome: OutcomeAdded, Lexeme: saved, Course: course, Reused: reused}, nil
}

// placeholderLexemeID подставляется, когда перевод проверяют до того, как
// у слова появился идентификатор. Проверка перевода требует непустой ссылки
// на слово, а узнать её раньше записи нельзя; в базу уезжает настоящая.
const placeholderLexemeID = lexicon.LexemeID(1)

// prepare проверяет слово целиком: и написание с чтением, и переводы.
//
// Отдельным шагом до всякой записи, потому что проверка на полпути хуже
// её отсутствия: человек получает ошибку, а в базе остаётся половина
// сделанного — заведённая колода без слова, курс без карточек.
func prepare(word *Word, lang, translationLang lexicon.Language, ownerID int64) (lexicon.Lexeme, []string, error) {
	lexeme, err := lexicon.NewLexeme(lexicon.LexemeParams{
		Lang:    lang,
		Term:    word.Term,
		Reading: word.Reading,
		Example: word.Example,
		POS:     word.POS,
		OwnerID: ownerID,
	})
	if err != nil {
		return lexicon.Lexeme{}, nil, err
	}
	if len(word.Translations) == 0 {
		return lexicon.Lexeme{}, nil, fmt.Errorf("translations: %w (слово без перевода — не карточка)", lexicon.ErrRequired)
	}

	texts := make([]string, 0, len(word.Translations))
	for _, text := range word.Translations {
		translation, err := lexicon.NewTranslation(lexicon.TranslationParams{
			LexemeID: placeholderLexemeID, Lang: translationLang, Text: text,
		})
		if err != nil {
			return lexicon.Lexeme{}, nil, err
		}
		texts = append(texts, translation.Text)
	}
	return lexeme, texts, nil
}

// lexeme находит слово в словаре или оставляет подготовленное новое.
//
// Сначала ищется встроенное: если слово уже есть в общем словаре, копию
// заводить незачем — у встроенного и чтение проставлено, и часть речи,
// и примеры. Потом — своё же, добавленное раньше.
func (s *Service) lexeme(ctx context.Context, ownerID int64, lang lexicon.Language, prepared *lexicon.Lexeme) (lexicon.Lexeme, bool, error) {
	builtin, err := s.deps.Lexemes.ByTerm(ctx, lang, prepared.Term, 0)
	switch {
	case err == nil:
		return builtin, true, nil
	case !errors.Is(err, port.ErrNotFound):
		return lexicon.Lexeme{}, false, fmt.Errorf("поискать слово во встроенном словаре: %w", err)
	}

	own, err := s.deps.Lexemes.ByTerm(ctx, lang, prepared.Term, ownerID)
	switch {
	case err == nil:
		return own, false, nil
	case !errors.Is(err, port.ErrNotFound):
		return lexicon.Lexeme{}, false, fmt.Errorf("поискать слово в личном словаре: %w", err)
	}
	return *prepared, false, nil
}

// save записывает слово и его переводы.
func (s *Service) save(ctx context.Context, lexeme *lexicon.Lexeme, lang lexicon.Language, texts []string) (lexicon.Lexeme, error) {
	if lexeme.ID == 0 {
		upserted, err := s.deps.Lexemes.Upsert(ctx, []lexicon.Lexeme{*lexeme})
		if err != nil {
			return lexicon.Lexeme{}, fmt.Errorf("сохранить слово: %w", err)
		}
		if len(upserted) == 0 {
			return lexicon.Lexeme{}, errors.New("слово не сохранилось")
		}
		lexeme = &upserted[0].Lexeme
	}

	translations := make([]lexicon.Translation, 0, len(texts))
	for i, text := range texts {
		translation, err := lexicon.NewTranslation(lexicon.TranslationParams{
			LexemeID: lexeme.ID, Lang: lang, Text: text, IsPrimary: i == 0,
		})
		if err != nil {
			return lexicon.Lexeme{}, err
		}
		translations = append(translations, translation)
	}
	if err := s.deps.Lexemes.SaveTranslations(ctx, translations); err != nil {
		return lexicon.Lexeme{}, fmt.Errorf("сохранить переводы: %w", err)
	}
	return *lexeme, nil
}

// currentCourse возвращает курс, который человек учит сейчас.
func (s *Service) currentCourse(ctx context.Context, userID user.ID) (study.Course, error) {
	known, err := s.deps.Users.ByID(ctx, userID)
	if err != nil {
		return study.Course{}, fmt.Errorf("найти пользователя: %w", err)
	}

	list, err := s.deps.Courses.ByUser(ctx, userID)
	if err != nil {
		return study.Course{}, fmt.Errorf("получить курсы: %w", err)
	}

	var first study.Course
	for _, course := range list {
		if course.ID == known.CurrentCourse {
			return course, nil
		}
		if first.ID == 0 && course.IsActive() {
			first = course
		}
	}
	if first.ID == 0 {
		return study.Course{}, ErrNoCourse
	}
	return first, nil
}

// personalTitle даёт личной колоде имя вида «Мои слова: ko».
//
// Язык в заголовке нужен потому, что колод у человека столько, сколько
// языков он учит: слова корейского и испанского не должны лежать вместе.
func personalTitle(lang lexicon.Language) string {
	return "Мои слова: " + lang.String()
}
