package telegram_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"lexi-bot/internal/adapter/i18n"
	"lexi-bot/internal/adapter/telegram"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/locales"
)

// fakeMessenger запоминает отправленное вместо похода в Telegram.
type fakeMessenger struct {
	mu   sync.Mutex
	sent []port.OutgoingMessage
	err  error
}

func (m *fakeMessenger) SendMessage(_ context.Context, msg port.OutgoingMessage) (port.MessageID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.err != nil {
		return 0, m.err
	}
	m.sent = append(m.sent, msg)
	return port.MessageID(len(m.sent)), nil
}

func (m *fakeMessenger) EditMessage(context.Context, port.MessageEdit) error         { return nil }
func (m *fakeMessenger) AnswerCallback(context.Context, port.CallbackAnswer) error   { return nil }
func (m *fakeMessenger) SendDocument(context.Context, port.Document) error           { return nil }
func (m *fakeMessenger) DownloadFile(context.Context, string, int64) ([]byte, error) { return nil, nil }

func (m *fakeMessenger) last(t *testing.T) port.OutgoingMessage {
	t.Helper()

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) == 0 {
		t.Fatal("боту следовало что-то отправить, но он промолчал")
	}
	return m.sent[len(m.sent)-1]
}

func (m *fakeMessenger) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

// fakeUsers — минимальный UserRepo в памяти.
type fakeUsers struct {
	mu       sync.Mutex
	byTgID   map[user.TelegramID]user.User
	nextID   user.ID
	ensures  int
	failWith error
}

func newFakeUsers() *fakeUsers {
	return &fakeUsers{byTgID: map[user.TelegramID]user.User{}, nextID: 1}
}

func (f *fakeUsers) Ensure(_ context.Context, u user.User) (user.User, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failWith != nil {
		return user.User{}, false, f.failWith
	}
	f.ensures++

	if existing, ok := f.byTgID[u.TelegramID]; ok {
		existing.Username = u.Username
		f.byTgID[u.TelegramID] = existing
		return existing, false, nil
	}
	u.ID = f.nextID
	f.nextID++
	f.byTgID[u.TelegramID] = u
	return u, true, nil
}

func (f *fakeUsers) ByTelegramID(_ context.Context, tgID user.TelegramID) (user.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failWith != nil {
		return user.User{}, f.failWith
	}
	if u, ok := f.byTgID[tgID]; ok {
		return u, nil
	}
	return user.User{}, port.ErrNotFound
}

func (f *fakeUsers) ByID(context.Context, user.ID) (user.User, error) {
	return user.User{}, port.ErrNotFound
}
func (f *fakeUsers) SetUILang(context.Context, user.ID, user.UILang) error { return nil }
func (f *fakeUsers) SoftDelete(context.Context, user.ID, time.Time) error  { return nil }
func (f *fakeUsers) Purge(context.Context, user.ID) error                  { return nil }

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

func testCatalog(t *testing.T) port.Catalog {
	t.Helper()

	catalog, err := i18n.NewCatalog(locales.FS)
	if err != nil {
		t.Fatalf("NewCatalog() вернул ошибку: %v", err)
	}
	return catalog
}

func message(text string) *port.Update {
	u := &port.Update{
		ID:     1,
		Chat:   777,
		Text:   text,
		Sender: port.Sender{TelegramID: 555, Username: "durov", LanguageCode: "ru"},
	}
	if strings.HasPrefix(text, "/") {
		command := strings.TrimPrefix(text, "/")
		if space := strings.IndexByte(command, ' '); space >= 0 {
			u.Args = command[space+1:]
			command = command[:space]
		}
		u.Command = command
	}
	return u
}

func TestRouterDispatchesByKind(t *testing.T) {
	t.Parallel()

	var called string
	router := telegram.NewRouter()
	router.Command("learn", record(&called, "learn"))
	router.Command("help", record(&called, "help"))
	router.Callback(record(&called, "callback"))
	router.Text(record(&called, "text"))
	router.Unknown(record(&called, "unknown"))

	tests := []struct {
		name   string
		update *port.Update
		want   string
	}{
		{"команда", message("/learn"), "learn"},
		{"другая команда", message("/help"), "help"},
		{"неизвестная команда", message("/nope"), "unknown"},
		{"обычный текст", message("дом"), "text"},
		{"нажатие кнопки", &port.Update{ID: 2, Chat: 777, Callback: &port.CallbackData{ID: "cb", Data: "rate:good"}}, "callback"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called = ""
			if err := router.Handle(context.Background(), tt.update); err != nil {
				t.Fatalf("Handle() вернул ошибку: %v", err)
			}
			if called != tt.want {
				t.Errorf("сработал хендлер %q, ожидался %q", called, tt.want)
			}
		})
	}
}

func TestRouterIgnoresUnroutableUpdates(t *testing.T) {
	t.Parallel()

	// Апдейт без текста, команды и кнопки (например, присланный стикер)
	// не должен ничего ломать: у него просто нет маршрута.
	router := telegram.NewRouter()
	if err := router.Handle(context.Background(), &port.Update{ID: 1, Chat: 777}); err != nil {
		t.Errorf("Handle() вернул ошибку: %v", err)
	}

	// И маршрут, для которого не назначен хендлер, тоже.
	if err := router.Handle(context.Background(), message("привет")); err != nil {
		t.Errorf("Handle() вернул ошибку: %v", err)
	}
}

func TestRouterCommandNamesAreNormalized(t *testing.T) {
	t.Parallel()

	var called string
	router := telegram.NewRouter()
	// Со слешем и в верхнем регистре — привязка должна сработать всё равно.
	router.Command("/Learn", record(&called, "learn"))

	if err := router.Handle(context.Background(), message("/learn")); err != nil {
		t.Fatalf("Handle() вернул ошибку: %v", err)
	}
	if called != "learn" {
		t.Errorf("хендлер не сработал: %q", called)
	}
}

func TestMiddlewareOrder(t *testing.T) {
	t.Parallel()

	var order []string
	trace := func(name string) telegram.Middleware {
		return func(next port.UpdateHandler) port.UpdateHandler {
			return port.UpdateHandlerFunc(func(ctx context.Context, u *port.Update) error {
				order = append(order, "вход:"+name)
				err := next.Handle(ctx, u)
				order = append(order, "выход:"+name)
				return err
			})
		}
	}

	router := telegram.NewRouter()
	router.Use(trace("первый"), trace("второй"))
	router.Command("learn", port.UpdateHandlerFunc(func(context.Context, *port.Update) error {
		order = append(order, "хендлер")
		return nil
	}))

	if err := router.Handle(context.Background(), message("/learn")); err != nil {
		t.Fatalf("Handle() вернул ошибку: %v", err)
	}

	// Первый добавленный — снаружи: он видит апдейт первым и уходит последним.
	want := []string{"вход:первый", "вход:второй", "хендлер", "выход:второй", "выход:первый"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("порядок = %v, ожидался %v", order, want)
	}
}

func TestMiddlewareWrapsUnroutedUpdates(t *testing.T) {
	t.Parallel()

	// Апдейт без маршрута обязан пройти через middleware: иначе он
	// не попадёт в лог, а паника в самом middleware уронит процесс.
	seen := 0
	router := telegram.NewRouter()
	router.Use(func(next port.UpdateHandler) port.UpdateHandler {
		return port.UpdateHandlerFunc(func(ctx context.Context, u *port.Update) error {
			seen++
			return next.Handle(ctx, u)
		})
	})

	if err := router.Handle(context.Background(), &port.Update{ID: 1, Chat: 777}); err != nil {
		t.Fatalf("Handle() вернул ошибку: %v", err)
	}
	if seen != 1 {
		t.Errorf("middleware сработал %d раз, ожидался один", seen)
	}
}

func record(target *string, name string) port.UpdateHandler {
	return port.UpdateHandlerFunc(func(context.Context, *port.Update) error {
		*target = name
		return nil
	})
}
