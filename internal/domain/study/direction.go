package study

import (
	"fmt"
	"strings"
)

// Direction — в какую сторону спрашивается карточка.
//
// Разница не косметическая: узнавание и воспроизведение — разные умения.
// Увидев «집», человек вспоминает знакомую форму; чтобы написать «집»
// по слову «дом», её нужно достать из памяти самому, и это заметно труднее.
type Direction uint8

// Направления проверки.
const (
	// DirectionRecognize — показываем слово изучаемого языка, ждём перевод.
	DirectionRecognize Direction = iota
	// DirectionProduce — показываем перевод, ждём слово изучаемого языка.
	DirectionProduce
)

var directionNames = map[Direction]string{
	DirectionRecognize: "recognize",
	DirectionProduce:   "produce",
}

// Directions возвращает оба направления.
func Directions() []Direction { return []Direction{DirectionRecognize, DirectionProduce} }

// ParseDirection разбирает направление из строки.
func ParseDirection(s string) (Direction, error) {
	name := strings.ToLower(strings.TrimSpace(s))
	if name == "" {
		return 0, fmt.Errorf("direction: %w", ErrRequired)
	}
	for direction, known := range directionNames {
		if name == known {
			return direction, nil
		}
	}
	return 0, fmt.Errorf("direction %q: %w", s, ErrInvalid)
}

// String возвращает имя направления.
func (d Direction) String() string {
	if name, ok := directionNames[d]; ok {
		return name
	}
	return fmt.Sprintf("direction(%d)", uint8(d))
}

// IsValid сообщает, что направление входит в набор допустимых.
func (d Direction) IsValid() bool {
	_, ok := directionNames[d]
	return ok
}

// AllowsTyping сообщает, можно ли в этом направлении спрашивать вводом текста.
//
// Печатать перевод на родной язык бессмысленно: у слова обычно несколько
// равноправных значений («профессия», «работа», «специальность»), и любой
// свободный ввод превращается в угадывание того, какое из них мы сочли
// основным. В обратную сторону слово одно, и напечатать его — настоящая
// проверка, а не узнавание.
func (d Direction) AllowsTyping() bool { return d == DirectionProduce }

// Modes возвращает режимы проверки, осмысленные в этом направлении.
func (d Direction) Modes(enabled []Mode) []Mode {
	out := make([]Mode, 0, len(enabled))
	for _, mode := range enabled {
		if mode == ModeTyping && !d.AllowsTyping() {
			continue
		}
		out = append(out, mode)
	}
	return out
}
