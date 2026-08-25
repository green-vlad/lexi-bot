package study

import (
	"fmt"
	"strings"
)

// Mode — режим проверки: как именно у пользователя спрашивают слово.
//
// Режим влияет на оценку (см. PLAN.md §5): она выводится из правильности
// и скорости ответа. Самооценки — «я вспомнил, поверьте на слово» — среди
// режимов нет намеренно: человек, который сам себе ставит отметку, склонен
// быть к себе добрее, чем стоило бы, и расписание повторений от этого
// расползается.
type Mode string

// Режимы проверки.
const (
	// ModeChoice — выбор правильного перевода из четырёх вариантов.
	ModeChoice Mode = "choice"
	// ModeTyping — ввод перевода текстом.
	ModeTyping Mode = "typing"
)

// modes перечислены в порядке возрастания сложности: от узнавания к
// воспроизведению. В этом же порядке они показываются в настройках.
var modes = []Mode{ModeChoice, ModeTyping}

// Modes возвращает все режимы проверки в порядке показа в настройках.
func Modes() []Mode {
	out := make([]Mode, len(modes))
	copy(out, modes)
	return out
}

// ParseMode разбирает режим из строки — так он хранится в базе и в настройках.
func ParseMode(s string) (Mode, error) {
	name := Mode(strings.ToLower(strings.TrimSpace(s)))
	if name == "" {
		return "", fmt.Errorf("mode: %w", ErrRequired)
	}
	if !name.IsValid() {
		return "", fmt.Errorf("mode %q: %w", s, ErrInvalid)
	}
	return name, nil
}

// String возвращает код режима.
func (m Mode) String() string { return string(m) }

// IsValid сообщает, что режим входит в набор допустимых.
func (m Mode) IsValid() bool {
	for _, known := range modes {
		if m == known {
			return true
		}
	}
	return false
}
