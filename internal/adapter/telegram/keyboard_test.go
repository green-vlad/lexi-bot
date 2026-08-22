package telegram_test

import (
	"strings"
	"testing"

	"lexi-bot/internal/adapter/telegram"
	"lexi-bot/internal/usecase/port"
)

// portMessage собирает сообщение с готовой клавиатурой.
func portMessage(kb *port.Keyboard) port.OutgoingMessage {
	return port.OutgoingMessage{ChatID: 777, Text: "집", Keyboard: kb}
}

func TestKeyboardRows(t *testing.T) {
	t.Parallel()

	kb, err := telegram.NewKeyboard().
		Row(
			telegram.Button("Помню", telegram.Callback{Action: "rate", ID: 7, Param: "good"}),
			telegram.Button("Не помню", telegram.Callback{Action: "rate", ID: 7, Param: "again"}),
		).
		Row(telegram.Button("Хватит", telegram.Callback{Action: "stop"})).
		Build()
	if err != nil {
		t.Fatalf("Build() вернул ошибку: %v", err)
	}
	if kb == nil {
		t.Fatal("клавиатура не собралась")
	}

	if len(kb.Rows) != 2 {
		t.Fatalf("строк %d, ожидалось две", len(kb.Rows))
	}
	if len(kb.Rows[0]) != 2 || len(kb.Rows[1]) != 1 {
		t.Errorf("раскладка = %d и %d кнопок", len(kb.Rows[0]), len(kb.Rows[1]))
	}
	if kb.Rows[0][0].Data != "rate:7:good" {
		t.Errorf("данные кнопки = %q", kb.Rows[0][0].Data)
	}
	if kb.Rows[1][0].Data != "stop" {
		t.Errorf("данные кнопки = %q", kb.Rows[1][0].Data)
	}
}

func TestKeyboardGrid(t *testing.T) {
	t.Parallel()

	buttons := make([]telegram.KeyboardButton, 0, 5)
	for i := 1; i <= 5; i++ {
		buttons = append(buttons, telegram.Button("вариант", telegram.Callback{Action: "pick", ID: int64(i)}))
	}

	kb, err := telegram.NewKeyboard().Grid(2, buttons...).Build()
	if err != nil {
		t.Fatalf("Build() вернул ошибку: %v", err)
	}

	// Пять кнопок по две в строке — это 2, 2 и остаток.
	if len(kb.Rows) != 3 {
		t.Fatalf("строк %d, ожидалось три", len(kb.Rows))
	}
	if len(kb.Rows[2]) != 1 {
		t.Errorf("в последней строке %d кнопок, ожидалась одна", len(kb.Rows[2]))
	}
	if kb.Rows[2][0].Data != "pick:5" {
		t.Errorf("последняя кнопка = %q", kb.Rows[2][0].Data)
	}
}

func TestKeyboardEmptyIsNil(t *testing.T) {
	t.Parallel()

	// Telegram отвергает сообщение с клавиатурой без кнопок, поэтому
	// пустая клавиатура — это отсутствие клавиатуры.
	kb, err := telegram.NewKeyboard().Build()
	if err != nil {
		t.Fatalf("Build() вернул ошибку: %v", err)
	}
	if kb != nil {
		t.Errorf("пустая клавиатура = %+v, ожидался nil", kb)
	}

	kb, err = telegram.NewKeyboard().Row().Build()
	if err != nil || kb != nil {
		t.Errorf("строка без кнопок дала %+v, %v", kb, err)
	}
}

func TestKeyboardReportsFirstError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		build func() (*telegram.Keyboard, string)
	}{
		{
			name: "слишком длинное callback_data",
			build: func() (*telegram.Keyboard, string) {
				long := telegram.Callback{Action: "pick", Param: strings.Repeat("я", 40)}
				return telegram.NewKeyboard().Row(telegram.Button("вариант", long)), "вариант"
			},
		},
		{
			name: "кнопка без надписи",
			build: func() (*telegram.Keyboard, string) {
				return telegram.NewKeyboard().Row(telegram.Button("", telegram.Callback{Action: "pick"})), "надписи"
			},
		},
		{
			name: "действие без имени",
			build: func() (*telegram.Keyboard, string) {
				return telegram.NewKeyboard().Row(telegram.Button("вариант", telegram.Callback{})), "действия"
			},
		},
		{
			name: "слишком много кнопок в строке",
			build: func() (*telegram.Keyboard, string) {
				buttons := make([]telegram.KeyboardButton, 0, telegram.MaxButtonsInRow+1)
				for i := 0; i <= telegram.MaxButtonsInRow; i++ {
					buttons = append(buttons, telegram.Button("кнопка", telegram.Callback{Action: "pick", ID: int64(i)}))
				}
				return telegram.NewKeyboard().Row(buttons...), "кнопок"
			},
		},
		{
			name: "неположительная ширина сетки",
			build: func() (*telegram.Keyboard, string) {
				return telegram.NewKeyboard().Grid(0, telegram.Button("кнопка", telegram.Callback{Action: "pick"})), "ширина"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			builder, hint := tt.build()
			kb, err := builder.Build()
			if err == nil {
				t.Fatalf("Build() = %+v, ожидалась ошибка", kb)
			}
			if !strings.Contains(err.Error(), hint) {
				t.Errorf("ошибка %v не объясняет, что не так", err)
			}
			if kb != nil {
				t.Error("при ошибке клавиатура не должна собираться")
			}
		})
	}
}

func TestKeyboardKeepsFirstError(t *testing.T) {
	t.Parallel()

	// Первая ошибка объясняет причину, остальные — лишь следствия.
	kb, err := telegram.NewKeyboard().
		Row(telegram.Button("", telegram.Callback{Action: "first"})).
		Row(telegram.Button("вторая", telegram.Callback{})).
		Build()
	if err == nil {
		t.Fatalf("Build() = %+v, ожидалась ошибка", kb)
	}
	if !strings.Contains(err.Error(), "надписи") {
		t.Errorf("ошибка = %v, ожидалась первая по порядку", err)
	}
}

func TestKeyboardFitsMessenger(t *testing.T) {
	t.Parallel()

	// Собранная клавиатура должна доезжать до Telegram как есть.
	messenger := &fakeMessenger{}
	kb, err := telegram.NewKeyboard().
		Row(telegram.Button("Помню", telegram.Callback{Action: "rate", ID: 7, Param: "good"})).
		Build()
	if err != nil {
		t.Fatalf("Build() вернул ошибку: %v", err)
	}

	if _, err := messenger.SendMessage(t.Context(), portMessage(kb)); err != nil {
		t.Fatalf("SendMessage() вернул ошибку: %v", err)
	}

	sent := messenger.last(t)
	if sent.Keyboard == nil || len(sent.Keyboard.Rows) != 1 {
		t.Fatalf("клавиатура не доехала: %+v", sent.Keyboard)
	}
	if got := sent.Keyboard.Rows[0][0]; got.Text != "Помню" || got.Data != "rate:7:good" {
		t.Errorf("кнопка = %+v", got)
	}
}
