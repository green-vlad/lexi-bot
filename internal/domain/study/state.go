package study

import (
	"fmt"
	"strings"
)

// State — фаза жизни карточки.
//
// Нулевое значение — StateNew, и это удобно: только что созданная карточка
// корректна без единого присваивания.
type State uint8

// Фазы карточки.
const (
	// StateNew — слово введено в курс, но ещё ни разу не показывалось.
	StateNew State = iota
	// StateLearning — карточка проходит короткие шаги обучения (минуты).
	StateLearning
	// StateReview — карточка выучена и повторяется с растущим интервалом.
	StateReview
	// StateRelearning — выученную карточку забыли, она снова на шагах обучения.
	StateRelearning
	// StateSuspended — карточка отложена и не выдаётся в сессии.
	StateSuspended
	// StateKnown — человек сказал «я уже знаю это слово» на знакомстве.
	// Карточка остаётся в курсе, чтобы слово не предлагалось снова,
	// но в повторения не попадает: учить в нём нечего.
	StateKnown
)

var stateNames = map[State]string{
	StateNew:        "new",
	StateLearning:   "learning",
	StateReview:     "review",
	StateRelearning: "relearning",
	StateSuspended:  "suspended",
	StateKnown:      "known",
}

// States возвращает все фазы в порядке естественного продвижения карточки.
func States() []State {
	return []State{StateNew, StateLearning, StateReview, StateRelearning, StateSuspended, StateKnown}
}

// ParseState разбирает фазу из строки — так она хранится в базе.
func ParseState(s string) (State, error) {
	name := strings.ToLower(strings.TrimSpace(s))
	if name == "" {
		return 0, fmt.Errorf("state: %w", ErrRequired)
	}
	for st, known := range stateNames {
		if name == known {
			return st, nil
		}
	}
	return 0, fmt.Errorf("state %q: %w", s, ErrInvalid)
}

// String возвращает имя фазы. У неизвестного значения — диагностическая запись
// вида state(9), которая не разбирается обратно.
func (s State) String() string {
	if name, ok := stateNames[s]; ok {
		return name
	}
	return fmt.Sprintf("state(%d)", uint8(s))
}

// IsValid сообщает, что фаза входит в набор допустимых.
func (s State) IsValid() bool {
	_, ok := stateNames[s]
	return ok
}

// IsLearning сообщает, что карточка на шагах обучения — первичных или
// повторных. В этой фазе интервалы измеряются минутами, а не сутками.
func (s State) IsLearning() bool { return s == StateLearning || s == StateRelearning }

// InRepetition сообщает, что карточка участвует в повторениях.
//
// Не участвуют три фазы, и по разным причинам. Новая карточка ещё ждёт
// знакомства: человек не видел ни слова, ни перевода, и спрашивать его
// не о чем. Отложенная убрана из занятий сознательно. А та, про которую
// сказали «уже знаю», не нуждается в повторении по определению.
func (s State) InRepetition() bool {
	return s == StateLearning || s == StateReview || s == StateRelearning
}
