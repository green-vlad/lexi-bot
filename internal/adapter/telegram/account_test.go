package telegram_test

import (
	"context"
	"strings"
	"testing"

	"lexi-bot/internal/adapter/telegram"
	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/account"
	"lexi-bot/internal/usecase/courses"
	"lexi-bot/internal/usecase/port"
)

type accountFixture struct {
	router    *telegram.Router
	messenger *editingMessenger
	courses   *stubCourses
	users     *fakeUsers
	owner     user.ID
}

func newAccountFixture(t *testing.T, active, paused int) *accountFixture {
	t.Helper()

	f := &accountFixture{
		messenger: &editingMessenger{},
		courses:   newStubCourses(),
		users:     newFakeUsers(),
	}

	owner := mustUser(t, 555, user.UILangRU)
	saved, _, err := f.users.Ensure(context.Background(), &owner)
	if err != nil {
		t.Fatalf("Ensure() вернул ошибку: %v", err)
	}
	f.owner = saved.ID

	// Курс — это пара «колода и язык перевода», и заводить несколько
	// на одной колоде нельзя: репозиторий вернёт прежний.
	add := func(status study.CourseStatus, deck lexicon.DeckID) {
		if _, err := f.courses.Ensure(context.Background(), study.Course{
			UserID: int64(saved.ID), DeckID: deck, TranslationLang: langRU, Status: status,
		}); err != nil {
			t.Fatalf("Ensure() вернул ошибку: %v", err)
		}
	}
	for i := 0; i < active; i++ {
		add(study.CourseActive, lexicon.DeckID(i+1))
	}
	for i := 0; i < paused; i++ {
		add(study.CoursePaused, lexicon.DeckID(active+i+1))
	}

	courseService, err := courses.New(courses.Deps{
		Users: f.users, Courses: f.courses, Decks: newStubDecks(), Cards: newStubCards(nil, 1),
	})
	if err != nil {
		t.Fatalf("courses.New() вернул ошибку: %v", err)
	}

	accountService, err := account.New(account.Deps{Users: f.users})
	if err != nil {
		t.Fatalf("account.New() вернул ошибку: %v", err)
	}

	handler, err := telegram.NewAccount(accountService, courseService, f.messenger)
	if err != nil {
		t.Fatalf("NewAccount() вернул ошибку: %v", err)
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

func (f *accountFixture) send(t *testing.T, text string) {
	t.Helper()

	if err := f.router.Handle(context.Background(), message(text)); err != nil {
		t.Fatalf("Handle(%q) вернул ошибку: %v", text, err)
	}
}

func (f *accountFixture) press(t *testing.T, data string) {
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

func (f *accountFixture) screen(t *testing.T) (text string, buttons []string) {
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

// statuses считает курсы по состояниям.
func (f *accountFixture) statuses() map[study.CourseStatus]int {
	out := map[study.CourseStatus]int{}
	for _, course := range f.courses.byID {
		out[course.Status]++
	}
	return out
}

func TestPauseStopsEveryActiveCourse(t *testing.T) {
	t.Parallel()

	f := newAccountFixture(t, 2, 0)

	f.send(t, "/pause")

	text, _ := f.screen(t)
	if !strings.Contains(text, "остановлено 2 курса") {
		t.Errorf("ответ = %q", text)
	}
	if got := f.statuses(); got[study.CoursePaused] != 2 || got[study.CourseActive] != 0 {
		t.Errorf("состояния = %+v", got)
	}

	// Второй раз останавливать нечего — и об этом говорится прямо,
	// а не рапортом «готово» о несделанном.
	f.send(t, "/pause")
	if text, _ = f.screen(t); !strings.Contains(text, "нечего") {
		t.Errorf("повторный ответ = %q", text)
	}
}

func TestPauseLeavesArchivedAlone(t *testing.T) {
	t.Parallel()

	f := newAccountFixture(t, 1, 0)
	// Архивный курс убран насовсем: ни пауза, ни возврат его не трогают.
	if _, err := f.courses.Ensure(context.Background(), study.Course{
		UserID: int64(f.owner), DeckID: 99, TranslationLang: langRU, Status: study.CourseArchived,
	}); err != nil {
		t.Fatalf("Ensure() вернул ошибку: %v", err)
	}

	f.send(t, "/pause")
	f.send(t, "/resume")

	if got := f.statuses(); got[study.CourseArchived] != 1 {
		t.Errorf("состояния = %+v, архивный курс тронут", got)
	}
}

func TestResumeBringsCoursesBack(t *testing.T) {
	t.Parallel()

	f := newAccountFixture(t, 0, 2)

	f.send(t, "/resume")

	text, _ := f.screen(t)
	if !strings.Contains(text, "Возвращено 2 курса") {
		t.Errorf("ответ = %q", text)
	}
	if got := f.statuses(); got[study.CourseActive] != 2 {
		t.Errorf("состояния = %+v", got)
	}

	f.send(t, "/resume")
	if text, _ = f.screen(t); !strings.Contains(text, "На паузе ничего нет") {
		t.Errorf("повторный ответ = %q", text)
	}
}

func TestDeleteAsksBeforeDoing(t *testing.T) {
	t.Parallel()

	f := newAccountFixture(t, 1, 0)

	f.send(t, "/delete_me")

	text, buttons := f.screen(t)
	// Удаление необратимо, и одной команды для него мало.
	if !strings.Contains(text, "необратимо") {
		t.Errorf("вопрос = %q", text)
	}
	if len(buttons) != 2 {
		t.Fatalf("кнопки = %v, ожидались две", buttons)
	}
	// Данные ещё на месте.
	if _, err := f.users.ByID(context.Background(), f.owner); err != nil {
		t.Error("данные удалены до подтверждения")
	}
}

func TestDeleteCancelKeepsEverything(t *testing.T) {
	t.Parallel()

	f := newAccountFixture(t, 1, 0)

	f.send(t, "/delete_me")
	_, buttons := f.screen(t)

	var keep string
	for _, data := range buttons {
		if strings.HasPrefix(data, "keepme") {
			keep = data
		}
	}
	if keep == "" {
		t.Fatalf("нет кнопки отказа: %v", buttons)
	}

	f.press(t, keep)

	text, _ := f.screen(t)
	if !strings.Contains(text, "Ничего не тронуто") {
		t.Errorf("ответ = %q", text)
	}
	if _, err := f.users.ByID(context.Background(), f.owner); err != nil {
		t.Error("отказ от удаления всё равно удалил данные")
	}
}

func TestDeleteConfirmRemovesUser(t *testing.T) {
	t.Parallel()

	f := newAccountFixture(t, 1, 0)

	f.send(t, "/delete_me")
	_, buttons := f.screen(t)

	var confirm string
	for _, data := range buttons {
		if strings.HasPrefix(data, "delme") {
			confirm = data
		}
	}
	if confirm == "" {
		t.Fatalf("нет кнопки подтверждения: %v", buttons)
	}

	f.press(t, confirm)

	if _, err := f.users.ByID(context.Background(), f.owner); err == nil {
		t.Error("пользователь не удалён")
	}
	text, moreButtons := f.screen(t)
	if !strings.Contains(text, "всё удалено") {
		t.Errorf("ответ = %q", text)
	}
	// Кнопки убраны: «да, удалить» под уже удалённой записью —
	// приглашение нажать её второй раз.
	if len(moreButtons) != 0 {
		t.Errorf("после удаления остались кнопки: %v", moreButtons)
	}
}

func TestAccountNeedsDependencies(t *testing.T) {
	t.Parallel()

	if _, err := telegram.NewAccount(nil, nil, nil); err == nil {
		t.Error("хендлер без зависимостей должен быть ошибкой")
	}
	if _, err := account.New(account.Deps{}); err == nil {
		t.Error("сценарий без зависимостей должен быть ошибкой")
	}
}
