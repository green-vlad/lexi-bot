package session_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/internal/usecase/session"
)

// applied запоминает записанные ответы: важно не только что вернул сценарий,
// но и что он попросил записать.
type applied struct {
	outcomes []port.ReviewOutcome
}

func (f *fakeCards) applyOutcome(outcome *port.ReviewOutcome) error {
	// Проверка версии: ровно то, что делает база условием по last_reviewed_at.
	for i := range f.cards {
		if f.cards[i].ID != outcome.CardID {
			continue
		}
		if !f.cards[i].LastReviewedAt.Equal(outcome.ExpectedLastReviewedAt) {
			return port.ErrConflict
		}

		f.cards[i].CardState = outcome.State
		f.cards[i].LastReviewedAt = outcome.Review.RatedAt
		f.applied.outcomes = append(f.applied.outcomes, *outcome)
		return nil
	}
	return port.ErrNotFound
}

// answerFixture — сессия, готовая принимать ответы.
type answerFixture struct {
	*fixture
	card study.Card
}

func newAnswerFixture(t *testing.T, mode study.Mode) *answerFixture {
	t.Helper()

	f := newFixture(t, 3)
	f.settings.settings.QuizModes = []study.Mode{mode}

	item, reason := f.next(t)
	if reason != session.ReasonNone {
		t.Fatalf("карточки нет: %v", reason)
	}
	return &answerFixture{fixture: f, card: item.Card}
}

func (a *answerFixture) submit(t *testing.T, answer session.Answer) session.Outcome {
	t.Helper()

	if answer.CardID == 0 {
		answer.CardID = a.card.ID
	}
	if answer.Attempt == "" {
		answer.Attempt = session.Attempt(&a.card)
	}

	outcome, err := a.service.Submit(context.Background(), answer)
	if err != nil {
		t.Fatalf("Submit() вернул ошибку: %v", err)
	}
	return outcome
}

func TestSubmitTypingExactAnswer(t *testing.T) {
	t.Parallel()

	f := newAnswerFixture(t, study.ModeTyping)

	outcome := f.submit(t, session.Answer{Mode: study.ModeTyping, Text: "перевод"})

	if outcome.Rating != study.RatingGood {
		t.Errorf("оценка = %v, ожидалось good", outcome.Rating)
	}
	if !outcome.Correct {
		t.Error("точный ответ должен быть засчитан")
	}
	if outcome.Match != lexicon.MatchExact {
		t.Errorf("совпадение = %v, ожидалось точное", outcome.Match)
	}
	if outcome.Card.State != study.StateLearning {
		t.Errorf("фаза = %v, ожидалось обучение: карточка была новой", outcome.Card.State)
	}

	// Записалось всё сразу: карточка, журнал и признак верного ответа.
	if len(f.cards.applied.outcomes) != 1 {
		t.Fatalf("записей %d, ожидалась одна", len(f.cards.applied.outcomes))
	}
	written := f.cards.applied.outcomes[0]
	if written.Review.Mode != study.ModeTyping || written.Review.AnswerRaw != "перевод" {
		t.Errorf("в журнале %+v", written.Review)
	}
	if !written.Review.IsCorrect {
		t.Error("в журнале ответ не засчитан")
	}
	if written.Day.IsZero() {
		t.Error("сутки для дневного счётчика не переданы")
	}
}

func TestSubmitTypingTypo(t *testing.T) {
	t.Parallel()

	f := newAnswerFixture(t, study.ModeTyping)

	// Опечатка в один символ засчитывается, но со скидкой: «трудно».
	outcome := f.submit(t, session.Answer{Mode: study.ModeTyping, Text: "перевоД"})
	if outcome.Rating != study.RatingGood {
		t.Errorf("оценка = %v: разница только в регистре, это точный ответ", outcome.Rating)
	}

	f2 := newAnswerFixture(t, study.ModeTyping)
	outcome = f2.submit(t, session.Answer{Mode: study.ModeTyping, Text: "перевоp"})
	if outcome.Rating != study.RatingHard {
		t.Errorf("оценка = %v, ожидалось hard", outcome.Rating)
	}
	if !outcome.Correct {
		t.Error("опечатка должна засчитываться")
	}
}

func TestSubmitTypingWrongAnswer(t *testing.T) {
	t.Parallel()

	f := newAnswerFixture(t, study.ModeTyping)

	outcome := f.submit(t, session.Answer{Mode: study.ModeTyping, Text: "совершенно другое"})

	if outcome.Rating != study.RatingAgain {
		t.Errorf("оценка = %v, ожидалось again", outcome.Rating)
	}
	if outcome.Correct {
		t.Error("неверный ответ не должен засчитываться")
	}
	// Пользователю показывают, как было правильно.
	if len(outcome.Expected) == 0 || outcome.Expected[0] != "перевод" {
		t.Errorf("допустимые ответы = %v", outcome.Expected)
	}
}

func TestSubmitChoiceCountsSpeed(t *testing.T) {
	t.Parallel()

	fast := newAnswerFixture(t, study.ModeChoice)
	outcome := fast.submit(t, session.Answer{Mode: study.ModeChoice, Correct: true, Elapsed: time.Second})
	if outcome.Rating != study.RatingEasy {
		t.Errorf("быстрый верный ответ = %v, ожидалось easy", outcome.Rating)
	}

	slow := newAnswerFixture(t, study.ModeChoice)
	outcome = slow.submit(t, session.Answer{Mode: study.ModeChoice, Correct: true, Elapsed: 10 * time.Second})
	if outcome.Rating != study.RatingGood {
		t.Errorf("медленный верный ответ = %v, ожидалось good", outcome.Rating)
	}

	wrong := newAnswerFixture(t, study.ModeChoice)
	outcome = wrong.submit(t, session.Answer{Mode: study.ModeChoice, Correct: false, Elapsed: time.Second})
	if outcome.Rating != study.RatingAgain {
		t.Errorf("неверный ответ = %v, ожидалось again", outcome.Rating)
	}
	// В журнал текста не попадает: пользователь ничего не печатал.
	if written := wrong.cards.applied.outcomes[0].Review; written.AnswerRaw != "" {
		t.Errorf("в журнале текст ответа %q, а печатать было негде", written.AnswerRaw)
	}
}

func TestSubmitFailureMovesToRelearning(t *testing.T) {
	t.Parallel()

	f := newAnswerFixture(t, study.ModeChoice)

	// Доводим карточку до выученного состояния, чтобы провал имел смысл.
	for i := range f.cards.cards {
		if f.cards.cards[i].ID == f.card.ID {
			f.cards.cards[i].CardState = study.CardState{
				State: study.StateReview, DueAt: f.now, IntervalDays: 10,
				EaseFactor: 2.5, Repetitions: 4,
			}
			f.card = f.cards.cards[i]
		}
	}

	outcome := f.submit(t, session.Answer{Mode: study.ModeChoice, Correct: false})

	if outcome.Card.State != study.StateRelearning {
		t.Errorf("фаза = %v, ожидалось переобучение", outcome.Card.State)
	}
	if outcome.Card.Lapses != 1 {
		t.Errorf("провалов = %d, ожидался один", outcome.Card.Lapses)
	}
	if outcome.Card.IntervalDays != 5 {
		t.Errorf("интервал = %v, ожидалась половина прежнего", outcome.Card.IntervalDays)
	}
	if outcome.Correct {
		t.Error("провал не должен засчитываться как верный ответ")
	}
}

func TestSubmitIsIdempotent(t *testing.T) {
	t.Parallel()

	f := newAnswerFixture(t, study.ModeChoice)
	answer := session.Answer{Mode: study.ModeChoice, Correct: true, Elapsed: 5 * time.Second}

	first := f.submit(t, answer)
	if first.Duplicate {
		t.Fatal("первый ответ не может быть повтором")
	}

	// Второе нажатие той же кнопки: токен попытки остался прежним, а карточка
	// уже уехала. Ничего не должно измениться.
	second := f.submit(t, answer)
	if !second.Duplicate {
		t.Error("повторный ответ должен распознаваться как повтор")
	}

	if len(f.cards.applied.outcomes) != 1 {
		t.Errorf("записей %d, ожидалась одна: второй ответ не должен писать в журнал",
			len(f.cards.applied.outcomes))
	}

	card, err := f.cards.ByID(context.Background(), f.card.ID)
	if err != nil {
		t.Fatalf("ByID() вернул ошибку: %v", err)
	}
	if !card.DueAt.Equal(first.Card.DueAt) {
		t.Errorf("срок сдвинулся дважды: %v против %v", card.DueAt, first.Card.DueAt)
	}
	if card.Repetitions != first.Card.Repetitions {
		t.Errorf("повторений %d, ожидалось %d", card.Repetitions, first.Card.Repetitions)
	}
}

func TestSubmitDetectsRaceOnApply(t *testing.T) {
	t.Parallel()

	f := newAnswerFixture(t, study.ModeChoice)

	// Кто-то ответил на карточку, пока мы считали: токен сходится, а версия
	// в базе уже другая. Проверка при записи — последняя линия обороны.
	stale := session.Attempt(&f.card)
	for i := range f.cards.cards {
		if f.cards.cards[i].ID == f.card.ID {
			f.cards.cards[i].LastReviewedAt = f.now.Add(-time.Second)
		}
	}
	f.card.LastReviewedAt = time.Time{}

	outcome, err := f.service.Submit(context.Background(), session.Answer{
		CardID: f.card.ID, Attempt: stale, Mode: study.ModeChoice, Correct: true,
	})
	if err != nil {
		t.Fatalf("Submit() вернул ошибку: %v", err)
	}
	if !outcome.Duplicate {
		t.Error("ответ на устаревшую версию карточки должен считаться повтором")
	}
	if len(f.cards.applied.outcomes) != 0 {
		t.Error("устаревший ответ не должен попадать в журнал")
	}
}

func TestSubmitAcceptsAnswerWithoutAttempt(t *testing.T) {
	t.Parallel()

	// Ответ текстом приходит обычным сообщением, а не кнопкой, и токена
	// в нём взяться неоткуда. Такой ответ принимается: защитой остаётся
	// проверка версии при записи.
	f := newAnswerFixture(t, study.ModeTyping)

	outcome, err := f.service.Submit(context.Background(), session.Answer{
		CardID: f.card.ID, Mode: study.ModeTyping, Text: "перевод",
	})
	if err != nil {
		t.Fatalf("Submit() вернул ошибку: %v", err)
	}
	if outcome.Duplicate {
		t.Error("ответ без токена не должен считаться повтором")
	}
	if len(f.cards.applied.outcomes) != 1 {
		t.Errorf("записей %d, ожидалась одна", len(f.cards.applied.outcomes))
	}
}

func TestSubmitRejectsUnknownCard(t *testing.T) {
	t.Parallel()

	f := newAnswerFixture(t, study.ModeChoice)

	_, err := f.service.Submit(context.Background(), session.Answer{
		CardID: 99999, Mode: study.ModeChoice, Correct: true,
	})
	if !errors.Is(err, port.ErrNotFound) {
		t.Errorf("Submit() = %v, ожидалась ErrNotFound", err)
	}
}

func TestSubmitRejectsBrokenAnswer(t *testing.T) {
	t.Parallel()

	f := newAnswerFixture(t, study.ModeChoice)

	// Ответ без режима: хендлер собрал его неправильно, и превратить
	// такой ответ в оценку не из чего.
	_, err := f.service.Submit(context.Background(), session.Answer{
		CardID: f.card.ID, Attempt: session.Attempt(&f.card),
	})
	if err == nil {
		t.Error("ответ без режима должен быть ошибкой")
	}
	if err != nil && !strings.Contains(err.Error(), "mode") {
		t.Errorf("ошибка = %v, ожидалось объяснение про режим", err)
	}
}

func TestAttemptChangesAfterAnswer(t *testing.T) {
	t.Parallel()

	card := study.Card{ID: 7}
	before := session.Attempt(&card)

	card.LastReviewedAt = time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	after := session.Attempt(&card)

	if before == after {
		t.Error("после ответа токен попытки обязан меняться")
	}
	if len(after) > 8 {
		t.Errorf("токен %q длиннее восьми байт: в callback_data их всего 64", after)
	}
}
