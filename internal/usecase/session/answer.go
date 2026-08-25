package session

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// Answer — ответ пользователя на карточку.
type Answer struct {
	CardID study.CardID
	// Attempt — токен попытки, выданный вместе с карточкой. По нему
	// отличается второе нажатие той же кнопки от honest ответа на новый
	// показ той же карточки.
	Attempt string
	Mode    study.Mode
	// Text — что человек напечатал (режим ввода).
	Text string
	// Correct — выбран ли правильный вариант (режим выбора).
	Correct bool
	// SelfRating — как человек оценил себя сам (режим узнавания).
	SelfRating study.Rating
	// Elapsed — сколько времени занял ответ.
	Elapsed time.Duration
}

// Outcome — что вышло из ответа.
type Outcome struct {
	Rating study.Rating
	// Correct — засчитан ли ответ.
	Correct bool
	// Match — насколько введённый текст совпал с переводом (режим ввода).
	Match lexicon.Match
	// Expected — допустимые переводы: их показывают вместе с разбором.
	Expected []string
	// Card — состояние карточки после ответа.
	Card study.CardState
	// CourseID — курс отвеченной карточки: по нему сессия продолжается
	// следующей карточкой, и спрашивать его отдельно незачем.
	CourseID study.CourseID
	// CardID — сама карточка: разбор промаха показывает её слово.
	CardID study.CardID
	// Duplicate означает, что этот ответ уже был учтён, и ничего
	// не изменилось. Обычно это второе нажатие той же кнопки.
	Duplicate bool
}

// Attempt возвращает токен попытки для карточки.
//
// Токен — это версия карточки, а не случайное число: момент прошлого ответа
// меняется при каждом ответе, поэтому кнопка, выданная до ответа, перестаёт
// подходить сразу после него. Случайный токен пришлось бы где-то хранить —
// то есть писать в базу на каждый показ карточки.
func Attempt(card *study.Card) string {
	if card.LastReviewedAt.IsZero() {
		return "0"
	}
	// Тридцать шестеричная запись: в callback_data 64 байта на всё,
	// и десятичные секунды эпохи тратят их зря.
	return strconv.FormatInt(card.LastReviewedAt.Unix(), 36)
}

// Submit принимает ответ: проверяет его, считает оценку, двигает карточку
// и записывает результат.
//
// Запись идёт одной транзакцией — карточка, журнал и дневной счётчик.
// Порознь это выглядело бы так: карточка уехала на месяц вперёд, а журнал
// об этом не знает, и статистика начала бы врать при первой же ошибке сети.
func (s *Service) Submit(ctx context.Context, answer Answer) (Outcome, error) {
	card, err := s.deps.Cards.ByID(ctx, answer.CardID)
	if err != nil {
		return Outcome{}, fmt.Errorf("найти карточку: %w", err)
	}

	course, err := s.deps.Courses.ByID(ctx, card.CourseID)
	if err != nil {
		return Outcome{}, fmt.Errorf("найти курс: %w", err)
	}

	// Токен проверяется до всякой работы: если он не подходит, на карточку
	// уже ответили, и считать заново нечего.
	if answer.Attempt != "" && answer.Attempt != Attempt(&card) {
		return Outcome{Duplicate: true, Card: card.CardState, CourseID: card.CourseID}, nil
	}

	settings, err := s.deps.Settings.Get(ctx, user.ID(course.UserID))
	if err != nil {
		return Outcome{}, fmt.Errorf("прочитать настройки: %w", err)
	}

	// Карточка собирается заново тем же способом, что и при показе: ответ
	// проверяется против того, что человеку показали, а не против того,
	// что мы считаем правильным сегодня.
	item, err := s.card(ctx, &course, &settings, &card)
	if err != nil {
		return Outcome{}, err
	}

	accepted := item.Answer
	if len(accepted) == 0 {
		return Outcome{}, fmt.Errorf("у карточки %d нет верного ответа: %w", card.ID, port.ErrNotFound)
	}

	graded, err := s.grade(answer, accepted, item.AnswerLang)
	if err != nil {
		return Outcome{}, err
	}

	now := s.deps.Clock.Now()
	next := s.deps.Scheduler.Next(card.CardState, graded.Rating, now)

	review, err := study.NewReview(study.ReviewParams{
		CardID:    card.ID,
		RatedAt:   now,
		Rating:    graded.Rating,
		Mode:      answer.Mode,
		AnswerRaw: typedText(answer),
		IsCorrect: graded.Correct,
		Duration:  answer.Elapsed,
		Prev:      card.CardState,
		Next:      next,
	})
	if err != nil {
		return Outcome{}, err
	}

	err = s.deps.Cards.Apply(ctx, &port.ReviewOutcome{
		CardID:                 card.ID,
		State:                  next,
		Review:                 review,
		UserID:                 user.ID(course.UserID),
		Day:                    settings.DayStart(now),
		ExpectedLastReviewedAt: card.LastReviewedAt,
	})
	if errors.Is(err, port.ErrConflict) {
		// Кто-то успел ответить на эту карточку, пока мы считали. На практике
		// это два нажатия подряд, обработанные разными горутинами.
		return Outcome{Duplicate: true, Card: card.CardState, CourseID: card.CourseID, Expected: accepted}, nil
	}
	if err != nil {
		return Outcome{}, fmt.Errorf("записать ответ: %w", err)
	}

	return Outcome{
		Rating:   graded.Rating,
		Correct:  graded.Correct,
		Match:    graded.Match,
		Expected: accepted,
		Card:     next,
		CourseID: card.CourseID,
		CardID:   card.ID,
	}, nil
}

// graded — результат проверки ответа до того, как он стал оценкой.
type graded struct {
	Rating  study.Rating
	Correct bool
	Match   lexicon.Match
}

// grade проверяет ответ и превращает его в оценку.
func (s *Service) grade(answer Answer, accepted []string, lang lexicon.Language) (graded, error) {
	resolved := study.Answer{
		Mode:       answer.Mode,
		SelfRating: answer.SelfRating,
		Correct:    answer.Correct,
		Elapsed:    answer.Elapsed,
	}

	var match lexicon.Match
	if answer.Mode == study.ModeTyping {
		check := lexicon.CheckAnswer(answer.Text, accepted, lang)
		match = check.Match
		resolved.Match = check.Match
	}

	rating, err := s.deps.Resolver.Resolve(resolved)
	if err != nil {
		return graded{}, fmt.Errorf("оценить ответ: %w", err)
	}
	return graded{Rating: rating, Correct: resolved.IsCorrect(), Match: match}, nil
}

// typedText возвращает текст ответа только там, где пользователь его
// действительно печатал: в журнале это поле бывает лишь у режима ввода.
func typedText(answer Answer) string {
	if answer.Mode == study.ModeTyping {
		return answer.Text
	}
	return ""
}
