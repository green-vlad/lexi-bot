package study

import (
	"fmt"
	"time"

	"lexi-bot/internal/domain/lexicon"
)

// DefaultFastAnswer — граница «ответил не задумываясь». Три секунды взяты
// из плана (§5): столько занимает прочитать четыре варианта и нажать нужный,
// если перевод всплыл сразу. Больше — значит, человек выбирал.
const DefaultFastAnswer = 3 * time.Second

// Answer — ответ пользователя в том виде, в каком его собрал хендлер: что
// именно считать ответом, зависит от режима.
type Answer struct {
	Mode Mode
	// SelfRating — оценка, которую пользователь выбрал сам. Только для recall:
	// там он единственный, кто знает, вспомнил ли слово.
	SelfRating Rating
	// Match — насколько введённый текст совпал с допустимыми переводами.
	// Только для typing; расстояние Левенштейна уже учтено в степени
	// совпадения (см. lexicon.CheckAnswer).
	Match lexicon.Match
	// Correct — выбран ли правильный вариант. Только для choice.
	Correct bool
	// Elapsed — сколько времени занял ответ. Ноль означает, что длительность
	// не измерялась, и быстрым такой ответ не считается.
	Elapsed time.Duration
}

// IsCorrect сообщает, засчитан ли ответ. Признак попадает в журнал повторений
// и в статистику, и считается он по-разному в каждом режиме.
func (a Answer) IsCorrect() bool {
	switch a.Mode {
	case ModeRecall:
		return a.SelfRating.IsValid() && !a.SelfRating.Failed()
	case ModeChoice:
		return a.Correct
	case ModeTyping:
		return a.Match.Accepted()
	default:
		return false
	}
}

// RatingResolver превращает ответ в оценку для планировщика.
//
// Смысл этого слоя в том, что SM-2 понимает только четыре оценки, а режимы
// проверки дают разные свидетельства: в recall человек оценивает себя сам,
// в choice мы знаем правильность и скорость, в typing — насколько текст
// разошёлся с переводом. Таблица отображения одна на всё приложение и живёт
// здесь, а не растекается по хендлерам.
type RatingResolver struct {
	fastAnswer time.Duration
}

// NewRatingResolver создаёт резолвер с заданной границей быстрого ответа.
func NewRatingResolver(fastAnswer time.Duration) (RatingResolver, error) {
	if fastAnswer <= 0 {
		return RatingResolver{}, fmt.Errorf("fast_answer = %v: %w (ожидалась положительная длительность)", fastAnswer, ErrOutOfRange)
	}
	return RatingResolver{fastAnswer: fastAnswer}, nil
}

// DefaultRatingResolver возвращает резолвер с границей DefaultFastAnswer.
func DefaultRatingResolver() RatingResolver {
	return RatingResolver{fastAnswer: DefaultFastAnswer}
}

// Resolve отображает ответ в оценку по таблице из плана (§5):
//
//	recall  — оценка пользователя как есть;
//	choice  — верно → good, верно и быстро → easy, неверно → again;
//	typing  — точное совпадение → good, после нормализации или опечатка → hard,
//	          иначе → again.
//
// Ошибка означает, что хендлер собрал ответ неправильно: не указал режим или
// не передал оценку там, где её должен был выбрать пользователь.
func (r RatingResolver) Resolve(a Answer) (Rating, error) {
	fast := r.fastAnswer
	if fast <= 0 {
		// Нулевое значение структуры остаётся рабочим: без этого резолвер,
		// созданный литералом, тихо считал бы любой ответ быстрым.
		fast = DefaultFastAnswer
	}

	switch a.Mode {
	case ModeRecall:
		if !a.SelfRating.IsValid() {
			return 0, fmt.Errorf("rating: %w (в режиме %s оценку выбирает пользователь)", ErrRequired, ModeRecall)
		}
		return a.SelfRating, nil

	case ModeChoice:
		if !a.Correct {
			return RatingAgain, nil
		}
		if a.Elapsed > 0 && a.Elapsed < fast {
			return RatingEasy, nil
		}
		return RatingGood, nil

	case ModeTyping:
		switch a.Match {
		case lexicon.MatchExact:
			return RatingGood, nil
		case lexicon.MatchNormalized, lexicon.MatchTypo:
			// Ответ засчитан, но с оговоркой: артикль забыт или палец промахнулся.
			// Оценка «трудно» вернёт слово раньше, чем «хорошо».
			return RatingHard, nil
		default:
			return RatingAgain, nil
		}

	default:
		return 0, fmt.Errorf("mode %q: %w", a.Mode, ErrInvalid)
	}
}
