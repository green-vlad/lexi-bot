package telegram_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"lexi-bot/internal/adapter/telegram"
	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/onboarding"
	"lexi-bot/internal/usecase/port"
)

var (
	langKO = lexicon.MustParseLanguage("ko")
	langRU = lexicon.MustParseLanguage("ru")
	langEN = lexicon.MustParseLanguage("en")
)

// editingMessenger дополняет fakeMessenger правками: онбординг меняет одно
// и то же сообщение, и проверять нужно именно их.
// screen — то, что пользователь видит на экране после очередного действия:
// отправленное сообщение или правка прежнего. Хранятся вперемешку и по
// порядку, потому что «последнее показанное» — это последнее из обоих,
// а не последнее из одного вида.
type screen struct {
	Text     string
	Keyboard *port.Keyboard
}

type editingMessenger struct {
	fakeMessenger

	mu      sync.Mutex
	edits   []port.MessageEdit
	answers []port.CallbackAnswer
	screens []screen
}

// SendMessage запоминает отправленное и как сообщение, и как экран.
func (m *editingMessenger) SendMessage(ctx context.Context, msg port.OutgoingMessage) (port.MessageID, error) {
	m.mu.Lock()
	m.screens = append(m.screens, screen{Text: msg.Text, Keyboard: msg.Keyboard})
	m.mu.Unlock()

	return m.fakeMessenger.SendMessage(ctx, msg)
}

// latest возвращает последнее показанное пользователю.
func (m *editingMessenger) latest(t *testing.T) screen {
	t.Helper()

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.screens) == 0 {
		t.Fatal("боту следовало что-то показать, но он промолчал")
	}
	return m.screens[len(m.screens)-1]
}

func (m *editingMessenger) EditMessage(_ context.Context, edit port.MessageEdit) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.edits = append(m.edits, edit)
	m.screens = append(m.screens, screen{Text: edit.Text, Keyboard: edit.Keyboard})
	return nil
}

func (m *editingMessenger) AnswerCallback(_ context.Context, answer port.CallbackAnswer) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.answers = append(m.answers, answer)
	return nil
}

func (m *editingMessenger) lastEdit(t *testing.T) port.MessageEdit {
	t.Helper()

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.edits) == 0 {
		t.Fatal("боту следовало исправить сообщение, но он этого не сделал")
	}
	return m.edits[len(m.edits)-1]
}

func (m *editingMessenger) answerCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.answers)
}

// onboardingFixture — бот, готовый провести знакомство.
type onboardingFixture struct {
	router    *telegram.Router
	messenger *editingMessenger
	decks     *stubDecks
	courses   *stubCourses
	settings  *stubSettings
	users     *fakeUsers
	sessions  *fakeSessions
}

func newOnboardingFixture(t *testing.T) *onboardingFixture {
	t.Helper()

	f := &onboardingFixture{
		messenger: &editingMessenger{},
		decks:     newStubDecks(),
		courses:   newStubCourses(),
		settings:  newStubSettings(),
		users:     newFakeUsers(),
		sessions:  newFakeSessions(),
	}

	service, err := onboarding.New(onboarding.Deps{
		Users: f.users, Settings: f.settings, Decks: f.decks, Courses: f.courses,
		DefaultTimezone: user.MustParseTimezone("Europe/Moscow"),
	})
	if err != nil {
		t.Fatalf("onboarding.New() вернул ошибку: %v", err)
	}

	dialogs, err := telegram.NewDialogs(&telegram.DialogsConfig{
		Sessions: f.sessions, Messenger: f.messenger, Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("NewDialogs() вернул ошибку: %v", err)
	}

	catalog := testCatalog(t)
	handler, err := telegram.NewOnboarding(service, dialogs, f.messenger, catalog)
	if err != nil {
		t.Fatalf("NewOnboarding() вернул ошибку: %v", err)
	}

	f.router = telegram.NewRouter()
	f.router.Use(
		telegram.AnswerCallbacks(f.messenger, quietLogger()),
		telegram.Identify(f.users, quietLogger()),
		telegram.Localize(catalog),
		dialogs.Middleware(),
	)
	handler.Register(f.router)
	return f
}

func (f *onboardingFixture) send(t *testing.T, text string) {
	t.Helper()

	if err := f.router.Handle(context.Background(), message(text)); err != nil {
		t.Fatalf("Handle(%q) вернул ошибку: %v", text, err)
	}
}

// press нажимает кнопку с указанными данными.
func (f *onboardingFixture) press(t *testing.T, data string) {
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

// buttons возвращает данные кнопок последнего показанного экрана.
func (f *onboardingFixture) buttons(t *testing.T) []string {
	t.Helper()

	var keyboard *port.Keyboard
	f.messenger.mu.Lock()
	if len(f.messenger.edits) > 0 {
		keyboard = f.messenger.edits[len(f.messenger.edits)-1].Keyboard
	}
	f.messenger.mu.Unlock()

	if keyboard == nil {
		keyboard = f.messenger.last(t).Keyboard
	}
	if keyboard == nil {
		t.Fatal("на экране нет кнопок")
	}

	var out []string
	for _, row := range keyboard.Rows {
		for _, b := range row {
			out = append(out, b.Data)
		}
	}
	return out
}

func TestStartWalksThroughOnboarding(t *testing.T) {
	t.Parallel()

	f := newOnboardingFixture(t)

	f.send(t, "/start")

	// Первый экран: приветствие и выбор языка изучения. Язык интерфейса
	// не спрашивается — он определился по клиенту Telegram.
	if got := f.buttons(t); len(got) != 2 {
		t.Fatalf("кнопки первого экрана = %v", got)
	}
	if greeting := f.messenger.sent[0].Text; !strings.Contains(greeting, "durov") {
		t.Errorf("приветствие без имени: %q", greeting)
	}

	f.press(t, "oblang:0:ko")
	deckButtons := f.buttons(t)
	if len(deckButtons) != 2 {
		t.Fatalf("на экране колод кнопок %v, ожидались колода и «назад»", deckButtons)
	}
	if deckButtons[0] != "obdeck:1" {
		t.Errorf("кнопка колоды = %q", deckButtons[0])
	}

	f.press(t, "obdeck:1")
	translationButtons := f.buttons(t)
	if len(translationButtons) != 3 {
		t.Fatalf("на экране языков перевода кнопки %v", translationButtons)
	}

	f.press(t, "obtr:0:ru")

	// Курс заведён, кнопки убраны, диалог закрыт.
	final := f.messenger.lastEdit(t)
	if final.Keyboard != nil {
		t.Error("после завершения кнопки должны исчезнуть")
	}
	if !strings.Contains(final.Text, "топ-2000") {
		t.Errorf("итоговое сообщение = %q, ожидалось название колоды", final.Text)
	}
	if _, ok := f.sessions.current(t); ok {
		t.Error("диалог должен быть закрыт")
	}

	courses, err := f.courses.ByUser(context.Background(), 1)
	if err != nil {
		t.Fatalf("ByUser() вернул ошибку: %v", err)
	}
	if len(courses) != 1 {
		t.Fatalf("курсов %d, ожидался один", len(courses))
	}
	if courses[0].TranslationLang != langRU || courses[0].DeckID != 1 {
		t.Errorf("курс = %+v", courses[0])
	}

	// Настройки завелись по умолчанию.
	settings, err := f.settings.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("настройки не сохранены: %v", err)
	}
	if settings.NewPerDay != user.DefaultNewPerDay || settings.RemindersEnabled() {
		t.Errorf("настройки = %+v", settings)
	}
}

func TestStartGoesBack(t *testing.T) {
	t.Parallel()

	f := newOnboardingFixture(t)

	f.send(t, "/start")
	f.press(t, "oblang:0:ko")
	f.press(t, "obdeck:1")

	// Назад с языков перевода — снова колоды.
	f.press(t, "obback:0:"+"onboarding:deck")
	if got := f.buttons(t); got[0] != "obdeck:1" {
		t.Fatalf("после «назад» кнопки = %v, ожидались колоды", got)
	}

	// Назад с колод — снова языки изучения.
	f.press(t, "obback:0:"+"onboarding:learning")
	got := f.buttons(t)
	if len(got) != 2 || !strings.HasPrefix(got[0], "oblang:") {
		t.Fatalf("после «назад» кнопки = %v, ожидались языки", got)
	}

	// И путь можно пройти заново, уже до конца.
	f.press(t, "oblang:0:ko")
	f.press(t, "obdeck:1")
	f.press(t, "obtr:0:ru")

	courses, err := f.courses.ByUser(context.Background(), 1)
	if err != nil {
		t.Fatalf("ByUser() вернул ошибку: %v", err)
	}
	if len(courses) != 1 {
		t.Errorf("курсов %d, ожидался один", len(courses))
	}
}

func TestStartAnswersCallbacks(t *testing.T) {
	t.Parallel()

	f := newOnboardingFixture(t)

	f.send(t, "/start")
	f.press(t, "oblang:0:ko")

	// Без ответа на нажатие у пользователя крутятся «часики».
	if f.messenger.answerCount() != 1 {
		t.Errorf("ответов на нажатия %d, ожидался один", f.messenger.answerCount())
	}
}

func TestStartIgnoresStrayInput(t *testing.T) {
	t.Parallel()

	f := newOnboardingFixture(t)

	f.send(t, "/start")

	// Текст вместо нажатия и кнопка от чужого шага: диалог остаётся
	// на месте, а не рушится.
	f.send(t, "просто текст")
	f.press(t, "obdeck:1")

	session, ok := f.sessions.current(t)
	if !ok {
		t.Fatal("диалог не должен был закрыться")
	}
	if session.State != "onboarding:learning" {
		t.Errorf("состояние = %q, ожидался выбор языка", session.State)
	}
}

func TestStartWhenAlreadyLearning(t *testing.T) {
	t.Parallel()

	f := newOnboardingFixture(t)

	f.send(t, "/start")
	f.press(t, "oblang:0:ko")
	f.press(t, "obdeck:1")
	f.press(t, "obtr:0:ru")

	// Повторный /start: человек вернулся заниматься, а не знакомиться.
	f.send(t, "/start")

	last := f.messenger.last(t)
	if !strings.Contains(last.Text, "топ-2000") || !strings.Contains(last.Text, "/learn") {
		t.Errorf("ответ вернувшемуся = %q", last.Text)
	}
	if last.Keyboard != nil {
		t.Error("вернувшемуся не нужно предлагать кнопки знакомства")
	}
	if _, ok := f.sessions.current(t); ok {
		t.Error("повторный /start не должен начинать диалог")
	}
}

func TestStartWithoutDecks(t *testing.T) {
	t.Parallel()

	f := newOnboardingFixture(t)
	f.decks.decks = map[lexicon.DeckID]lexicon.Deck{}

	// Словари не загружены: об этом надо сказать словами, а не молчать
	// и не показывать «что-то пошло не так».
	f.send(t, "/start")

	last := f.messenger.last(t)
	if !strings.Contains(strings.ToLower(last.Text), "словари") {
		t.Errorf("ответ = %q, ожидалось объяснение про словари", last.Text)
	}
	if _, ok := f.sessions.current(t); ok {
		t.Error("диалог не должен был начаться")
	}
}

func TestStartAsksUILangWhenUnknown(t *testing.T) {
	t.Parallel()

	f := newOnboardingFixture(t)

	// Клиент Telegram на языке, которого мы не знаем: спрашиваем язык
	// интерфейса, иначе человек не прочитает ни одного вопроса.
	update := message("/start")
	update.Sender.LanguageCode = "ko"
	if err := f.router.Handle(context.Background(), update); err != nil {
		t.Fatalf("Handle() вернул ошибку: %v", err)
	}

	session, ok := f.sessions.current(t)
	if !ok || session.State != "onboarding:ui_lang" {
		t.Fatalf("состояние = %+v, ожидался выбор языка интерфейса", session)
	}

	buttons := f.buttons(t)
	if len(buttons) != 2 || !strings.HasPrefix(buttons[0], "obui:") {
		t.Fatalf("кнопки = %v", buttons)
	}

	f.press(t, "obui:0:en")

	if f.users.byTgID[555].UILang != user.UILangEN {
		t.Errorf("язык интерфейса = %q", f.users.byTgID[555].UILang)
	}
	session, ok = f.sessions.current(t)
	if !ok || session.State != "onboarding:learning" {
		t.Errorf("после выбора языка состояние = %+v", session)
	}
	// И следующий вопрос уже на выбранном языке.
	if got := f.messenger.lastEdit(t).Text; !strings.Contains(got, "learning") {
		t.Errorf("вопрос = %q, ожидался английский", got)
	}
}

func TestOnboardingHandlerNeedsDependencies(t *testing.T) {
	t.Parallel()

	if _, err := telegram.NewOnboarding(nil, nil, nil, nil); err == nil {
		t.Error("хендлер без зависимостей должен быть ошибкой")
	}
}

// Заглушки репозиториев: те же, что в тестах сценария, но здесь нужны
// в пакете адаптера.

type stubDecks struct {
	decks        map[lexicon.DeckID]lexicon.Deck
	translations map[lexicon.DeckID][]lexicon.Language
}

func newStubDecks() *stubDecks {
	return &stubDecks{
		decks: map[lexicon.DeckID]lexicon.Deck{
			1: {ID: 1, Code: "ko-top-2000", Lang: langKO, Title: "Корейский: топ-2000", Size: 2000},
		},
		translations: map[lexicon.DeckID][]lexicon.Language{
			1: {langEN, langRU},
		},
	}
}

func (s *stubDecks) Languages(context.Context) ([]lexicon.Language, error) {
	var out []lexicon.Language
	seen := map[lexicon.Language]bool{}
	for _, deck := range s.decks {
		if !seen[deck.Lang] {
			seen[deck.Lang] = true
			out = append(out, deck.Lang)
		}
	}
	// Чтобы кнопок было две даже с одной колодой: онбординг показывает
	// языки, а не колоды.
	if len(out) == 1 {
		out = append(out, langEN)
	}
	return out, nil
}

func (s *stubDecks) TranslationLanguages(_ context.Context, id lexicon.DeckID) ([]lexicon.Language, error) {
	return s.translations[id], nil
}

func (s *stubDecks) Builtin(_ context.Context, lang lexicon.Language) ([]lexicon.Deck, error) {
	var out []lexicon.Deck
	for _, deck := range s.decks {
		if deck.Lang == lang {
			out = append(out, deck)
		}
	}
	return out, nil
}

func (s *stubDecks) ByID(_ context.Context, id lexicon.DeckID) (lexicon.Deck, error) {
	if deck, ok := s.decks[id]; ok {
		return deck, nil
	}
	return lexicon.Deck{}, port.ErrNotFound
}

func (s *stubDecks) ByCode(context.Context, string) (lexicon.Deck, error) {
	return lexicon.Deck{}, port.ErrNotFound
}

func (s *stubDecks) EnsurePersonal(context.Context, int64, lexicon.Language, string) (lexicon.Deck, error) {
	return lexicon.Deck{}, nil
}
func (s *stubDecks) AddItems(context.Context, []lexicon.DeckItem) error { return nil }

func (s *stubDecks) Distractors(context.Context, port.DistractorQuery) ([]lexicon.Translation, error) {
	return nil, nil
}
func (s *stubDecks) Items(context.Context, lexicon.DeckID, int, int) ([]lexicon.DeckItem, error) {
	return nil, nil
}

type stubCourses struct {
	byID   map[study.CourseID]study.Course
	nextID study.CourseID
}

func newStubCourses() *stubCourses {
	return &stubCourses{byID: map[study.CourseID]study.Course{}, nextID: 1}
}

func (s *stubCourses) Ensure(_ context.Context, c study.Course) (study.Course, error) {
	for _, existing := range s.byID {
		if existing.UserID == c.UserID && existing.DeckID == c.DeckID && existing.TranslationLang == c.TranslationLang {
			return existing, nil
		}
	}
	c.ID = s.nextID
	s.nextID++
	s.byID[c.ID] = c
	return c, nil
}

func (s *stubCourses) ByID(_ context.Context, id study.CourseID) (study.Course, error) {
	if c, ok := s.byID[id]; ok {
		return c, nil
	}
	return study.Course{}, port.ErrNotFound
}

func (s *stubCourses) ByUser(_ context.Context, userID user.ID) ([]study.Course, error) {
	var out []study.Course
	for _, c := range s.byID {
		if c.UserID == int64(userID) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *stubCourses) SetStatus(context.Context, study.CourseID, study.CourseStatus) error {
	return nil
}

type stubSettings struct {
	byUser map[user.ID]user.Settings
}

func newStubSettings() *stubSettings {
	return &stubSettings{byUser: map[user.ID]user.Settings{}}
}

func (s *stubSettings) Get(_ context.Context, userID user.ID) (user.Settings, error) {
	if settings, ok := s.byUser[userID]; ok {
		return settings, nil
	}
	return user.Settings{}, port.ErrNotFound
}

func (s *stubSettings) Save(_ context.Context, userID user.ID, settings user.Settings) error {
	s.byUser[userID] = settings
	return nil
}
