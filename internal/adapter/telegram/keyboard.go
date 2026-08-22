package telegram

import (
	"errors"
	"fmt"

	"lexi-bot/internal/usecase/port"
)

// MaxButtonsInRow — сколько кнопок помещается в строку, не превращаясь
// в нечитаемую полоску. Ограничение наше, а не Telegram: четыре кнопки
// оценки в ряд ещё читаются, шесть — уже нет.
const MaxButtonsInRow = 4

// KeyboardButton — кнопка до сборки: текст и то, что вернётся при нажатии.
type KeyboardButton struct {
	Text     string
	Callback Callback
}

// Button собирает кнопку.
func Button(text string, callback Callback) KeyboardButton {
	return KeyboardButton{Text: text, Callback: callback}
}

// Keyboard — сборщик инлайн-клавиатуры.
//
// Ошибки копятся внутри и всплывают один раз в Build: иначе каждая строка
// вызывающего кода превращалась бы в проверку if err != nil, а собирают
// клавиатуры в хендлерах десятками.
type Keyboard struct {
	rows [][]port.Button
	err  error
}

// NewKeyboard создаёт пустой сборщик.
func NewKeyboard() *Keyboard { return &Keyboard{} }

// Row добавляет строку кнопок.
func (k *Keyboard) Row(buttons ...KeyboardButton) *Keyboard {
	if len(buttons) == 0 {
		return k
	}
	if len(buttons) > MaxButtonsInRow {
		k.fail(fmt.Errorf("в строке %d кнопок при пределе %d", len(buttons), MaxButtonsInRow))
		return k
	}

	row := make([]port.Button, 0, len(buttons))
	for _, b := range buttons {
		encoded, err := b.Callback.Encode()
		if err != nil {
			k.fail(fmt.Errorf("кнопка %q: %w", b.Text, err))
			return k
		}
		if b.Text == "" {
			k.fail(errors.New("кнопка без надписи"))
			return k
		}
		row = append(row, port.Button{Text: b.Text, Data: encoded})
	}

	k.rows = append(k.rows, row)
	return k
}

// Grid раскладывает кнопки по строкам заданной ширины: так показываются
// списки языков, колод и вариантов ответа.
func (k *Keyboard) Grid(columns int, buttons ...KeyboardButton) *Keyboard {
	if columns <= 0 {
		k.fail(fmt.Errorf("ширина сетки должна быть положительной, получено %d", columns))
		return k
	}

	for start := 0; start < len(buttons); start += columns {
		end := min(start+columns, len(buttons))
		k.Row(buttons[start:end]...)
	}
	return k
}

// Build возвращает готовую клавиатуру.
//
// Пустая клавиатура — это nil, а не пустая разметка: Telegram отвергает
// сообщение с клавиатурой без кнопок.
func (k *Keyboard) Build() (*port.Keyboard, error) {
	if k.err != nil {
		return nil, k.err
	}
	if len(k.rows) == 0 {
		return nil, nil
	}
	return &port.Keyboard{Rows: k.rows}, nil
}

// fail запоминает первую ошибку: она и объясняет, что пошло не так,
// а последующие — лишь следствия.
func (k *Keyboard) fail(err error) {
	if k.err == nil {
		k.err = err
	}
}
