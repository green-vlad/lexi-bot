// Package courses ведёт курсы пользователя: список, переключение между
// ними, пауза и архив.
//
// Курс — это «колода плюс язык перевода», и их у человека может быть
// несколько: корейский по учебнику и английский для работы. Занятие идёт
// по одному курсу за раз, поэтому кроме списка нужен ответ на вопрос
// «что учим сейчас».
package courses

import (
	"context"
	"errors"
	"fmt"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// Deps — зависимости сценария.
type Deps struct {
	Users   port.UserRepo
	Courses port.CourseRepo
	Decks   port.DeckRepo
	Cards   port.CardRepo
}

// Service ведёт курсы.
type Service struct {
	deps Deps
}

// New создаёт сценарий.
func New(deps Deps) (*Service, error) {
	switch {
	case deps.Users == nil:
		return nil, errors.New("курсам нужен UserRepo")
	case deps.Courses == nil:
		return nil, errors.New("курсам нужен CourseRepo")
	case deps.Decks == nil:
		return nil, errors.New("курсам нужен DeckRepo")
	case deps.Cards == nil:
		return nil, errors.New("курсам нужен CardRepo")
	}
	return &Service{deps: deps}, nil
}

// Summary — курс в том виде, в каком его показывают в списке.
type Summary struct {
	Course study.Course
	Deck   lexicon.Deck
	// Current отмечает курс, которым занимаются сейчас.
	Current bool
	// Learned и Total — сколько слов уже введено и сколько всего в колоде.
	// Без этих чисел список курсов — просто список названий.
	Learned int
	Total   int
}

// List возвращает курсы пользователя.
func (s *Service) List(ctx context.Context, userID user.ID) ([]Summary, error) {
	known, err := s.deps.Users.ByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("найти пользователя: %w", err)
	}

	list, err := s.deps.Courses.ByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("получить курсы: %w", err)
	}

	current := s.resolveCurrent(known.CurrentCourse, list)

	out := make([]Summary, 0, len(list))
	for i := range list {
		deck, err := s.deps.Decks.ByID(ctx, list[i].DeckID)
		if err != nil {
			return nil, fmt.Errorf("найти колоду курса %d: %w", list[i].ID, err)
		}

		counts, err := s.deps.Cards.CountsByState(ctx, list[i].ID)
		if err != nil {
			return nil, fmt.Errorf("посчитать карточки курса %d: %w", list[i].ID, err)
		}

		learned := 0
		for _, count := range counts {
			learned += count
		}

		out = append(out, Summary{
			Course:  list[i],
			Deck:    deck,
			Current: list[i].ID == current,
			Learned: learned,
			Total:   deck.Size,
		})
	}
	return out, nil
}

// Current возвращает курс, по которому идёт занятие.
//
// Если выбор не сделан или выбранный курс больше не активен, берётся любой
// активный: человек, поставивший текущий курс на паузу, ждёт, что занятие
// продолжится по оставшемуся, а не упрётся в «учить нечего».
func (s *Service) Current(ctx context.Context, userID user.ID) (study.Course, bool, error) {
	known, err := s.deps.Users.ByID(ctx, userID)
	if err != nil {
		return study.Course{}, false, fmt.Errorf("найти пользователя: %w", err)
	}

	list, err := s.deps.Courses.ByUser(ctx, userID)
	if err != nil {
		return study.Course{}, false, fmt.Errorf("получить курсы: %w", err)
	}

	current := s.resolveCurrent(known.CurrentCourse, list)
	for i := range list {
		if list[i].ID == current {
			return list[i], true, nil
		}
	}
	return study.Course{}, false, nil
}

// resolveCurrent выбирает курс для занятия.
func (s *Service) resolveCurrent(chosen study.CourseID, list []study.Course) study.CourseID {
	for i := range list {
		if list[i].ID == chosen && list[i].IsActive() {
			return chosen
		}
	}
	for i := range list {
		if list[i].IsActive() {
			return list[i].ID
		}
	}
	return 0
}

// SetCurrent переключает курс, по которому идёт занятие.
//
// Приостановленный курс при этом снова становится активным: человек,
// нажавший «учить», хочет учить, а не получить объяснение, что курс
// на паузе и сперва надо нажать другую кнопку.
func (s *Service) SetCurrent(ctx context.Context, userID user.ID, courseID study.CourseID) error {
	course, err := s.owned(ctx, userID, courseID)
	if err != nil {
		return err
	}

	if !course.IsActive() {
		if err := s.deps.Courses.SetStatus(ctx, courseID, study.CourseActive); err != nil {
			return fmt.Errorf("возобновить курс: %w", err)
		}
	}
	if err := s.deps.Users.SetCurrentCourse(ctx, userID, courseID); err != nil {
		return fmt.Errorf("запомнить текущий курс: %w", err)
	}
	return nil
}

// SetStatus ставит курс на паузу, возобновляет или убирает в архив.
func (s *Service) SetStatus(ctx context.Context, userID user.ID, courseID study.CourseID, status study.CourseStatus) error {
	if _, err := s.owned(ctx, userID, courseID); err != nil {
		return err
	}
	if !status.IsValid() {
		return fmt.Errorf("состояние %q: %w", status, study.ErrInvalid)
	}

	if err := s.deps.Courses.SetStatus(ctx, courseID, status); err != nil {
		return fmt.Errorf("сменить состояние курса: %w", err)
	}
	return nil
}

// PauseAll останавливает все активные курсы разом.
//
// Это /pause: человек уезжает в отпуск или просто устал, и ему нужно одно
// действие, а не обход списка курсов по одному. Архивные не трогаются —
// они убраны насовсем, и «продолжить» возвращать их не должно.
func (s *Service) PauseAll(ctx context.Context, userID user.ID) (int, error) {
	return s.switchAll(ctx, userID, study.CourseActive, study.CoursePaused)
}

// ResumeAll возвращает в строй всё, что стоит на паузе.
//
// Различить, что остановлено командой, а что руками в /decks, нельзя:
// в базе это одно состояние. Поэтому возвращается всё, а человеку говорится
// сколько — чтобы он заметил, если вернулось лишнее.
func (s *Service) ResumeAll(ctx context.Context, userID user.ID) (int, error) {
	return s.switchAll(ctx, userID, study.CoursePaused, study.CourseActive)
}

// switchAll переводит все курсы из одного состояния в другое.
func (s *Service) switchAll(ctx context.Context, userID user.ID, from, to study.CourseStatus) (int, error) {
	list, err := s.deps.Courses.ByUser(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("получить курсы: %w", err)
	}

	changed := 0
	for i := range list {
		if list[i].Status != from {
			continue
		}
		if err := s.deps.Courses.SetStatus(ctx, list[i].ID, to); err != nil {
			return 0, fmt.Errorf("сменить состояние курса %d: %w", list[i].ID, err)
		}
		changed++
	}
	return changed, nil
}

// owned убеждается, что курс принадлежит этому пользователю.
//
// Идентификатор курса приезжает из кнопки, а кнопку можно подделать: без
// проверки чужой курс можно было бы поставить на паузу или увести себе.
func (s *Service) owned(ctx context.Context, userID user.ID, courseID study.CourseID) (study.Course, error) {
	course, err := s.deps.Courses.ByID(ctx, courseID)
	if err != nil {
		return study.Course{}, fmt.Errorf("найти курс: %w", err)
	}
	if course.UserID != int64(userID) {
		return study.Course{}, fmt.Errorf("курс %d: %w", courseID, port.ErrNotFound)
	}
	return course, nil
}
