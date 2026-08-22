package telegram_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"lexi-bot/internal/adapter/telegram"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// fakeSessions — SessionRepo в памяти.
type fakeSessions struct {
	mu       sync.Mutex
	byUser   map[user.ID]port.Session
	failWith error
}

func newFakeSessions() *fakeSessions {
	return &fakeSessions{byUser: map[user.ID]port.Session{}}
}

func (f *fakeSessions) Get(_ context.Context, userID user.ID) (port.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failWith != nil {
		return port.Session{}, f.failWith
	}
	if s, ok := f.byUser[userID]; ok {
		return s, nil
	}
	return port.Session{}, port.ErrNotFound
}

func (f *fakeSessions) Save(_ context.Context, s port.Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failWith != nil {
		return f.failWith
	}
	f.byUser[s.UserID] = s
	return nil
}

func (f *fakeSessions) Delete(_ context.Context, userID user.ID) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.byUser, userID)
	return nil
}

func (f *fakeSessions) DeleteStale(context.Context, time.Time) (int64, error) { return 0, nil }

func (f *fakeSessions) current(t *testing.T) (port.Session, bool) {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.byUser[1]
	return s, ok
}

// addWord — диалог добавления слова: спрашивает слово, потом перевод.
// Ровно то, чем будет /add в T-040.
type addWord struct {
	Term        string `json:"term"`
	Translation string `json:"translation"`
}

// dialogFixture собирает роутер с движком диалогов и пользователем.
type dialogFixture struct {
	router    *telegram.Router
	dialogs   *telegram.Dialogs
	sessions  *fakeSessions
	messenger *fakeMessenger
	saved     []addWord
	now       time.Time
}

func newDialogFixture(t *testing.T) *dialogFixture {
	t.Helper()

	f := &dialogFixture{
		sessions:  newFakeSessions(),
		messenger: &fakeMessenger{},
		now:       time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC),
	}

	dialogs, err := telegram.NewDialogs(&telegram.DialogsConfig{
		Sessions:  f.sessions,
		Messenger: f.messenger,
		Clock:     port.ClockFunc(func() time.Time { return f.now }),
		Logger:    quietLogger(),
	})
	if err != nil {
		t.Fatalf("NewDialogs() вернул ошибку: %v", err)
	}
	f.dialogs = dialogs

	dialogs.Register("add:term", telegram.StepFunc(func(ctx context.Context, u *port.Update, _ json.RawMessage) (telegram.StepResult, error) {
		if strings.TrimSpace(u.Text) == "" {
			// Неподходящий ввод: переспрашиваем, а не бросаем диалог.
			return telegram.Stay(nil), nil
		}
		return telegram.Next("add:translation", addWord{Term: u.Text}), nil
	}))

	dialogs.Register("add:translation", telegram.StepFunc(func(ctx context.Context, u *port.Update, payload json.RawMessage) (telegram.StepResult, error) {
		var word addWord
		if err := telegram.UnmarshalPayload(payload, &word); err != nil {
			return telegram.StepResult{}, err
		}
		word.Translation = u.Text
		f.saved = append(f.saved, word)
		return telegram.Done(), nil
	}))

	users := newFakeUsers()
	f.router = telegram.NewRouter()
	f.router.Use(
		telegram.Identify(users, quietLogger()),
		telegram.Localize(testCatalog(t)),
		dialogs.Middleware(),
	)
	f.router.Command("add", port.UpdateHandlerFunc(func(ctx context.Context, u *port.Update) error {
		return dialogs.Start(ctx, "add:term", nil)
	}))
	f.router.Command("stats", port.UpdateHandlerFunc(func(ctx context.Context, u *port.Update) error {
		_, err := f.messenger.SendMessage(ctx, port.OutgoingMessage{ChatID: u.Chat, Text: "статистика"})
		return err
	}))
	f.router.Text(port.UpdateHandlerFunc(func(ctx context.Context, u *port.Update) error {
		_, err := f.messenger.SendMessage(ctx, port.OutgoingMessage{ChatID: u.Chat, Text: "просто текст: " + u.Text})
		return err
	}))
	return f
}

func (f *dialogFixture) handle(t *testing.T, text string) {
	t.Helper()

	if err := f.router.Handle(context.Background(), message(text)); err != nil {
		t.Fatalf("Handle(%q) вернул ошибку: %v", text, err)
	}
}

func TestDialogFullCycle(t *testing.T) {
	t.Parallel()

	f := newDialogFixture(t)

	f.handle(t, "/add")
	session, ok := f.sessions.current(t)
	if !ok || session.State != "add:term" {
		t.Fatalf("после /add состояние = %+v", session)
	}

	// Обычный текст достаётся шагу диалога, а не маршруту роутера.
	f.handle(t, "집")
	session, ok = f.sessions.current(t)
	if !ok || session.State != "add:translation" {
		t.Fatalf("после первого ответа состояние = %+v", session)
	}
	if !strings.Contains(string(session.Payload), "집") {
		t.Errorf("payload = %s, слово не сохранилось", session.Payload)
	}

	f.handle(t, "дом")
	if _, ok := f.sessions.current(t); ok {
		t.Error("после завершения диалог должен быть сброшен")
	}
	if len(f.saved) != 1 || f.saved[0].Term != "집" || f.saved[0].Translation != "дом" {
		t.Errorf("сохранено %+v", f.saved)
	}

	// Диалог закончился — следующий текст снова идёт по обычному маршруту.
	f.handle(t, "просто сообщение")
	if got := f.messenger.last(t).Text; !strings.HasPrefix(got, "просто текст:") {
		t.Errorf("после диалога текст ушёл не туда: %q", got)
	}
}

func TestDialogStaysOnBadInput(t *testing.T) {
	t.Parallel()

	f := newDialogFixture(t)
	f.handle(t, "/add")

	// Пустой ввод: шаг переспрашивает, состояние не двигается.
	f.handle(t, "   ")
	session, ok := f.sessions.current(t)
	if !ok || session.State != "add:term" {
		t.Fatalf("состояние = %+v, ожидался тот же шаг", session)
	}
}

func TestDialogCancelledByCommand(t *testing.T) {
	t.Parallel()

	f := newDialogFixture(t)
	f.handle(t, "/add")
	f.handle(t, "집")

	// Посторонняя команда посреди диалога — это «передумал», а не ответ
	// на вопрос про перевод.
	f.handle(t, "/stats")

	if _, ok := f.sessions.current(t); ok {
		t.Error("команда должна прерывать диалог")
	}
	if got := f.messenger.last(t).Text; got != "статистика" {
		t.Errorf("команда не выполнилась, отправлено %q", got)
	}
	if len(f.saved) != 0 {
		t.Errorf("незаконченный диалог не должен ничего сохранять: %+v", f.saved)
	}
}

func TestDialogCancelCommand(t *testing.T) {
	t.Parallel()

	f := newDialogFixture(t)
	f.handle(t, "/add")
	f.handle(t, "집")

	f.handle(t, "/cancel")

	if _, ok := f.sessions.current(t); ok {
		t.Error("/cancel должен сбрасывать диалог")
	}
	// Пользователю сообщают, что отмена случилась, — на его языке.
	if got := f.messenger.last(t).Text; got == "" || strings.HasPrefix(got, "просто текст") {
		t.Errorf("после /cancel отправлено %q", got)
	}
	if len(f.saved) != 0 {
		t.Errorf("отменённый диалог не должен ничего сохранять: %+v", f.saved)
	}
}

func TestDialogUnknownStateIsReset(t *testing.T) {
	t.Parallel()

	f := newDialogFixture(t)
	f.handle(t, "/add")

	// Состояние из базы, которого движок не знает: так бывает после выката,
	// убравшего шаг. Пользователь не должен застрять в нём навсегда.
	session, _ := f.sessions.current(t)
	session.State = "add:step_which_no_longer_exists"
	if err := f.sessions.Save(context.Background(), session); err != nil {
		t.Fatalf("Save() вернул ошибку: %v", err)
	}

	f.handle(t, "привет")

	if _, ok := f.sessions.current(t); ok {
		t.Error("неизвестное состояние должно сбрасываться")
	}
	// И сообщение не пропадает: оно уходит по обычному маршруту.
	if got := f.messenger.last(t).Text; !strings.HasPrefix(got, "просто текст:") {
		t.Errorf("сообщение потерялось: %q", got)
	}
}

func TestDialogExpires(t *testing.T) {
	t.Parallel()

	f := newDialogFixture(t)
	f.handle(t, "/add")

	// Человек начал добавлять слово утром и вернулся вечером: он ждёт
	// от бота меню, а не вопроса, о котором давно забыл.
	f.now = f.now.Add(telegram.DefaultDialogMaxAge + time.Minute)
	f.handle(t, "привет")

	if _, ok := f.sessions.current(t); ok {
		t.Error("брошенный диалог должен сбрасываться")
	}
	if got := f.messenger.last(t).Text; !strings.HasPrefix(got, "просто текст:") {
		t.Errorf("сообщение потерялось: %q", got)
	}
}

func TestDialogResetOnBrokenPayload(t *testing.T) {
	t.Parallel()

	f := newDialogFixture(t)
	f.handle(t, "/add")
	f.handle(t, "집")

	// В payload оказалось не то, что шаг туда клал: продолжать нечем.
	session, _ := f.sessions.current(t)
	session.Payload = json.RawMessage(`["совсем", "не", "то"]`)
	if err := f.sessions.Save(context.Background(), session); err != nil {
		t.Fatalf("Save() вернул ошибку: %v", err)
	}

	f.handle(t, "дом")

	if _, ok := f.sessions.current(t); ok {
		t.Error("шаг вернул ErrResetDialog — диалог должен быть сброшен")
	}
	if len(f.saved) != 0 {
		t.Errorf("сломанный диалог не должен ничего сохранять: %+v", f.saved)
	}
}

func TestDialogStartChecksStep(t *testing.T) {
	t.Parallel()

	f := newDialogFixture(t)

	f.router.Command("broken", port.UpdateHandlerFunc(func(ctx context.Context, u *port.Update) error {
		// Опечатка в имени шага должна обнаруживаться сразу, а не оставлять
		// пользователя в состоянии, которое некому обработать.
		return f.dialogs.Start(ctx, "add:nonexistent", nil)
	}))

	if err := f.router.Handle(context.Background(), message("/broken")); err == nil {
		t.Error("запуск незарегистрированного шага должен быть ошибкой")
	}
	if _, ok := f.sessions.current(t); ok {
		t.Error("состояние не должно было сохраниться")
	}
}

func TestDialogNeedsUser(t *testing.T) {
	t.Parallel()

	f := newDialogFixture(t)

	// Апдейт без отправителя не должен ни начинать диалог, ни падать.
	anonymous := &port.Update{ID: 1, Chat: 777, Text: "привет"}
	if err := f.router.Handle(context.Background(), anonymous); err != nil {
		t.Fatalf("Handle() вернул ошибку: %v", err)
	}
}

func TestDialogsRequireStorage(t *testing.T) {
	t.Parallel()

	if _, err := telegram.NewDialogs(&telegram.DialogsConfig{}); err == nil {
		t.Error("движок без хранилища должен быть ошибкой")
	}
	if _, err := telegram.NewDialogs(nil); err == nil {
		t.Error("движок без параметров должен быть ошибкой")
	}
}

func TestDialogReportsStorageFailure(t *testing.T) {
	t.Parallel()

	f := newDialogFixture(t)
	f.sessions.failWith = fmt.Errorf("база недоступна")

	// Читать состояние диалога не получилось — значит, неизвестно, ждёт ли
	// бот ответа на вопрос. Обрабатывать сообщение вслепую нельзя.
	if err := f.router.Handle(context.Background(), message("привет")); err == nil {
		t.Error("недоступное хранилище диалогов должно давать ошибку")
	}
	if f.messenger.count() != 0 {
		t.Error("боту не следовало отвечать, не зная состояния диалога")
	}
}
