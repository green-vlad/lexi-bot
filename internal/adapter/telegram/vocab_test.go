package telegram_test

import (
	"context"
	"strings"
	"testing"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/internal/usecase/vocab"

	"lexi-bot/internal/adapter/telegram"
)

type vocabFixture struct {
	router    *telegram.Router
	messenger *fakeMessenger
	sessions  *fakeSessions
	decks     *stubDecks
	lexemes   *stubLexemes
	courses   *stubCourses
	users     *fakeUsers
	builtin   lexicon.DeckID
}

func newVocabFixture(t *testing.T) *vocabFixture {
	t.Helper()

	f := &vocabFixture{
		messenger: &fakeMessenger{},
		sessions:  newFakeSessions(),
		decks:     newStubDecks(),
		lexemes:   newStubLexemes(),
		courses:   newStubCourses(),
		users:     newFakeUsers(),
	}

	owner := mustUser(t, 555, user.UILangRU)
	saved, _, err := f.users.Ensure(context.Background(), &owner)
	if err != nil {
		t.Fatalf("Ensure() вернул ошибку: %v", err)
	}

	// Встроенная колода у заглушки уже есть — заводить вторую незачем.
	f.builtin = lexicon.DeckID(1)
	course, err := f.courses.Ensure(context.Background(), study.Course{
		UserID: int64(saved.ID), DeckID: f.builtin, TranslationLang: langRU, Status: study.CourseActive,
	})
	if err != nil {
		t.Fatalf("Ensure() вернул ошибку: %v", err)
	}
	if err := f.users.SetCurrentCourse(context.Background(), saved.ID, course.ID); err != nil {
		t.Fatalf("SetCurrentCourse() вернул ошибку: %v", err)
	}

	service, err := vocab.New(vocab.Deps{
		Users: f.users, Decks: f.decks, Lexemes: f.lexemes, Courses: f.courses,
	})
	if err != nil {
		t.Fatalf("vocab.New() вернул ошибку: %v", err)
	}

	dialogs, err := telegram.NewDialogs(&telegram.DialogsConfig{
		Sessions: f.sessions, Messenger: f.messenger, Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("NewDialogs() вернул ошибку: %v", err)
	}

	handler, err := telegram.NewVocab(service, dialogs, f.messenger)
	if err != nil {
		t.Fatalf("NewVocab() вернул ошибку: %v", err)
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

func (f *vocabFixture) send(t *testing.T, text string) {
	t.Helper()

	if err := f.router.Handle(context.Background(), message(text)); err != nil {
		t.Fatalf("Handle(%q) вернул ошибку: %v", text, err)
	}
}

func (f *vocabFixture) press(t *testing.T, data string) {
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

// skip нажимает кнопку «пропустить» на текущем экране.
func (f *vocabFixture) skip(t *testing.T) {
	t.Helper()

	last := f.messenger.last(t)
	if last.Keyboard == nil || len(last.Keyboard.Rows) == 0 {
		t.Fatalf("на экране %q нет кнопки пропуска", last.Text)
	}
	f.press(t, last.Keyboard.Rows[0][0].Data)
}

func TestAddWalksThroughDialog(t *testing.T) {
	t.Parallel()

	f := newVocabFixture(t)

	f.send(t, "/add")
	if got := f.messenger.last(t).Text; !strings.Contains(got, "Какое слово") {
		t.Errorf("первый вопрос = %q", got)
	}

	f.send(t, "냉장고")
	if got := f.messenger.last(t).Text; !strings.Contains(got, "переводится") {
		t.Errorf("второй вопрос = %q", got)
	}

	f.send(t, "холодильник; морозильник")
	// Необязательные шаги приходят с кнопкой пропуска.
	if f.messenger.last(t).Keyboard == nil {
		t.Error("у вопроса про чтение нет кнопки пропуска")
	}

	f.send(t, "nengjanggo")
	f.send(t, "냉장고에 물이 있어요.")

	text := f.messenger.last(t).Text
	if !strings.Contains(text, "Добавлено") || !strings.Contains(text, "냉장고") {
		t.Errorf("итог = %q", text)
	}
	// Оба перевода дошли до ответа: точка с запятой делит значения.
	if !strings.Contains(text, "холодильник") || !strings.Contains(text, "морозильник") {
		t.Errorf("итог = %q, ожидались оба перевода", text)
	}

	// Диалог закончился: следующее сообщение уже не ответ на шаг.
	if _, ok := f.sessions.current(t); ok {
		t.Error("диалог остался висеть после сохранения")
	}

	lexeme, err := f.lexemes.ByTerm(context.Background(), langKO, "냉장고", 1)
	if err != nil {
		t.Fatalf("слово не сохранилось: %v", err)
	}
	if lexeme.Reading != "nengjanggo" || lexeme.Example != "냉장고에 물이 있어요." {
		t.Errorf("слово = %+v: чтение или пример потеряны", lexeme)
	}
}

func TestAddTakesWordFromCommand(t *testing.T) {
	t.Parallel()

	f := newVocabFixture(t)

	// Слово пришло сразу — первый вопрос задавать незачем.
	f.send(t, "/add 냉장고")
	if got := f.messenger.last(t).Text; !strings.Contains(got, "переводится") {
		t.Fatalf("вопрос = %q, ожидался вопрос про перевод", got)
	}

	f.send(t, "холодильник")
	f.skip(t)
	f.skip(t)

	if got := f.messenger.last(t).Text; !strings.Contains(got, "Добавлено") {
		t.Errorf("итог = %q", got)
	}

	lexeme, err := f.lexemes.ByTerm(context.Background(), langKO, "냉장고", 1)
	if err != nil {
		t.Fatalf("слово не сохранилось: %v", err)
	}
	// Пропущенные шаги остались пустыми, а не заполнились текстом кнопки.
	if lexeme.Reading != "" || lexeme.Example != "" {
		t.Errorf("слово = %+v: пропущенные поля не пусты", lexeme)
	}
}

func TestAddCancelSavesNothing(t *testing.T) {
	t.Parallel()

	f := newVocabFixture(t)

	f.send(t, "/add 냉장고")
	f.send(t, "холодильник")
	f.send(t, "/cancel")

	if _, ok := f.sessions.current(t); ok {
		t.Error("/cancel должен сбрасывать диалог")
	}
	// Слово записывается последним шагом, и до него в базе пусто:
	// прерванный диалог не оставляет ни лексемы, ни колоды, ни курса.
	if _, err := f.lexemes.ByTerm(context.Background(), langKO, "냉장고", 1); err == nil {
		t.Error("прерванный диалог сохранил слово")
	}
	if f.decks.personal != 0 {
		t.Error("прерванный диалог завёл личную колоду")
	}
	if len(f.courses.byID) != 1 {
		t.Errorf("курсов %d, ожидался один: прерванный диалог завёл курс", len(f.courses.byID))
	}
}

func TestAddRepeatsQuestionOnEmptyInput(t *testing.T) {
	t.Parallel()

	f := newVocabFixture(t)

	f.send(t, "/add 냉장고")
	// Строка из одних разделителей — не перевод: переспрашиваем.
	f.send(t, " ; / ; ")

	if got := f.messenger.last(t).Text; !strings.Contains(got, "переводится") {
		t.Errorf("ответ = %q, ожидался повтор вопроса", got)
	}
	session, ok := f.sessions.current(t)
	if !ok || session.State != "add:translation" {
		t.Errorf("состояние = %+v, ожидалось ожидание перевода", session)
	}
}

func TestAddWithoutCourse(t *testing.T) {
	t.Parallel()

	f := newVocabFixture(t)
	f.courses.byID = map[study.CourseID]study.Course{}

	f.send(t, "/add 냉장고")
	f.send(t, "холодильник")
	f.skip(t)
	f.skip(t)

	if got := f.messenger.last(t).Text; !strings.Contains(got, "/start") {
		t.Errorf("ответ = %q, ожидалась подсказка про /start", got)
	}
}

func TestAddRefusesWordAlreadyInCourse(t *testing.T) {
	t.Parallel()

	f := newVocabFixture(t)

	// Слово уже во встроенной колоде, которую человек учит.
	builtin := f.lexemes.addLexeme(&lexicon.Lexeme{Lang: langKO, Term: "냉장고", POS: lexicon.POSNoun})
	if err := f.decks.AddItems(context.Background(), []lexicon.DeckItem{
		{DeckID: f.builtin, LexemeID: builtin, Position: 0},
	}); err != nil {
		t.Fatalf("AddItems() вернул ошибку: %v", err)
	}

	f.send(t, "/add 냉장고")
	f.send(t, "холодильник")
	f.skip(t)
	f.skip(t)

	if got := f.messenger.last(t).Text; !strings.Contains(got, "своим чередом") {
		t.Errorf("ответ = %q, ожидалось объяснение про уже учащееся слово", got)
	}
	if f.decks.personal != 0 {
		t.Error("под слово, которое и так учится, заведена личная колода")
	}
}

func TestVocabNeedsDependencies(t *testing.T) {
	t.Parallel()

	if _, err := telegram.NewVocab(nil, nil, nil); err == nil {
		t.Error("хендлер без зависимостей должен быть ошибкой")
	}
}
