package telegram_test

import (
	"context"
	"strings"
	"testing"

	"lexi-bot/internal/adapter/telegram"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// languageFixture — бот с /help и /language.
type languageFixture struct {
	router    *telegram.Router
	messenger *editingMessenger
	users     *fakeUsers
}

func newLanguageFixture(t *testing.T) *languageFixture {
	t.Helper()

	f := &languageFixture{messenger: &editingMessenger{}, users: newFakeUsers()}
	catalog := testCatalog(t)

	handler, err := telegram.NewLanguage(f.users, f.messenger, catalog)
	if err != nil {
		t.Fatalf("NewLanguage() вернул ошибку: %v", err)
	}

	f.router = telegram.NewRouter()
	f.router.Use(
		telegram.AnswerCallbacks(f.messenger, quietLogger()),
		telegram.Identify(f.users, quietLogger()),
		telegram.Localize(catalog),
	)
	handler.Register(f.router)
	return f
}

func (f *languageFixture) send(t *testing.T, text string) {
	t.Helper()

	if err := f.router.Handle(context.Background(), message(text)); err != nil {
		t.Fatalf("Handle(%q) вернул ошибку: %v", text, err)
	}
}

func (f *languageFixture) press(t *testing.T, data string) {
	t.Helper()

	update := &port.Update{
		ID:       2,
		Chat:     777,
		Sender:   port.Sender{TelegramID: 555, Username: "durov", LanguageCode: "ru"},
		Callback: &port.CallbackData{ID: "cb", Data: data, MessageID: 1},
	}
	if err := f.router.Handle(context.Background(), update); err != nil {
		t.Fatalf("нажатие %q вернуло ошибку: %v", data, err)
	}
}

func TestHelpListsCommands(t *testing.T) {
	t.Parallel()

	f := newLanguageFixture(t)
	f.send(t, "/help")

	text := f.messenger.last(t).Text
	// Справка перечисляет то, что бот действительно умеет: обещать
	// несуществующие команды хуже, чем не обещать ничего.
	for _, command := range []string{"/start", "/language", "/cancel"} {
		if !strings.Contains(text, command) {
			t.Errorf("в справке нет %s: %q", command, text)
		}
	}
}

func TestLanguageSwitch(t *testing.T) {
	t.Parallel()

	f := newLanguageFixture(t)

	f.send(t, "/language")
	keyboard := f.messenger.last(t).Keyboard
	if keyboard == nil {
		t.Fatal("выбор языка без кнопок")
	}

	var codes []string
	for _, row := range keyboard.Rows {
		for _, b := range row {
			codes = append(codes, b.Data)
		}
	}
	if len(codes) != 2 {
		t.Fatalf("кнопки = %v, ожидались два языка", codes)
	}

	f.press(t, "lang:0:en")

	if got := f.users.byTgID[555].UILang; got != user.UILangEN {
		t.Fatalf("язык интерфейса = %q, ожидался en", got)
	}

	// Подтверждение приходит уже на новом языке — иначе выглядит так,
	// будто переключение не сработало.
	confirmation := f.messenger.lastEdit(t)
	if !strings.Contains(confirmation.Text, "English") {
		t.Errorf("подтверждение = %q, ожидалось английское", confirmation.Text)
	}
	if confirmation.Keyboard != nil {
		t.Error("после выбора кнопки должны исчезнуть")
	}

	// И следующая команда отвечает уже по-английски.
	f.send(t, "/help")
	if got := f.messenger.last(t).Text; !strings.Contains(got, "Here is what I can do") {
		t.Errorf("справка после смены языка = %q", got)
	}
}

func TestLanguageIgnoresStaleButton(t *testing.T) {
	t.Parallel()

	f := newLanguageFixture(t)
	f.send(t, "/language")

	// Кнопка от прошлой версии бота: язык не меняется, ошибки нет.
	f.press(t, "lang:0:ko")
	f.press(t, "lang")

	if got := f.users.byTgID[555].UILang; got != user.UILangRU {
		t.Errorf("язык интерфейса = %q, ожидался прежний", got)
	}
}

func TestLanguageNeedsDependencies(t *testing.T) {
	t.Parallel()

	if _, err := telegram.NewLanguage(nil, nil, nil); err == nil {
		t.Error("хендлер без зависимостей должен быть ошибкой")
	}
}

func TestRouterCallbackActions(t *testing.T) {
	t.Parallel()

	var called string
	router := telegram.NewRouter()
	router.CallbackAction("rate", record(&called, "rate"))
	router.CallbackAction("deck", record(&called, "deck"))
	router.Callback(record(&called, "общий"))

	tests := []struct {
		data string
		want string
	}{
		{"rate:7:good", "rate"},
		{"deck:42", "deck"},
		{"неизвестное:1", "общий"},
		// Данные, которые не разбираются, тоже достаются общему хендлеру:
		// кнопка могла остаться от прошлой версии бота.
		{"", "общий"},
	}

	for _, tt := range tests {
		called = ""
		update := &port.Update{ID: 1, Chat: 777, Callback: &port.CallbackData{ID: "cb", Data: tt.data}}
		if err := router.Handle(context.Background(), update); err != nil {
			t.Fatalf("Handle(%q) вернул ошибку: %v", tt.data, err)
		}
		if called != tt.want {
			t.Errorf("нажатие %q ушло в %q, ожидалось %q", tt.data, called, tt.want)
		}
	}
}
