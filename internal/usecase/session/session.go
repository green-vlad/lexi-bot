// Package session ведёт учебную сессию: решает, что показать следующим,
// и принимает ответы.
//
// Очередь строится по одной карточке за раз, а не списком на всё занятие.
// Список пришлось бы держать между сообщениями и как-то освежать: пока
// человек думает над словом, наступают сроки других карточек, а сам он может
// уйти на полчаса и вернуться. Запрос «что дальше» дёшев (одна выборка
// по индексу), и он всегда отвечает про сейчас, а не про то, как было
// в начале занятия.
package session

import (
	"context"
	"errors"
	"fmt"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// Deps — зависимости сессии.
type Deps struct {
	Cards    port.CardRepo
	Counters port.CounterRepo
	Courses  port.CourseRepo
	Settings port.SettingsRepo
	Lexemes  port.LexemeRepo
	Clock    port.Clock
}

// Service — учебная сессия.
type Service struct {
	deps Deps
}

// New создаёт сессию. Зависимости передаются указателем, как и в остальных
// конструкторах: структура крупная, а копировать её незачем.
func New(deps *Deps) (*Service, error) {
	if deps == nil {
		return nil, errors.New("сессии нужны зависимости")
	}

	switch {
	case deps.Cards == nil:
		return nil, errors.New("сессии нужен CardRepo")
	case deps.Counters == nil:
		return nil, errors.New("сессии нужен CounterRepo")
	case deps.Courses == nil:
		return nil, errors.New("сессии нужен CourseRepo")
	case deps.Settings == nil:
		return nil, errors.New("сессии нужен SettingsRepo")
	case deps.Lexemes == nil:
		return nil, errors.New("сессии нужен LexemeRepo")
	}
	if deps.Clock == nil {
		return nil, errors.New("сессии нужны часы")
	}
	return &Service{deps: *deps}, nil
}

// Item — карточка, готовая к показу.
type Item struct {
	Card   study.Card
	Lexeme lexicon.Lexeme
	// Translations — все допустимые переводы, основной первым. В режиме
	// ввода текстом засчитывается любой из них.
	Translations []lexicon.Translation
	// Mode — как спрашивать это слово.
	Mode study.Mode
}

// Reason объясняет, почему карточек нет.
type Reason uint8

// Причины остановки.
const (
	// ReasonNone — карточка есть, останавливаться не из-за чего.
	ReasonNone Reason = iota
	// ReasonCaughtUp — всё, что было к повторению, повторено, а новые
	// слова кончились в колоде.
	ReasonCaughtUp
	// ReasonDailyLimit — дневной лимит выбран. Завтра будет ещё.
	ReasonDailyLimit
	// ReasonPaused — курс на паузе.
	ReasonPaused
)

// Next возвращает следующую карточку курса.
//
// Порядок выдачи задан сроком показа и потому получается сам собой:
// просроченные повторения ждут дольше всех, за ними карточки на шагах
// обучения, и только потом новые слова, которым срок ставится текущим
// моментом.
func (s *Service) Next(ctx context.Context, courseID study.CourseID) (Item, Reason, error) {
	course, err := s.deps.Courses.ByID(ctx, courseID)
	if err != nil {
		return Item{}, ReasonNone, fmt.Errorf("найти курс: %w", err)
	}
	if !course.IsActive() {
		return Item{}, ReasonPaused, nil
	}

	settings, err := s.deps.Settings.Get(ctx, user.ID(course.UserID))
	if err != nil {
		return Item{}, ReasonNone, fmt.Errorf("прочитать настройки: %w", err)
	}

	now := s.deps.Clock.Now()
	day := settings.DayStart(now)

	counter, err := s.deps.Counters.Get(ctx, courseID, day)
	if err != nil {
		return Item{}, ReasonNone, fmt.Errorf("прочитать дневные счётчики: %w", err)
	}

	// Дневной потолок считает все ответы, а не только повторения: человек,
	// настроивший «не больше пятидесяти карточек в день», имеет в виду
	// пятьдесят карточек, а не пятьдесят плюс новые сверху.
	if counter.ReviewsDone >= settings.MaxReviewsPerDay {
		return Item{}, ReasonDailyLimit, nil
	}

	due, err := s.deps.Cards.Due(ctx, port.DueQuery{CourseID: courseID, Now: now, Limit: 1})
	if err != nil {
		return Item{}, ReasonNone, fmt.Errorf("получить карточки к повторению: %w", err)
	}
	if len(due) > 0 {
		return s.prepare(ctx, &course, &settings, &due[0])
	}

	if counter.NewIntroduced >= settings.NewPerDay {
		return Item{}, ReasonDailyLimit, nil
	}

	// Новое слово вводится по одному и ровно тогда, когда мы собираемся
	// его показать: иначе человек, бросивший занятие после первой карточки,
	// потратил бы весь дневной лимит на слова, которых не видел.
	introduced, err := s.deps.Cards.IntroduceNew(ctx, port.IntroduceQuery{
		CourseID: courseID,
		Now:      now,
		Day:      day,
		Limit:    settings.NewPerDay,
		Batch:    1,
	})
	if err != nil {
		return Item{}, ReasonNone, fmt.Errorf("ввести новое слово: %w", err)
	}
	if len(introduced) == 0 {
		// Лимит не выбран, но колода кончилась.
		return Item{}, ReasonCaughtUp, nil
	}
	return s.prepare(ctx, &course, &settings, &introduced[0])
}

// prepare достаёт слово и переводы и выбирает режим проверки.
func (s *Service) prepare(ctx context.Context, course *study.Course, settings *user.Settings, card *study.Card) (Item, Reason, error) {
	lexemes, err := s.deps.Lexemes.ByIDs(ctx, []lexicon.LexemeID{card.LexemeID})
	if err != nil {
		return Item{}, ReasonNone, fmt.Errorf("получить слово: %w", err)
	}
	if len(lexemes) == 0 {
		return Item{}, ReasonNone, fmt.Errorf("слово %d карточки %d: %w", card.LexemeID, card.ID, port.ErrNotFound)
	}

	translations, err := s.deps.Lexemes.Translations(ctx, []lexicon.LexemeID{card.LexemeID}, course.TranslationLang)
	if err != nil {
		return Item{}, ReasonNone, fmt.Errorf("получить переводы: %w", err)
	}
	if len(translations[card.LexemeID]) == 0 {
		// Карточка без перевода — это карточка, на которой нечего показать.
		// Такое возможно, если словарь пополнили криво; пропускать её молча
		// нельзя, иначе сессия встанет на ней насмерть.
		return Item{}, ReasonNone, fmt.Errorf("у слова %d нет переводов на %s: %w",
			card.LexemeID, course.TranslationLang, port.ErrNotFound)
	}

	return Item{
		Card:         *card,
		Lexeme:       lexemes[0],
		Translations: translations[card.LexemeID],
		Mode:         PickMode(settings.QuizModes, card),
	}, ReasonNone, nil
}

// PickMode выбирает режим проверки для карточки.
//
// Выбор детерминированный — по идентификатору карточки и числу успешных
// повторений: одна и та же карточка в одном и том же состоянии всегда
// спрашивается одинаково, и тест может это проверить. Случайный выбор
// давал бы то же чередование, но проверить его было бы нечем.
//
// Новое слово всегда показывается узнаванием: напечатать перевод слова,
// которого человек ни разу не видел, невозможно, и спрашивать его так —
// значит гарантированно засчитать провал.
func PickMode(enabled []study.Mode, card *study.Card) study.Mode {
	if len(enabled) == 0 {
		return study.ModeRecall
	}
	if card.State == study.StateNew || card.IsNew() {
		if contains(enabled, study.ModeRecall) {
			return study.ModeRecall
		}
		return enabled[0]
	}

	index := (int64(card.ID) + int64(card.Repetitions)) % int64(len(enabled))
	if index < 0 {
		index = -index
	}
	return enabled[index]
}

func contains(modes []study.Mode, wanted study.Mode) bool {
	for _, mode := range modes {
		if mode == wanted {
			return true
		}
	}
	return false
}
