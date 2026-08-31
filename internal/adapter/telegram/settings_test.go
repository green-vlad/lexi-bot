package telegram_test

import (
	"context"
	"strings"
	"testing"

	"lexi-bot/internal/adapter/telegram"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/internal/usecase/settings"
)

type settingsFixture struct {
	router    *telegram.Router
	messenger *editingMessenger
	sessions  *fakeSessions
	repo      *stubSettings
	users     *fakeUsers
}

func newSettingsFixture(t *testing.T) *settingsFixture {
	t.Helper()

	f := &settingsFixture{
		messenger: &editingMessenger{},
		sessions:  newFakeSessions(),
		repo:      newStubSettings(),
		users:     newFakeUsers(),
	}

	owner := mustUser(t, 555, user.UILangRU)
	saved, _, err := f.users.Ensure(context.Background(), &owner)
	if err != nil {
		t.Fatalf("Ensure() вернул ошибку: %v", err)
	}
	f.repo.byUser[saved.ID] = user.DefaultSettings(user.MustParseTimezone("Asia/Seoul"))

	service, err := settings.New(settings.Deps{Settings: f.repo})
	if err != nil {
		t.Fatalf("settings.New() вернул ошибку: %v", err)
	}

	dialogs, err := telegram.NewDialogs(&telegram.DialogsConfig{
		Sessions: f.sessions, Messenger: f.messenger, Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("NewDialogs() вернул ошибку: %v", err)
	}

	handler, err := telegram.NewSettings(service, dialogs, f.messenger)
	if err != nil {
		t.Fatalf("NewSettings() вернул ошибку: %v", err)
	}

	f.router = telegram.NewRouter()
	f.router.Use(
		telegram.AnswerCallbacks(f.messenger, quietLogger()),
		telegram.Identify(f.users, quietLogger()),
		telegram.Localize(testCatalog(t)),
		dialogs.Middleware(),
	)
	handler.Register(f.router)
	return f
}

func (f *settingsFixture) send(t *testing.T, text string) {
	t.Helper()

	if err := f.router.Handle(context.Background(), message(text)); err != nil {
		t.Fatalf("Handle(%q) вернул ошибку: %v", text, err)
	}
}

func (f *settingsFixture) press(t *testing.T, data string) {
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

func (f *settingsFixture) screen(t *testing.T) (text string, buttons []string) {
	t.Helper()

	last := f.messenger.latest(t)
	if last.Keyboard != nil {
		for _, row := range last.Keyboard.Rows {
			for _, b := range row {
				buttons = append(buttons, b.Data)
			}
		}
	}
	return last.Text, buttons
}

// открывает редактор поля.
func (f *settingsFixture) edit(t *testing.T, field string) {
	t.Helper()

	f.send(t, "/settings")
	_, buttons := f.screen(t)
	for _, data := range buttons {
		if strings.HasSuffix(data, ":"+field) {
			f.press(t, data)
			return
		}
	}
	t.Fatalf("в настройках нет строки %q: %v", field, buttons)
}

func (f *settingsFixture) current(t *testing.T) user.Settings {
	t.Helper()

	current, err := f.repo.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("Get() вернул ошибку: %v", err)
	}
	return current
}

func TestSettingsShowsEveryOption(t *testing.T) {
	t.Parallel()

	f := newSettingsFixture(t)

	f.send(t, "/settings")

	text, buttons := f.screen(t)
	// На экране все шесть настроек с текущими значениями.
	for _, want := range []string{"Новых слов в день: 5", "Повторений в день", "Как спрашивать",
		"Направление: слово → перевод", "Напоминание: выключено", "Таймзона: Asia/Seoul"} {
		if !strings.Contains(text, want) {
			t.Errorf("экран = %q, в нём нет %q", text, want)
		}
	}
	if len(buttons) != 6 {
		t.Errorf("кнопок %d, ожидалось шесть: %v", len(buttons), buttons)
	}
}

func TestSettingsChangesNewPerDay(t *testing.T) {
	t.Parallel()

	f := newSettingsFixture(t)
	f.edit(t, "new")

	text, buttons := f.screen(t)
	// Человека предупреждают, что сегодняшний день правка не задевает.
	if !strings.Contains(text, "завтра") {
		t.Errorf("экран = %q, ожидалось предупреждение про завтра", text)
	}

	var twenty string
	for _, data := range buttons {
		if strings.HasSuffix(data, "new:20") {
			twenty = data
		}
	}
	if twenty == "" {
		t.Fatalf("нет кнопки на 20: %v", buttons)
	}

	f.press(t, twenty)

	if got := f.current(t).NewPerDay; got != 20 {
		t.Errorf("норма = %d, ожидалось 20", got)
	}
	// После правки человек видит список целиком с новым значением.
	if text, _ = f.screen(t); !strings.Contains(text, "Новых слов в день: 20") {
		t.Errorf("экран = %q", text)
	}
}

func TestSettingsTogglesModesButKeepsOne(t *testing.T) {
	t.Parallel()

	f := newSettingsFixture(t)
	f.edit(t, "mode")

	_, buttons := f.screen(t)
	var typing, choice string
	for _, data := range buttons {
		switch {
		case strings.HasSuffix(data, "mode:typing"):
			typing = data
		case strings.HasSuffix(data, "mode:choice"):
			choice = data
		}
	}
	if typing == "" || choice == "" {
		t.Fatalf("кнопки режимов = %v", buttons)
	}

	f.press(t, typing)
	if f.current(t).ModeEnabled(study.ModeTyping) {
		t.Error("ввод текстом не выключился")
	}

	// Последний оставшийся режим выключить нельзя.
	f.edit(t, "mode")
	f.press(t, choice)
	if !f.current(t).ModeEnabled(study.ModeChoice) {
		t.Error("последний режим выключился — карточку станет нечем показать")
	}
	if text, _ := f.screen(t); !strings.Contains(text, "единственный") {
		t.Errorf("ответ = %q, ожидалось объяснение", text)
	}
}

func TestSettingsTogglesDirection(t *testing.T) {
	t.Parallel()

	f := newSettingsFixture(t)
	f.edit(t, "dir")

	_, buttons := f.screen(t)
	if len(buttons) < 1 {
		t.Fatalf("кнопки = %v", buttons)
	}
	f.press(t, buttons[0])

	if !f.current(t).ReverseDirection {
		t.Error("направление не переключилось")
	}
	if text, _ := f.screen(t); !strings.Contains(text, "перевод → слово") {
		t.Errorf("экран = %q", text)
	}
}

func TestSettingsSetsAndClearsReminder(t *testing.T) {
	t.Parallel()

	f := newSettingsFixture(t)
	f.edit(t, "rem")

	_, buttons := f.screen(t)
	var at, off string
	for _, data := range buttons {
		switch {
		case strings.HasSuffix(data, "rem:21:30"):
			at = data
		case strings.HasSuffix(data, "rem:off"):
			off = data
		}
	}
	if at == "" || off == "" {
		t.Fatalf("кнопки напоминания = %v", buttons)
	}

	f.press(t, at)
	current := f.current(t)
	if !current.RemindersEnabled() || current.ReminderAt.String() != "21:30" {
		t.Errorf("напоминание = %q", current.ReminderAt)
	}

	f.edit(t, "rem")
	f.press(t, off)
	if f.current(t).RemindersEnabled() {
		t.Error("напоминание не выключилось")
	}
}

func TestSettingsAcceptsTypedTimezone(t *testing.T) {
	t.Parallel()

	f := newSettingsFixture(t)
	f.edit(t, "tz")

	if text, _ := f.screen(t); !strings.Contains(text, "Asia/Seoul") {
		t.Errorf("подсказка = %q", text)
	}
	if dialog, ok := f.sessions.current(t); !ok || dialog.State != "settings:timezone" {
		t.Fatalf("состояние = %+v, ожидалось ожидание ввода", dialog)
	}

	f.send(t, "Europe/Moscow")

	if got := f.current(t).Timezone.String(); got != "Europe/Moscow" {
		t.Errorf("таймзона = %q", got)
	}
	// Диалог закончился, и человек снова видит список настроек.
	if _, ok := f.sessions.current(t); ok {
		t.Error("диалог остался висеть")
	}
	if text, _ := f.screen(t); !strings.Contains(text, "Таймзона: Europe/Moscow") {
		t.Errorf("экран = %q", text)
	}
}

func TestSettingsKeepsAskingAfterBadValue(t *testing.T) {
	t.Parallel()

	f := newSettingsFixture(t)
	f.edit(t, "tz")

	f.send(t, "Марс/Олимп")

	// Опечатка не сбрасывает диалог: заставлять начинать заново
	// из-за неё было бы наказанием.
	if dialog, ok := f.sessions.current(t); !ok || dialog.State != "settings:timezone" {
		t.Errorf("состояние = %+v, ожидалось прежнее ожидание", dialog)
	}
	if got := f.current(t).Timezone.String(); got != "Asia/Seoul" {
		t.Errorf("таймзона = %q, ожидалась прежняя", got)
	}
	if text := f.messenger.latest(t).Text; !strings.Contains(text, "не подходит") {
		t.Errorf("ответ = %q", text)
	}
}

func TestSettingsRejectsOutOfRangeTypedNumber(t *testing.T) {
	t.Parallel()

	f := newSettingsFixture(t)
	f.edit(t, "new")

	_, buttons := f.screen(t)
	var custom string
	for _, data := range buttons {
		if strings.HasSuffix(data, "newask") {
			custom = data
		}
	}
	if custom == "" {
		t.Fatalf("нет кнопки своего значения: %v", buttons)
	}

	f.press(t, custom)
	f.send(t, "1000")

	if got := f.current(t).NewPerDay; got != 5 {
		t.Errorf("норма = %d, ожидалась прежняя", got)
	}
	if text := f.messenger.latest(t).Text; !strings.Contains(text, "не подходит") {
		t.Errorf("ответ = %q", text)
	}

	// А годное значение принимается.
	f.send(t, "12")
	if got := f.current(t).NewPerDay; got != 12 {
		t.Errorf("норма = %d, ожидалось 12", got)
	}
}

func TestSettingsNeedsDependencies(t *testing.T) {
	t.Parallel()

	if _, err := telegram.NewSettings(nil, nil, nil); err == nil {
		t.Error("хендлер без зависимостей должен быть ошибкой")
	}
}
