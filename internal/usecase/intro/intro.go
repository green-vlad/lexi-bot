// Package intro ведёт знакомство с новыми словами.
//
// Знакомство отделено от повторения намеренно. Спрашивать перевод слова,
// которое человек видит впервые, бессмысленно: любой его ответ — угадывание,
// а провал в первую же секунду знакомства ничему не учит. Поэтому новое
// слово сначала показывают целиком — написание, чтение, перевод, пример —
// и спрашивают не перевод, а решение: начать учить, отметить знакомым
// или отложить.
package intro

import (
	"context"
	"errors"
	"fmt"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// Deps — зависимости знакомства.
type Deps struct {
	Cards    port.CardRepo
	Counters port.CounterRepo
	Courses  port.CourseRepo
	Settings port.SettingsRepo
	Lexemes  port.LexemeRepo
	Clock    port.Clock
	// Scheduler считает первый срок показа слова, которое человек решил
	// учить: знакомство не выдумывает интервалы само.
	Scheduler study.Scheduler
}

// Service — сценарий знакомства с новыми словами.
type Service struct {
	deps Deps
}

// New создаёт сценарий.
func New(deps *Deps) (*Service, error) {
	if deps == nil {
		return nil, errors.New("знакомству нужны зависимости")
	}

	switch {
	case deps.Cards == nil:
		return nil, errors.New("знакомству нужен CardRepo")
	case deps.Counters == nil:
		return nil, errors.New("знакомству нужен CounterRepo")
	case deps.Courses == nil:
		return nil, errors.New("знакомству нужен CourseRepo")
	case deps.Settings == nil:
		return nil, errors.New("знакомству нужен SettingsRepo")
	case deps.Lexemes == nil:
		return nil, errors.New("знакомству нужен LexemeRepo")
	case deps.Clock == nil:
		return nil, errors.New("знакомству нужны часы")
	case deps.Scheduler == nil:
		return nil, errors.New("знакомству нужен планировщик")
	}
	return &Service{deps: *deps}, nil
}

// Word — новое слово, готовое к показу.
type Word struct {
	Lexeme lexicon.Lexeme
	// Translations — все переводы слова, основной первым.
	Translations []lexicon.Translation
}

// Reason объясняет, почему новых слов нет.
type Reason uint8

// Причины остановки.
const (
	// ReasonNone — слово есть, останавливаться не из-за чего.
	ReasonNone Reason = iota
	// ReasonDailyLimit — дневная норма новых слов выбрана.
	ReasonDailyLimit
	// ReasonDeckDone — в колоде не осталось слов, которых человек не видел.
	ReasonDeckDone
	// ReasonPaused — курс на паузе.
	ReasonPaused
)

// Next возвращает следующее новое слово курса.
func (s *Service) Next(ctx context.Context, courseID study.CourseID) (Word, Reason, error) {
	course, settings, err := s.context(ctx, courseID)
	if err != nil {
		return Word{}, ReasonNone, err
	}
	if !course.IsActive() {
		return Word{}, ReasonPaused, nil
	}

	left, err := s.left(ctx, courseID, &settings)
	if err != nil {
		return Word{}, ReasonNone, err
	}
	if left <= 0 {
		return Word{}, ReasonDailyLimit, nil
	}

	ids, err := s.deps.Cards.NewWords(ctx, port.NewWordQuery{
		CourseID: courseID,
		Now:      s.deps.Clock.Now(),
		Limit:    1,
	})
	if err != nil {
		return Word{}, ReasonNone, fmt.Errorf("получить новые слова: %w", err)
	}
	if len(ids) == 0 {
		return Word{}, ReasonDeckDone, nil
	}

	word, err := s.word(ctx, &course, ids[0])
	if err != nil {
		return Word{}, ReasonNone, err
	}
	return word, ReasonNone, nil
}

// Available сообщает, сколько новых слов человек может посмотреть сейчас.
//
// Меню занятия обещает число, и оно должно совпадать с тем, что человек
// увидит: и остаток дневной нормы, и остаток колоды тут учтены.
func (s *Service) Available(ctx context.Context, courseID study.CourseID) (int, error) {
	course, settings, err := s.context(ctx, courseID)
	if err != nil {
		return 0, err
	}
	if !course.IsActive() {
		return 0, nil
	}

	left, err := s.left(ctx, courseID, &settings)
	if err != nil {
		return 0, err
	}
	if left <= 0 {
		return 0, nil
	}

	ids, err := s.deps.Cards.NewWords(ctx, port.NewWordQuery{
		CourseID: courseID,
		Now:      s.deps.Clock.Now(),
		Limit:    left,
	})
	if err != nil {
		return 0, fmt.Errorf("получить новые слова: %w", err)
	}
	return len(ids), nil
}

// Remember — «запомнил»: слово уходит на первый шаг обучения и вернётся
// повторением.
//
// Второе значение — false, если дневная норма кончилась между показом слова
// и нажатием кнопки: слово осталось новым, и человеку надо сказать, что
// на сегодня хватит.
func (s *Service) Remember(ctx context.Context, courseID study.CourseID, lexemeID lexicon.LexemeID) (bool, error) {
	_, settings, err := s.context(ctx, courseID)
	if err != nil {
		return false, err
	}

	now := s.deps.Clock.Now()
	// Первый срок считает планировщик по своей таблице шагов: знакомство
	// не должно знать, что первый шаг — минута, а не десять.
	first := s.deps.Scheduler.Next(study.CardState{
		State:      study.StateNew,
		DueAt:      now,
		EaseFactor: study.DefaultEaseFactor,
	}, study.RatingGood, now)

	_, accepted, err := s.deps.Cards.StartLearning(ctx, &port.StartLearningQuery{
		CourseID: courseID,
		LexemeID: lexemeID,
		State:    first,
		Now:      now,
		Day:      settings.DayStart(now),
		Limit:    settings.NewPerDay,
	})
	if err != nil {
		return false, fmt.Errorf("начать учить слово: %w", err)
	}
	return accepted, nil
}

// AlreadyKnow — «я уже знаю это слово»: оно не попадёт ни в знакомство,
// ни в повторения.
func (s *Service) AlreadyKnow(ctx context.Context, courseID study.CourseID, lexemeID lexicon.LexemeID) error {
	if err := s.deps.Cards.MarkKnown(ctx, courseID, lexemeID, s.deps.Clock.Now()); err != nil {
		return fmt.Errorf("пометить слово выученным: %w", err)
	}
	return nil
}

// Skip — «пропустить»: слово уходит из знакомства до следующих суток.
//
// Именно до суток, а не до следующего слова: пропуск означает «не сейчас»,
// и вернуть слово через минуту значило бы не услышать человека.
func (s *Service) Skip(ctx context.Context, courseID study.CourseID, lexemeID lexicon.LexemeID) error {
	_, settings, err := s.context(ctx, courseID)
	if err != nil {
		return err
	}

	now := s.deps.Clock.Now()
	if err := s.deps.Cards.PostponeNew(ctx, courseID, lexemeID, now, settings.NextDayStart(now)); err != nil {
		return fmt.Errorf("отложить слово: %w", err)
	}
	return nil
}

// context достаёт курс и настройки его владельца.
func (s *Service) context(ctx context.Context, courseID study.CourseID) (study.Course, user.Settings, error) {
	course, err := s.deps.Courses.ByID(ctx, courseID)
	if err != nil {
		return study.Course{}, user.Settings{}, fmt.Errorf("найти курс: %w", err)
	}

	settings, err := s.deps.Settings.Get(ctx, user.ID(course.UserID))
	if err != nil {
		return study.Course{}, user.Settings{}, fmt.Errorf("прочитать настройки: %w", err)
	}
	return course, settings, nil
}

// left считает остаток дневной нормы новых слов.
func (s *Service) left(ctx context.Context, courseID study.CourseID, settings *user.Settings) (int, error) {
	day := settings.DayStart(s.deps.Clock.Now())

	counter, err := s.deps.Counters.Get(ctx, courseID, day)
	if err != nil {
		return 0, fmt.Errorf("прочитать дневные счётчики: %w", err)
	}
	return settings.NewPerDay - counter.NewIntroduced, nil
}

// word собирает слово к показу: само слово и его переводы.
func (s *Service) word(ctx context.Context, course *study.Course, id lexicon.LexemeID) (Word, error) {
	lexemes, err := s.deps.Lexemes.ByIDs(ctx, []lexicon.LexemeID{id})
	if err != nil {
		return Word{}, fmt.Errorf("получить слово: %w", err)
	}
	if len(lexemes) == 0 {
		return Word{}, fmt.Errorf("слово %d: %w", id, port.ErrNotFound)
	}

	translations, err := s.deps.Lexemes.Translations(ctx, []lexicon.LexemeID{id}, course.TranslationLang)
	if err != nil {
		return Word{}, fmt.Errorf("получить переводы: %w", err)
	}
	if len(translations[id]) == 0 {
		// Слово без перевода показывать нечем: карточка знакомства состоит
		// из него наполовину.
		return Word{}, fmt.Errorf("у слова %d нет переводов на %s: %w",
			id, course.TranslationLang, port.ErrNotFound)
	}

	return Word{Lexeme: lexemes[0], Translations: translations[id]}, nil
}
