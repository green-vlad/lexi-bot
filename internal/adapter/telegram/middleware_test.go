package telegram_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"lexi-bot/internal/adapter/telegram"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

func TestRecoverKeepsProcessAlive(t *testing.T) {
	t.Parallel()

	messenger := &fakeMessenger{}
	catalog := testCatalog(t)

	router := telegram.NewRouter()
	router.Use(telegram.Recover(messenger, catalog, quietLogger(), nil))
	router.Command("learn", port.UpdateHandlerFunc(func(context.Context, *port.Update) error {
		panic("сценарий разыменовал nil")
	}))

	// Главное: паника не выходит наружу и не уносит с собой процесс,
	// а значит и сессии остальных пользователей.
	err := router.Handle(context.Background(), message("/learn"))
	if err == nil {
		t.Fatal("паника должна превращаться в ошибку")
	}

	sent := messenger.last(t)
	if sent.ChatID != 777 {
		t.Errorf("извинение ушло в чат %d", sent.ChatID)
	}
	if sent.Text == "" {
		t.Error("пользователь остался без ответа: молчащий бот выглядит сломанным")
	}
}

func TestRecoverSpeaksUserLanguage(t *testing.T) {
	t.Parallel()

	messenger := &fakeMessenger{}
	catalog := testCatalog(t)

	router := telegram.NewRouter()
	// Локализация снаружи: извинение должно прийти на языке пользователя.
	router.Use(telegram.Localize(catalog), telegram.Recover(messenger, catalog, quietLogger(), nil))
	router.Command("learn", port.UpdateHandlerFunc(func(context.Context, *port.Update) error {
		panic("что-то пошло не так")
	}))

	russian := message("/learn")
	if err := router.Handle(context.Background(), russian); err == nil {
		t.Fatal("паника должна превращаться в ошибку")
	}
	ru := messenger.last(t).Text

	english := message("/learn")
	english.Sender.LanguageCode = "en"
	if err := router.Handle(context.Background(), english); err == nil {
		t.Fatal("паника должна превращаться в ошибку")
	}
	en := messenger.last(t).Text

	if ru == en {
		t.Errorf("извинение одинаково на обоих языках: %q", ru)
	}
	if !strings.Contains(en, "wrong") {
		t.Errorf("англоязычному пользователю пришло %q", en)
	}
}

func TestRecoverSurvivesBrokenMessenger(t *testing.T) {
	t.Parallel()

	// Аварийный путь не должен падать второй раз: если извиниться
	// не получилось, это лишь запись в логе.
	messenger := &fakeMessenger{err: errors.New("Telegram недоступен")}

	router := telegram.NewRouter()
	router.Use(telegram.Recover(messenger, testCatalog(t), quietLogger(), nil))
	router.Command("learn", port.UpdateHandlerFunc(func(context.Context, *port.Update) error {
		panic("паника")
	}))

	if err := router.Handle(context.Background(), message("/learn")); err == nil {
		t.Error("паника должна превращаться в ошибку")
	}
}

func TestLoggingPassesErrorsThrough(t *testing.T) {
	t.Parallel()

	wanted := errors.New("сценарий сломался")

	router := telegram.NewRouter()
	router.Use(telegram.Logging(quietLogger()))
	router.Command("learn", port.UpdateHandlerFunc(func(context.Context, *port.Update) error {
		return wanted
	}))

	if err := router.Handle(context.Background(), message("/learn")); !errors.Is(err, wanted) {
		t.Errorf("Handle() = %v, ожидалась ошибка хендлера", err)
	}
}

func TestIdentifyRegistersNewUser(t *testing.T) {
	t.Parallel()

	users := newFakeUsers()

	var seen user.User
	router := telegram.NewRouter()
	router.Use(telegram.Identify(users, quietLogger()))
	router.Command("learn", port.UpdateHandlerFunc(func(ctx context.Context, _ *port.Update) error {
		known, ok := telegram.UserFrom(ctx)
		if !ok {
			t.Error("пользователь не попал в контекст")
		}
		seen = known
		return nil
	}))

	// Человек может начать с любой команды, а не с /start: без записи
	// в базе он всё равно ничего не сможет сохранить.
	if err := router.Handle(context.Background(), message("/learn")); err != nil {
		t.Fatalf("Handle() вернул ошибку: %v", err)
	}
	if seen.ID == 0 || seen.TelegramID != 555 {
		t.Errorf("пользователь = %+v", seen)
	}
	// Язык интерфейса берётся из клиента Telegram: первое сообщение должно
	// прийти на понятном языке.
	if seen.UILang != user.UILangRU {
		t.Errorf("UILang = %q, ожидалось ru (language_code=ru)", seen.UILang)
	}
}

func TestIdentifyDoesNotWriteOnEveryUpdate(t *testing.T) {
	t.Parallel()

	users := newFakeUsers()

	router := telegram.NewRouter()
	router.Use(telegram.Identify(users, quietLogger()))
	router.Text(port.UpdateHandlerFunc(func(context.Context, *port.Update) error { return nil }))

	for i := 0; i < 3; i++ {
		if err := router.Handle(context.Background(), message("дом")); err != nil {
			t.Fatalf("Handle() вернул ошибку: %v", err)
		}
	}

	// Запись только при регистрации: каждый апдейт не должен превращаться
	// в поход на запись в базу.
	if users.ensures != 1 {
		t.Errorf("записей в базу %d, ожидалась одна", users.ensures)
	}
}

func TestIdentifyUpdatesChangedUsername(t *testing.T) {
	t.Parallel()

	users := newFakeUsers()

	router := telegram.NewRouter()
	router.Use(telegram.Identify(users, quietLogger()))
	router.Text(port.UpdateHandlerFunc(func(context.Context, *port.Update) error { return nil }))

	if err := router.Handle(context.Background(), message("дом")); err != nil {
		t.Fatalf("Handle() вернул ошибку: %v", err)
	}

	renamed := message("дом")
	renamed.Sender.Username = "pavel"
	if err := router.Handle(context.Background(), renamed); err != nil {
		t.Fatalf("Handle() вернул ошибку: %v", err)
	}

	if users.ensures != 2 {
		t.Errorf("записей в базу %d, ожидалось две: имя сменилось", users.ensures)
	}
	saved, err := users.ByTelegramID(context.Background(), 555)
	if err != nil {
		t.Fatalf("ByTelegramID() вернул ошибку: %v", err)
	}
	if saved.Username != "pavel" {
		t.Errorf("Username = %q, ожидалось pavel", saved.Username)
	}
}

func TestIdentifySurvivesUnusableUsername(t *testing.T) {
	t.Parallel()

	users := newFakeUsers()

	var seen user.User
	router := telegram.NewRouter()
	router.Use(telegram.Identify(users, quietLogger()))
	router.Text(port.UpdateHandlerFunc(func(ctx context.Context, _ *port.Update) error {
		seen, _ = telegram.UserFrom(ctx)
		return nil
	}))

	// Имя приходит из внешнего мира и может не пройти нашу проверку.
	// Терять из-за этого человека нельзя.
	odd := message("дом")
	odd.Sender.Username = "имя кириллицей"
	if err := router.Handle(context.Background(), odd); err != nil {
		t.Fatalf("Handle() вернул ошибку: %v", err)
	}
	if seen.ID == 0 {
		t.Fatal("пользователь не зарегистрирован")
	}
	if seen.Username != "" {
		t.Errorf("Username = %q, ожидалось пустое имя", seen.Username)
	}
}

func TestIdentifyPassesUpdatesWithoutSender(t *testing.T) {
	t.Parallel()

	users := newFakeUsers()

	reached := false
	router := telegram.NewRouter()
	router.Use(telegram.Identify(users, quietLogger()))
	router.Text(port.UpdateHandlerFunc(func(ctx context.Context, _ *port.Update) error {
		reached = true
		if _, ok := telegram.UserFrom(ctx); ok {
			t.Error("пользователь взялся из ниоткуда")
		}
		return nil
	}))

	anonymous := &port.Update{ID: 1, Chat: 777, Text: "дом"}
	if err := router.Handle(context.Background(), anonymous); err != nil {
		t.Fatalf("Handle() вернул ошибку: %v", err)
	}
	if !reached {
		t.Error("апдейт без отправителя не дошёл до хендлера")
	}
	if users.ensures != 0 {
		t.Error("апдейт без отправителя не должен никого регистрировать")
	}
}

func TestIdentifyReportsRepositoryFailure(t *testing.T) {
	t.Parallel()

	broken := newFakeUsers()
	broken.failWith = errors.New("база недоступна")

	reached := false
	router := telegram.NewRouter()
	router.Use(telegram.Identify(broken, quietLogger()))
	router.Text(port.UpdateHandlerFunc(func(context.Context, *port.Update) error {
		reached = true
		return nil
	}))

	// Работать со сломанной базой хендлеру нечем: лучше честная ошибка,
	// чем половина сценария.
	if err := router.Handle(context.Background(), message("дом")); err == nil {
		t.Error("недоступная база должна давать ошибку")
	}
	if reached {
		t.Error("хендлер не должен был получить управление")
	}
}

func TestLocalizePrefersSavedLanguage(t *testing.T) {
	t.Parallel()

	users := newFakeUsers()
	catalog := testCatalog(t)

	var greeting string
	router := telegram.NewRouter()
	router.Use(telegram.Identify(users, quietLogger()), telegram.Localize(catalog))
	router.Text(port.UpdateHandlerFunc(func(ctx context.Context, _ *port.Update) error {
		localizer, ok := telegram.LocalizerFrom(ctx)
		if !ok {
			t.Fatal("локализатор не попал в контекст")
		}
		text, err := localizer.T("common.cancelled", nil)
		if err != nil {
			return err
		}
		greeting = text
		return nil
	}))

	// Пользователь выбрал английский в боте, а клиент Telegram у него
	// русский: выбор в боте важнее.
	wanted := mustUser(t, 555, user.UILangEN)
	saved, _, err := users.Ensure(context.Background(), &wanted)
	if err != nil {
		t.Fatalf("Ensure() вернул ошибку: %v", err)
	}
	if saved.UILang != user.UILangEN {
		t.Fatalf("подготовка теста: UILang = %q", saved.UILang)
	}

	if err := router.Handle(context.Background(), message("дом")); err != nil {
		t.Fatalf("Handle() вернул ошибку: %v", err)
	}
	if greeting != "Cancelled." {
		t.Errorf("сообщение = %q, ожидалось английское", greeting)
	}
}

func TestLocalizeFallsBackToDefault(t *testing.T) {
	t.Parallel()

	catalog := testCatalog(t)

	var text string
	router := telegram.NewRouter()
	router.Use(telegram.Localize(catalog))
	router.Text(port.UpdateHandlerFunc(func(ctx context.Context, _ *port.Update) error {
		localizer, _ := telegram.LocalizerFrom(ctx)
		var err error
		text, err = localizer.T("common.cancelled", nil)
		return err
	}))

	// Клиент на языке, которого мы не знаем: отвечаем на языке по умолчанию,
	// но отвечаем.
	korean := message("집")
	korean.Sender.LanguageCode = "ko"
	if err := router.Handle(context.Background(), korean); err != nil {
		t.Fatalf("Handle() вернул ошибку: %v", err)
	}
	if text == "" {
		t.Error("пользователь остался без ответа")
	}
}

func mustUser(t *testing.T, tgID user.TelegramID, lang user.UILang) user.User {
	t.Helper()

	u, err := user.NewUser(tgID, "durov", lang)
	if err != nil {
		t.Fatalf("NewUser() вернул ошибку: %v", err)
	}
	return u
}

func TestUnknownCommandAnswersInUserLanguage(t *testing.T) {
	t.Parallel()

	messenger := &fakeMessenger{}
	catalog := testCatalog(t)

	router := telegram.NewRouter()
	router.Use(telegram.Localize(catalog))
	router.Unknown(telegram.UnknownCommand(messenger))

	if err := router.Handle(context.Background(), message("/nope")); err != nil {
		t.Fatalf("Handle() вернул ошибку: %v", err)
	}

	// Молчать нельзя: пользователь не различает «такой команды нет»
	// и «бот сломался».
	sent := messenger.last(t)
	if !strings.Contains(sent.Text, "/help") {
		t.Errorf("ответ = %q, ожидалась подсказка про /help", sent.Text)
	}

	english := message("/nope")
	english.Sender.LanguageCode = "en"
	if err := router.Handle(context.Background(), english); err != nil {
		t.Fatalf("Handle() вернул ошибку: %v", err)
	}
	if messenger.last(t).Text == sent.Text {
		t.Error("ответ не переведён на язык пользователя")
	}
}

func TestPingAnswersPong(t *testing.T) {
	t.Parallel()

	messenger := &fakeMessenger{}

	router := telegram.NewRouter()
	router.Command("ping", telegram.Ping(messenger))

	if err := router.Handle(context.Background(), message("/ping")); err != nil {
		t.Fatalf("Handle() вернул ошибку: %v", err)
	}
	if got := messenger.last(t).Text; got != "pong" {
		t.Errorf("ответ = %q, ожидалось pong", got)
	}
}

func TestReplyNeedsLocalizer(t *testing.T) {
	t.Parallel()

	// Без middleware локализации хендлер не должен молча отправлять
	// пустое сообщение — пусть лучше ошибка попадёт в лог.
	messenger := &fakeMessenger{}

	router := telegram.NewRouter()
	router.Unknown(telegram.UnknownCommand(messenger))

	if err := router.Handle(context.Background(), message("/nope")); err == nil {
		t.Error("отсутствие локализатора должно быть ошибкой")
	}
	if messenger.count() != 0 {
		t.Error("боту не следовало ничего отправлять")
	}
}
