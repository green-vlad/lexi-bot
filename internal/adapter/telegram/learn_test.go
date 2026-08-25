package telegram_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"lexi-bot/internal/adapter/telegram"
	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/courses"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/internal/usecase/session"
)

// learnFixture — бот с готовым курсом и парой слов в колоде.
type learnFixture struct {
	router    *telegram.Router
	messenger *editingMessenger
	decks     *stubDeckSource
	reviews   *stubReviews
	rand      *stubRand
	sessions  *fakeSessions
	lexemes   *stubLexemes
	cards     *stubCards
	courses   *stubCourses
	settings  *stubSettings
	now       time.Time
}

func newLearnFixture(t *testing.T, words int, modes ...study.Mode) *learnFixture {
	t.Helper()

	f := &learnFixture{
		messenger: &editingMessenger{},
		courses:   newStubCourses(),
		settings:  newStubSettings(),
		now:       time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC),
	}

	settings := user.DefaultSettings(user.UTCTimezone())
	if len(modes) > 0 {
		updated, err := settings.WithQuizModes(modes)
		if err != nil {
			t.Fatalf("WithQuizModes() вернул ошибку: %v", err)
		}
		settings = updated
	}
	f.settings.byUser[1] = settings

	course, err := f.courses.Ensure(context.Background(), study.Course{
		UserID: 1, DeckID: 1, TranslationLang: langRU, Status: study.CourseActive,
	})
	if err != nil {
		t.Fatalf("Ensure() вернул ошибку: %v", err)
	}

	lexemes := map[lexicon.LexemeID]lexicon.Lexeme{}
	translations := map[lexicon.LexemeID][]lexicon.Translation{}
	pool := make([]lexicon.LexemeID, 0, words)
	terms := []string{"집", "개", "물", "불"}
	meanings := []string{"дом", "собака", "вода", "огонь"}
	for i := 0; i < words; i++ {
		id := lexicon.LexemeID(i + 1)
		pool = append(pool, id)
		lexemes[id] = lexicon.Lexeme{ID: id, Lang: langKO, Term: terms[i], Reading: "чтение", POS: lexicon.POSNoun}
		translations[id] = []lexicon.Translation{{LexemeID: id, Lang: langRU, Text: meanings[i], IsPrimary: true}}
	}
	f.cards = newStubCards(pool, course.ID)
	f.lexemes = &stubLexemes{lexemes: lexemes, translations: translations}
	f.decks = &stubDeckSource{translations: translations}
	f.reviews = &stubReviews{}
	// Порядок вариантов фиксирован: тест должен знать, где правильный.
	f.rand = &stubRand{}

	users := newFakeUsers()
	// Пользователь должен существовать до занятия: курс ищется по нему.
	owner := mustUser(t, 555, user.UILangRU)
	if _, _, err := users.Ensure(context.Background(), &owner); err != nil {
		t.Fatalf("Ensure() вернул ошибку: %v", err)
	}

	scheduler, err := study.NewSM2(study.DefaultSM2Config(), nil)
	if err != nil {
		t.Fatalf("NewSM2() вернул ошибку: %v", err)
	}

	clock := port.ClockFunc(func() time.Time { return f.now })
	service, err := session.New(&session.Deps{
		Cards:     f.cards,
		Decks:     f.decks,
		Reviews:   f.reviews,
		Rand:      f.rand,
		Counters:  f.cards.counters,
		Courses:   f.courses,
		Settings:  f.settings,
		Lexemes:   f.lexemes,
		Clock:     clock,
		Scheduler: scheduler,
		Resolver:  study.DefaultRatingResolver(),
	})
	if err != nil {
		t.Fatalf("session.New() вернул ошибку: %v", err)
	}

	f.sessions = newFakeSessions()
	dialogs, err := telegram.NewDialogs(&telegram.DialogsConfig{
		Sessions: f.sessions, Messenger: f.messenger, Clock: clock, Logger: quietLogger(),
	})
	if err != nil {
		t.Fatalf("NewDialogs() вернул ошибку: %v", err)
	}

	courseService, err := courses.New(courses.Deps{
		Users:   users,
		Courses: f.courses,
		Decks:   f.decks,
		Cards:   f.cards,
	})
	if err != nil {
		t.Fatalf("courses.New() вернул ошибку: %v", err)
	}

	handler, err := telegram.NewLearn(service, courseService, f.messenger, testCatalog(t), clock, dialogs)
	if err != nil {
		t.Fatalf("NewLearn() вернул ошибку: %v", err)
	}

	f.router = telegram.NewRouter()
	f.router.Use(
		telegram.AnswerCallbacks(f.messenger, quietLogger()),
		telegram.Identify(users, quietLogger()),
		telegram.Localize(testCatalog(t)),
		dialogs.Middleware(),
	)
	handler.Register(f.router)
	// Посторонняя команда, на которой проверяется прерывание ожидания ввода.
	f.router.Command("help", telegram.Reply(f.messenger, "help.text"))
	return f
}

// reverse переключает курс на проверку в сторону изучаемого языка:
// только там разрешён ввод текстом.
func (f *learnFixture) reverse(t *testing.T) {
	t.Helper()

	settings := f.settings.byUser[1]
	settings.ReverseDirection = true
	f.settings.byUser[1] = settings
}

func (f *learnFixture) send(t *testing.T, text string) {
	t.Helper()

	if err := f.router.Handle(context.Background(), message(text)); err != nil {
		t.Fatalf("Handle(%q) вернул ошибку: %v", text, err)
	}
}

func (f *learnFixture) press(t *testing.T, data string) {
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

// screen возвращает последнее показанное пользователю — неважно, новым
// сообщением или правкой прежнего.
func (f *learnFixture) screen(t *testing.T) (text string, buttons []string) {
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

func TestLearnShowsCardAndAcceptsRating(t *testing.T) {
	t.Parallel()

	f := newLearnFixture(t, 2, study.ModeRecall)

	f.send(t, "/learn")

	text, buttons := f.screen(t)
	if !strings.Contains(text, "집") {
		t.Errorf("на карточке %q, ожидалось слово", text)
	}
	if !strings.Contains(text, "чтение") {
		t.Errorf("на карточке %q, ожидалось чтение слова", text)
	}
	if len(buttons) != 1 || !strings.HasPrefix(buttons[0], "show:") {
		t.Fatalf("кнопки = %v, ожидалась одна кнопка показа перевода", buttons)
	}

	// Перевод открывается по кнопке, а не сразу: в этом весь смысл режима.
	f.press(t, buttons[0])
	text, buttons = f.screen(t)
	if !strings.Contains(text, "дом") {
		t.Errorf("после показа %q, ожидался перевод", text)
	}
	if len(buttons) != len(study.Ratings()) {
		t.Fatalf("кнопок оценки %d, ожидалось четыре: %v", len(buttons), buttons)
	}

	// Оценка двигает карточку и показывает следующее слово.
	var good string
	for _, data := range buttons {
		if strings.Contains(data, ":good:") {
			good = data
		}
	}
	if good == "" {
		t.Fatalf("среди кнопок нет оценки good: %v", buttons)
	}

	f.press(t, good)
	text, buttons = f.screen(t)
	if !strings.Contains(text, "개") {
		t.Errorf("после ответа %q, ожидалось следующее слово", text)
	}
	if len(buttons) != 1 {
		t.Errorf("кнопки следующей карточки = %v", buttons)
	}

	// Карточка действительно уехала: состояние изменилось.
	card, err := f.cards.ByID(context.Background(), 1)
	if err != nil {
		t.Fatalf("ByID() вернул ошибку: %v", err)
	}
	if card.State == study.StateNew || card.IsNew() {
		t.Errorf("карточка осталась новой: %+v", card.CardState)
	}
}

func TestLearnFinishesWhenNothingLeft(t *testing.T) {
	t.Parallel()

	f := newLearnFixture(t, 1, study.ModeRecall)

	f.send(t, "/learn")
	_, buttons := f.screen(t)
	f.press(t, buttons[0])

	_, buttons = f.screen(t)
	for _, data := range buttons {
		if strings.Contains(data, ":good:") {
			f.press(t, data)
		}
	}

	// Слов больше нет: занятие закончилось, кнопок не осталось.
	text, buttons := f.screen(t)
	if len(buttons) != 0 {
		t.Errorf("после конца занятия остались кнопки: %v", buttons)
	}
	if !strings.Contains(strings.ToLower(text), "сегодня") {
		t.Errorf("сообщение о конце = %q", text)
	}
}

func TestLearnWithoutCourse(t *testing.T) {
	t.Parallel()

	f := newLearnFixture(t, 2, study.ModeRecall)
	f.courses.byID = map[study.CourseID]study.Course{}

	f.send(t, "/learn")

	text, _ := f.screen(t)
	if !strings.Contains(text, "/start") {
		t.Errorf("ответ без курса = %q, ожидалась подсказка про /start", text)
	}
}

func TestLearnSkipsPausedCourse(t *testing.T) {
	t.Parallel()

	f := newLearnFixture(t, 2, study.ModeRecall)
	for id, course := range f.courses.byID {
		course.Status = study.CoursePaused
		f.courses.byID[id] = course
	}

	f.send(t, "/learn")

	text, _ := f.screen(t)
	if !strings.Contains(text, "/start") {
		t.Errorf("ответ = %q: активных курсов нет", text)
	}
}

func TestLearnIgnoresStaleButtons(t *testing.T) {
	t.Parallel()

	f := newLearnFixture(t, 2, study.ModeRecall)

	f.send(t, "/learn")
	_, buttons := f.screen(t)
	show := buttons[0]

	f.press(t, show)
	_, rating := f.screen(t)

	var good string
	for _, data := range rating {
		if strings.Contains(data, ":good:") {
			good = data
		}
	}
	f.press(t, good)

	// Второе нажатие той же кнопки оценки: карточка уже уехала, и токен
	// в кнопке устарел.
	f.press(t, good)
	text, _ := f.screen(t)
	if !strings.Contains(text, "уже отвечена") {
		t.Errorf("ответ на устаревшую кнопку = %q", text)
	}

	// Как и кнопка показа перевода от той же карточки.
	f.press(t, show)
	text, _ = f.screen(t)
	if !strings.Contains(text, "уже отвечена") {
		t.Errorf("ответ на устаревшую кнопку показа = %q", text)
	}
}

func TestLearnNeedsDependencies(t *testing.T) {
	t.Parallel()

	if _, err := telegram.NewLearn(nil, nil, nil, nil, nil, nil); err == nil {
		t.Error("хендлер без зависимостей должен быть ошибкой")
	}
}

func TestLearnChoiceMode(t *testing.T) {
	t.Parallel()

	f := newLearnFixture(t, 4, study.ModeChoice)

	f.send(t, "/learn")

	text, buttons := f.screen(t)
	if !strings.Contains(text, "집") {
		t.Errorf("на карточке %q, ожидалось слово", text)
	}
	if len(buttons) != 4 {
		t.Fatalf("вариантов %d, ожидалось четыре: %v", len(buttons), buttons)
	}

	// Правильный вариант ровно один.
	correct := 0
	for _, data := range buttons {
		if strings.HasPrefix(data, "ans:1:1:") {
			correct++
		}
	}
	if correct != 1 {
		t.Fatalf("правильных вариантов %d, ожидался один: %v", correct, buttons)
	}

	// Верный ответ ведёт сразу к следующему слову — разбирать нечего.
	for _, data := range buttons {
		if strings.HasPrefix(data, "ans:1:1:") {
			f.press(t, data)
		}
	}
	text, _ = f.screen(t)
	if !strings.Contains(text, "개") {
		t.Errorf("после верного ответа %q, ожидалось следующее слово", text)
	}
}

func TestLearnChoiceExplainsMiss(t *testing.T) {
	t.Parallel()

	f := newLearnFixture(t, 4, study.ModeChoice)

	f.send(t, "/learn")
	_, buttons := f.screen(t)

	// Промах: показываем верный перевод и ждём нажатия «дальше».
	// Проскочить мимо правильного ответа человек не должен.
	for _, data := range buttons {
		if strings.HasPrefix(data, "ans:1:0:") {
			f.press(t, data)
			break
		}
	}

	text, next := f.screen(t)
	if !strings.Contains(text, "дом") {
		t.Errorf("разбор = %q, ожидался правильный перевод", text)
	}
	if len(next) != 1 || !strings.HasPrefix(next[0], "next:") {
		t.Fatalf("кнопки разбора = %v, ожидалась одна кнопка «дальше»", next)
	}

	f.press(t, next[0])
	text, _ = f.screen(t)
	if !strings.Contains(text, "개") {
		t.Errorf("после «дальше» %q, ожидалось следующее слово", text)
	}

	// Промах записан провалом: карточка вернётся скоро.
	card, err := f.cards.ByID(context.Background(), 1)
	if err != nil {
		t.Fatalf("ByID() вернул ошибку: %v", err)
	}
	if card.State != study.StateLearning || card.LearnStep != 0 {
		t.Errorf("состояние после промаха = %+v", card.CardState)
	}
}

func TestLearnChoiceFallsBackWhenDeckIsTiny(t *testing.T) {
	t.Parallel()

	// В колоде одно слово: ложных вариантов взять негде, и выбор из одной
	// кнопки был бы издевательством. Спрашиваем узнаванием.
	f := newLearnFixture(t, 1, study.ModeChoice)

	f.send(t, "/learn")

	_, buttons := f.screen(t)
	if len(buttons) != 1 || !strings.HasPrefix(buttons[0], "show:") {
		t.Fatalf("кнопки = %v, ожидался переход к показу перевода", buttons)
	}
}

func TestLearnChoiceCountsSpeed(t *testing.T) {
	t.Parallel()

	f := newLearnFixture(t, 4, study.ModeChoice)

	f.send(t, "/learn")
	_, buttons := f.screen(t)

	var correct string
	for _, data := range buttons {
		if strings.HasPrefix(data, "ans:1:1:") {
			correct = data
		}
	}

	// Человек думал долго: ответ верный, но не «лёгкий».
	f.now = f.now.Add(30 * time.Second)
	f.press(t, correct)

	card, err := f.cards.ByID(context.Background(), 1)
	if err != nil {
		t.Fatalf("ByID() вернул ошибку: %v", err)
	}
	// «Легко» у новой карточки отправило бы её сразу на четыре дня;
	// «хорошо» оставляет на шагах обучения.
	if card.State != study.StateLearning {
		t.Errorf("состояние = %v, ожидалось обучение: ответ не был быстрым", card.State)
	}
}

func TestLearnTypingCorrectAnswer(t *testing.T) {
	t.Parallel()

	f := newLearnFixture(t, 3, study.ModeTyping)
	f.reverse(t)

	f.send(t, "/learn")
	text, buttons := f.screen(t)
	if len(buttons) != 0 {
		t.Errorf("в режиме ввода кнопок быть не должно: %v", buttons)
	}
	// Спрашиваем в сторону изучаемого языка: показан перевод, ждём слово.
	if !strings.Contains(text, "дом") {
		t.Errorf("на карточке %q, ожидался перевод", text)
	}

	// Бот перешёл в ожидание ответа: без этого он не отличил бы перевод
	// от случайного сообщения.
	if dialog, ok := f.sessions.current(t); !ok || dialog.State != "learn:typing" {
		t.Fatalf("состояние диалога = %+v, ожидалось ожидание ввода", dialog)
	}

	f.send(t, "집")

	// Разбор показан правкой карточки, а следующее слово — новым сообщением.
	if got := f.messenger.edits[len(f.messenger.edits)-1].Text; !strings.Contains(got, "Верно") {
		t.Errorf("разбор = %q, ожидалось подтверждение", got)
	}
	text, _ = f.screen(t)
	if !strings.Contains(text, "собака") {
		t.Errorf("после верного ответа %q, ожидался следующий перевод", text)
	}

	card, err := f.cards.ByID(context.Background(), 1)
	if err != nil {
		t.Fatalf("ByID() вернул ошибку: %v", err)
	}
	if card.IsNew() {
		t.Error("карточка не была отвечена")
	}
}

func TestLearnTypingTypoShowsSpelling(t *testing.T) {
	t.Parallel()

	f := newLearnFixture(t, 3, study.ModeTyping)
	f.reverse(t)

	// Опечатки прощаются словам от четырёх символов (T-010), а корейские
	// слова часто короче: берём подлиннее, иначе проверять нечего.
	long := f.lexemes.lexemes[1]
	long.Term = "초등학생"
	f.lexemes.lexemes[1] = long

	f.send(t, "/learn")

	// Опечатка в один символ засчитывается, но человек должен увидеть,
	// как пишется правильно, — иначе он выучит свою опечатку.
	f.send(t, "초등학샹")

	text, buttons := f.screen(t)
	if !strings.Contains(text, "Почти") {
		t.Errorf("разбор = %q, ожидалось «почти»", text)
	}
	if !strings.Contains(text, "초등학생") || !strings.Contains(text, "초등학샹") {
		t.Errorf("разбор = %q, ожидались оба написания", text)
	}
	// После опечатки сессия ждёт нажатия «дальше»: проскакивать мимо
	// правильного написания не нужно.
	if len(buttons) != 1 || !strings.HasPrefix(buttons[0], "next:") {
		t.Fatalf("кнопки = %v, ожидалась одна кнопка «дальше»", buttons)
	}

	f.press(t, buttons[0])
	text, _ = f.screen(t)
	if !strings.Contains(text, "собака") {
		t.Errorf("после «дальше» %q, ожидался следующий перевод", text)
	}
}

func TestLearnTypingWrongAnswer(t *testing.T) {
	t.Parallel()

	f := newLearnFixture(t, 3, study.ModeTyping)
	f.reverse(t)

	f.send(t, "/learn")
	f.send(t, "совсем не то")

	text, buttons := f.screen(t)
	if !strings.Contains(text, "집") {
		t.Errorf("разбор = %q, ожидалось правильное слово", text)
	}
	if !strings.Contains(text, "совсем не то") {
		t.Errorf("разбор = %q, ожидался введённый ответ", text)
	}
	if len(buttons) != 1 {
		t.Fatalf("кнопки = %v", buttons)
	}

	card, err := f.cards.ByID(context.Background(), 1)
	if err != nil {
		t.Fatalf("ByID() вернул ошибку: %v", err)
	}
	if card.LearnStep != 0 || card.State != study.StateLearning {
		t.Errorf("состояние после промаха = %+v", card.CardState)
	}
}

func TestLearnTypingIgnoresCommands(t *testing.T) {
	t.Parallel()

	f := newLearnFixture(t, 3, study.ModeTyping)
	f.reverse(t)

	f.send(t, "/learn")
	if _, ok := f.sessions.current(t); !ok {
		t.Fatal("ожидание ответа не началось")
	}

	// Посторонняя команда во время ожидания ответом не считается: она
	// прерывает ожидание и выполняется сама (PLAN §5).
	f.send(t, "/help")

	if _, ok := f.sessions.current(t); ok {
		t.Error("команда должна прерывать ожидание ответа")
	}

	card, err := f.cards.ByID(context.Background(), 1)
	if err != nil {
		t.Fatalf("ByID() вернул ошибку: %v", err)
	}
	if !card.IsNew() {
		t.Error("команда не должна засчитываться ответом на карточку")
	}
}

func TestLearnTypingWaitsForRealAnswer(t *testing.T) {
	t.Parallel()

	f := newLearnFixture(t, 3, study.ModeTyping)
	f.reverse(t)

	f.send(t, "/learn")
	f.send(t, "   ")

	// Пустое сообщение ответом не считается: продолжаем ждать.
	dialog, ok := f.sessions.current(t)
	if !ok || dialog.State != "learn:typing" {
		t.Errorf("состояние = %+v, ожидалось прежнее ожидание", dialog)
	}

	card, err := f.cards.ByID(context.Background(), 1)
	if err != nil {
		t.Fatalf("ByID() вернул ошибку: %v", err)
	}
	if !card.IsNew() {
		t.Error("пустое сообщение засчиталось ответом")
	}
}

func TestLearnShowsSummary(t *testing.T) {
	t.Parallel()

	f := newLearnFixture(t, 2, study.ModeRecall)
	f.reviews.total, f.reviews.correct = 2, 1

	// Проходим обе карточки: одну верно, одну нет.
	for i, rating := range []string{"good", "again"} {
		f.send(t, "/learn")
		_, buttons := f.screen(t)
		if len(buttons) == 0 {
			t.Fatalf("карточка %d: кнопок нет", i+1)
		}
		f.press(t, buttons[0])

		_, ratings := f.screen(t)
		for _, data := range ratings {
			if strings.Contains(data, ":"+rating+":") {
				f.press(t, data)
			}
		}
	}

	// Слова кончились — показан итог.
	text, buttons := f.screen(t)
	if len(buttons) != 0 {
		t.Errorf("в итоге остались кнопки: %v", buttons)
	}
	if !strings.Contains(text, "2 карточки") {
		t.Errorf("итог = %q, ожидалось число повторённых", text)
	}
	if !strings.Contains(text, "2 новых") {
		t.Errorf("итог = %q, ожидалось число новых слов", text)
	}
	if !strings.Contains(text, "50%") {
		t.Errorf("итог = %q, ожидалась точность 50%%", text)
	}
	if !strings.Contains(text, "Следующее повторение") {
		t.Errorf("итог = %q, ожидался срок следующего повторения", text)
	}
}

func TestLearnSummaryWithoutAnswers(t *testing.T) {
	t.Parallel()

	f := newLearnFixture(t, 1, study.ModeRecall)

	// Отвечаем на единственную карточку, чтобы дневной лимит кончился
	// не сразу, а колода — да.
	f.send(t, "/learn")
	_, buttons := f.screen(t)
	f.press(t, buttons[0])
	_, ratings := f.screen(t)
	for _, data := range ratings {
		if strings.Contains(data, ":good:") {
			f.press(t, data)
		}
	}

	text, _ := f.screen(t)
	if !strings.Contains(text, "1 карточка") {
		t.Errorf("итог = %q, ожидалась одна карточка", text)
	}
	// Точность из журнала не пришла — фейк молчит, — но и «0%» в итоге
	// быть не должно: строка про точность просто опускается.
	if strings.Contains(text, "Точность: 0%") {
		t.Errorf("итог = %q: нулевая точность при верном ответе вводит в заблуждение", text)
	}
}
