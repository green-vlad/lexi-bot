package telegram_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"lexi-bot/internal/adapter/telegram"
	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/courses"
	"lexi-bot/internal/usecase/port"
)

// decksFixture — бот с двумя курсами у одного пользователя.
type decksFixture struct {
	router    *telegram.Router
	messenger *editingMessenger
	users     *fakeUsers
	courses   *stubCourses
	cards     *stubCards
	owner     user.User
	first     study.Course
	second    study.Course
}

func newDecksFixture(t *testing.T) *decksFixture {
	t.Helper()

	ctx := context.Background()
	f := &decksFixture{
		messenger: &editingMessenger{},
		users:     newFakeUsers(),
		courses:   newStubCourses(),
	}

	owner := mustUser(t, 555, user.UILangRU)
	saved, _, err := f.users.Ensure(ctx, &owner)
	if err != nil {
		t.Fatalf("Ensure() вернул ошибку: %v", err)
	}
	f.owner = saved

	f.first, err = f.courses.Ensure(ctx, study.Course{
		UserID: int64(saved.ID), DeckID: 1, TranslationLang: langRU, Status: study.CourseActive,
	})
	if err != nil {
		t.Fatalf("Ensure() вернул ошибку: %v", err)
	}
	f.second, err = f.courses.Ensure(ctx, study.Course{
		UserID: int64(saved.ID), DeckID: 2, TranslationLang: langRU, Status: study.CourseActive,
	})
	if err != nil {
		t.Fatalf("Ensure() вернул ошибку: %v", err)
	}

	decks := &stubDecks{
		decks: map[lexicon.DeckID]lexicon.Deck{
			1: {ID: 1, Code: "ko-lesson-1", Lang: langKO, Title: "Урок 1", Size: 23},
			2: {ID: 2, Code: "ko-lesson-2", Lang: langKO, Title: "Урок 2", Size: 29},
		},
	}
	f.cards = newStubCards(nil, f.first.ID)

	service, err := courses.New(courses.Deps{
		Users: f.users, Courses: f.courses, Decks: decks, Cards: f.cards,
	})
	if err != nil {
		t.Fatalf("courses.New() вернул ошибку: %v", err)
	}

	handler, err := telegram.NewDecks(service, f.messenger)
	if err != nil {
		t.Fatalf("NewDecks() вернул ошибку: %v", err)
	}

	f.router = telegram.NewRouter()
	f.router.Use(
		telegram.AnswerCallbacks(f.messenger, quietLogger()),
		telegram.Identify(f.users, quietLogger()),
		telegram.Localize(testCatalog(t)),
	)
	handler.Register(f.router)
	return f
}

func (f *decksFixture) send(t *testing.T, text string) {
	t.Helper()

	if err := f.router.Handle(context.Background(), message(text)); err != nil {
		t.Fatalf("Handle(%q) вернул ошибку: %v", text, err)
	}
}

func (f *decksFixture) press(t *testing.T, data string) {
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

func (f *decksFixture) screen(t *testing.T) (text string, buttons []string) {
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

func TestDecksListsCourses(t *testing.T) {
	t.Parallel()

	f := newDecksFixture(t)
	f.send(t, "/decks")

	text, buttons := f.screen(t)
	for _, title := range []string{"Урок 1", "Урок 2"} {
		if !strings.Contains(text, title) {
			t.Errorf("в списке нет курса %q: %q", title, text)
		}
	}

	// Первый курс — текущий: занятие идёт по нему, и кнопки «учить»
	// у него быть не должно.
	if strings.Contains(text, "▸ Урок 1") == false {
		t.Errorf("текущий курс не отмечен: %q", text)
	}
	var learnFirst, learnSecond bool
	for _, data := range buttons {
		switch data {
		case "cl:1":
			learnFirst = true
		case "cl:2":
			learnSecond = true
		}
	}
	if learnFirst {
		t.Error("у текущего курса не нужна кнопка «учить»")
	}
	if !learnSecond {
		t.Errorf("у второго курса нет кнопки «учить»: %v", buttons)
	}

	// И кнопка «добавить курс» — она ведёт в тот же диалог, что знакомство.
	if !contains(buttons, "cadd") {
		t.Errorf("нет кнопки добавления курса: %v", buttons)
	}
}

func TestDecksSwitchesCurrent(t *testing.T) {
	t.Parallel()

	f := newDecksFixture(t)
	f.send(t, "/decks")

	f.press(t, "cl:2")

	saved, err := f.users.ByID(context.Background(), f.owner.ID)
	if err != nil {
		t.Fatalf("ByID() вернул ошибку: %v", err)
	}
	if saved.CurrentCourse != f.second.ID {
		t.Errorf("текущий курс = %d, ожидался %d", saved.CurrentCourse, f.second.ID)
	}

	// Список перерисован: теперь отмечен второй курс.
	text, _ := f.screen(t)
	if !strings.Contains(text, "▸ Урок 2") {
		t.Errorf("список не обновился: %q", text)
	}
}

func TestDecksPauseAndResume(t *testing.T) {
	t.Parallel()

	f := newDecksFixture(t)
	f.send(t, "/decks")

	f.press(t, "cp:1")

	course, err := f.courses.ByID(context.Background(), f.first.ID)
	if err != nil {
		t.Fatalf("ByID() вернул ошибку: %v", err)
	}
	if course.Status != study.CoursePaused {
		t.Errorf("состояние = %v, ожидалась пауза", course.Status)
	}

	text, buttons := f.screen(t)
	if !strings.Contains(text, "⏸ Урок 1") {
		t.Errorf("пауза не отмечена: %q", text)
	}
	if !contains(buttons, "cr:1") {
		t.Errorf("нет кнопки «продолжить»: %v", buttons)
	}

	// «Продолжить» не просто снимает паузу, а возвращает к курсу: человек
	// нажимает её, чтобы заниматься.
	f.press(t, "cr:1")

	course, err = f.courses.ByID(context.Background(), f.first.ID)
	if err != nil {
		t.Fatalf("ByID() вернул ошибку: %v", err)
	}
	if course.Status != study.CourseActive {
		t.Errorf("состояние = %v, ожидался активный курс", course.Status)
	}

	saved, err := f.users.ByID(context.Background(), f.owner.ID)
	if err != nil {
		t.Fatalf("ByID() вернул ошибку: %v", err)
	}
	if saved.CurrentCourse != f.first.ID {
		t.Errorf("текущий курс = %d, ожидался возобновлённый %d", saved.CurrentCourse, f.first.ID)
	}
}

func TestDecksArchive(t *testing.T) {
	t.Parallel()

	f := newDecksFixture(t)
	f.send(t, "/decks")

	f.press(t, "ca:2")

	course, err := f.courses.ByID(context.Background(), f.second.ID)
	if err != nil {
		t.Fatalf("ByID() вернул ошибку: %v", err)
	}
	if course.Status != study.CourseArchived {
		t.Errorf("состояние = %v, ожидался архив", course.Status)
	}

	// Архивный курс виден строкой, но кнопок у него нет: он убран с глаз
	// намеренно, и случайное нажатие не должно возвращать его в оборот.
	text, buttons := f.screen(t)
	if !strings.Contains(text, "🗄 Урок 2") {
		t.Errorf("архив не отмечен: %q", text)
	}
	for _, data := range buttons {
		if strings.HasSuffix(data, ":2") {
			t.Errorf("у архивного курса осталась кнопка %q", data)
		}
	}
}

func TestDecksRejectsForeignCourse(t *testing.T) {
	t.Parallel()

	f := newDecksFixture(t)

	// Курс другого пользователя: идентификатор приезжает из кнопки,
	// а кнопку можно подделать.
	stranger := mustUser(t, 999, user.UILangRU)
	saved, _, err := f.users.Ensure(context.Background(), &stranger)
	if err != nil {
		t.Fatalf("Ensure() вернул ошибку: %v", err)
	}
	foreign, err := f.courses.Ensure(context.Background(), study.Course{
		UserID: int64(saved.ID), DeckID: 1, TranslationLang: langRU, Status: study.CourseActive,
	})
	if err != nil {
		t.Fatalf("Ensure() вернул ошибку: %v", err)
	}

	f.send(t, "/decks")
	f.press(t, "cp:"+itoa(int64(foreign.ID)))

	course, err := f.courses.ByID(context.Background(), foreign.ID)
	if err != nil {
		t.Fatalf("ByID() вернул ошибку: %v", err)
	}
	if course.Status != study.CourseActive {
		t.Errorf("чужой курс изменён: %v", course.Status)
	}

	text, _ := f.screen(t)
	if !strings.Contains(text, "уже отвечена") {
		t.Errorf("ответ на чужой курс = %q", text)
	}
}

func TestDecksEmpty(t *testing.T) {
	t.Parallel()

	f := newDecksFixture(t)
	f.courses.byID = map[study.CourseID]study.Course{}

	f.send(t, "/decks")

	text, _ := f.screen(t)
	if !strings.Contains(text, "/start") {
		t.Errorf("ответ без курсов = %q, ожидалась подсказка", text)
	}
}

func TestDecksNeedsDependencies(t *testing.T) {
	t.Parallel()

	if _, err := telegram.NewDecks(nil, nil); err == nil {
		t.Error("хендлер без зависимостей должен быть ошибкой")
	}
}

func contains(list []string, wanted string) bool {
	for _, item := range list {
		if item == wanted {
			return true
		}
	}
	return false
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}
