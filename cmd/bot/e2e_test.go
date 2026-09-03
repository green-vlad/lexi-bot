//go:build integration

// Сквозной тест: живой граф зависимостей приложения, настоящая база
// и фейковый Telegram Bot API вместо сети.
//
// Тест живёт рядом с main намеренно. Собирает граф функция router, и
// проверять имеет смысл именно её результат: тест, собравший свой граф,
// проверял бы копию проводки, которая разойдётся с настоящей при первой же
// правке — и молча.
//
// Время в тесте своё: SM-2 откладывает только что показанную карточку
// на минуту, и на настоящих часах занятие пришлось бы ждать по-настоящему.
// Часы двигает сам тест, поэтому прогон занимает секунды и повторяется
// одинаково.
package main

import (
	"bytes"
	"context"
	"log/slog"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"lexi-bot/internal/adapter/i18n"
	"lexi-bot/internal/adapter/seeds"
	storage "lexi-bot/internal/adapter/storage/postgres"
	"lexi-bot/internal/adapter/telegram"
	"lexi-bot/internal/infra/config"
	"lexi-bot/internal/infra/logger"
	"lexi-bot/internal/infra/metrics"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/internal/usecase/seeding"
	"lexi-bot/locales"
	seedfiles "lexi-bot/seeds"
	"lexi-bot/test/pgtest"
	"lexi-bot/test/tgtest"
)

// Начало отсчёта. Дата зафиксирована, чтобы прогон не зависел ни от
// сегодняшнего дня, ни от времени суток: дневные счётчики считаются
// по календарным суткам пользователя.
var epoch = time.Date(2026, time.March, 2, 9, 0, 0, 0, time.UTC)

// Кто пишет боту. Язык клиента — русский: с него начинается онбординг,
// пока человек не выбрал язык интерфейса сам.
var tester = tgtest.User{ID: 770001, Username: "tester", Language: "ru"}

// pollTimeout — время ожидания в getUpdates. Секунда сверху уходит внутрь
// библиотеки: она вычитает её из таймаута, а нулевое ожидание превратило бы
// long polling в непрерывный опрос фейкового сервера.
const pollTimeout = 2 * time.Second

// TestLearningFlow проходит путь пользователя целиком: знакомство с ботом,
// выбор курса, первые слова, повторение с ответами в обоих режимах — и
// проверяет то, ради чего всё это затевалось: что карточки разъехались
// по срокам, а в журнале осталась запись о каждом ответе.
func TestLearningFlow(t *testing.T) {
	app := start(t)

	// --- Знакомство ---------------------------------------------------

	// Язык интерфейса не спрашивают: клиент тестера говорит по-русски,
	// и он подобран сам. Спрашивают то, чего бот знать не может.
	app.command("/start")
	app.press("oblang:0:ko") // язык изучения
	app.press("obdeck:")     // первая колода
	app.press("obtr:0:ru")   // язык перевода

	if got := app.screen.Text(); !strings.Contains(got, "/learn") {
		t.Errorf("после онбординга бот не позвал на занятие: %q", got)
	}

	// --- Первые слова -------------------------------------------------

	app.command("/learn")
	app.press("new:") // «новые слова»

	learned := app.introduce()
	if learned != newPerDay {
		t.Fatalf("знакомство дало %d карточек, ожидалось %d (дневная норма)", learned, newPerDay)
	}

	cards := app.cards()
	if len(cards) != newPerDay {
		t.Fatalf("в базе %d карточек, ожидалось %d", len(cards), newPerDay)
	}
	for _, card := range cards {
		if !card.dueAt.After(app.clock.Now()) {
			t.Errorf("карточка %d сразу просрочена: due_at = %s, сейчас %s",
				card.id, card.dueAt, app.clock.Now())
		}
	}

	// --- Повторение: выбор из вариантов -------------------------------

	// Слово, отмеченное «запомнил», планировщик ставит на второй шаг
	// обучения — через десять минут. Сдвигаем часы за него: ждать эти
	// десять минут по-настоящему тест не станет.
	app.clock.advance(15 * time.Minute)

	app.command("/learn")
	app.press("rev:") // «повторить»

	// Верный ответ разбора не требует — бот сразу показывает следующее слово.
	right := app.answer(true)
	app.expectReview(right.card, "choice", true)
	app.expectMoved(right.card)

	// Промах: бот показывает правильный перевод и ждёт «дальше».
	wrong := app.answer(false)
	app.expectReview(wrong.card, "choice", false)
	app.expectMoved(wrong.card)

	// Повторное нажатие той же кнопки. Токен попытки в ней устарел, и
	// второго ответа быть не должно — ни в журнале, ни в сроке карточки.
	before := app.card(wrong.card)
	app.pressStale(wrong.data)
	if got := app.card(wrong.card); got != before {
		t.Errorf("повторное нажатие изменило карточку: было %+v, стало %+v", before, got)
	}
	if got := app.reviews(wrong.card); got != 1 {
		t.Errorf("повторное нажатие завело второй ответ в журнале: записей %d", got)
	}

	app.press("next:") // «дальше» после разбора

	// --- Повторение: ввод текстом -------------------------------------

	// Ввод текстом бот предлагает там, где ответ — слово изучаемого языка,
	// то есть в обратном направлении. Включаем его так же, как включил бы
	// пользователь: через /settings.
	app.reverseDirection()

	app.command("/learn")
	app.press("rev:")

	typed := app.typeAnswer(true)
	app.expectReview(typed, "typing", true)
	app.expectMoved(typed)

	mistyped := app.typeAnswer(false)
	app.expectReview(mistyped, "typing", false)
	app.expectMoved(mistyped)

	// --- Итог ---------------------------------------------------------

	// Каждый ответ оставил ровно одну запись, и ни один не потерялся:
	// четыре ответа — четыре строки, включая оба промаха и не считая
	// повторного нажатия.
	if got, want := app.totalReviews(), 4; got != want {
		t.Errorf("в журнале %d записей, ожидалось %d", got, want)
	}
	if got, want := app.answeredModes(), []string{"choice", "typing"}; !slices.Equal(got, want) {
		t.Errorf("в журнале режимы %v, ожидались %v", got, want)
	}

	app.api.Quiet(t)
}

// newPerDay — дневная норма новых слов из настроек по умолчанию.
const newPerDay = 5

// --- Стенд ------------------------------------------------------------

// harness — поднятое приложение и всё, что нужно, чтобы им управлять.
type harness struct {
	t     *testing.T
	api   *tgtest.Server
	pool  *pgxpool.Pool
	clock *testClock
	// registry — те же метрики, что отдаёт /metrics. Тест смотрит в них
	// по одной причине: счётчик обработанных апдейтов растёт последним,
	// уже после того, как хендлер дописал своё в базу. Дождавшись его,
	// тест знает, что бот доработал, — и не обгоняет его следующим
	// нажатием (человек за него и не успел бы).
	registry *metrics.Registry
	// sent — сколько апдейтов тест отправил боту.
	sent int

	// screen — последнее, что бот показал пользователю. Учебная сессия
	// правит одно сообщение на месте, поэтому «экран» и есть состояние
	// диалога с точки зрения человека.
	screen tgtest.Call
}

// start поднимает приложение: база со словарями, фейковый Bot API,
// граф зависимостей из router и запущенный цикл опроса.
func start(t *testing.T) *harness {
	t.Helper()

	pool := pgtest.New(t)
	api := tgtest.New(t)
	clock := &testClock{now: epoch}

	app := &harness{t: t, api: api, pool: pool, clock: clock, registry: metrics.New()}
	app.seed()

	// Уровень предупреждений: рабочий лог занятия тесту не нужен, а вот
	// сорванная обработка апдейта иначе прошла бы незамеченной — транспорт
	// пишет о ней в лог и продолжает работать.
	log := logger.New(os.Stderr, slog.LevelWarn, logger.FormatText)

	transport, err := telegram.New(telegram.Config{
		Token:       tgtest.Token,
		ServerURL:   api.URL,
		PollTimeout: pollTimeout,
		Logger:      log,
	})
	if err != nil {
		t.Fatalf("создать транспорт: %v", err)
	}

	cfg := config.Config{
		Env:             config.EnvProd,
		BotToken:        tgtest.Token,
		LogLevel:        slog.LevelWarn,
		DefaultTimezone: time.UTC,
		PollTimeout:     pollTimeout,
	}

	handler, err := router(&wiring{
		transport: transport,
		catalog:   app.catalog(),
		pool:      pool,
		cfg:       &cfg,
		log:       log,
		registry:  app.registry,
		// Чат админа нулевой — тревоги выключены, и слать их некуда.
		alerter: telegram.NewAlerter(transport, 0, clock.Now, log),
		clock:   clock,
		// Зерно постоянное: варианты ответа перемешиваются и интервалы
		// разводятся одинаково от прогона к прогону.
		rand: rand.New(rand.NewPCG(20260302, 55)), //nolint:gosec // воспроизводимость, а не секреты
	})
	if err != nil {
		t.Fatalf("собрать граф зависимостей: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := transport.Run(ctx, handler); err != nil {
			t.Errorf("цикл опроса завершился ошибкой: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	return app
}

// seed загружает встроенные словари — те же, что уезжают в прод.
func (h *harness) seed() {
	h.t.Helper()

	decks, err := seeds.Load(seedfiles.FS)
	if err != nil {
		h.t.Fatalf("разобрать встроенные словари: %v", err)
	}

	service, err := seeding.New(seeding.Deps{
		Decks:   storage.NewDeckRepo(h.pool),
		Lexemes: storage.NewLexemeRepo(h.pool),
		Tx:      storage.NewTxManager(h.pool),
	})
	if err != nil {
		h.t.Fatalf("создать сидер: %v", err)
	}
	if _, err := service.Load(context.Background(), decks); err != nil {
		h.t.Fatalf("загрузить словари: %v", err)
	}
}

func (h *harness) catalog() port.Catalog {
	h.t.Helper()

	catalog, err := i18n.NewCatalog(locales.FS)
	if err != nil {
		h.t.Fatalf("собрать каталог переводов: %v", err)
	}
	return catalog
}

// --- Управление ботом -------------------------------------------------

// command отправляет команду и запоминает показанный экран.
func (h *harness) command(text string) {
	h.t.Helper()

	h.api.Command(tester, text)
	h.settle()
	h.screen = h.api.Screen(h.t)
}

// await пролистывает экраны, пока не покажется кнопка с нужным действием.
//
// Листать приходится потому, что бот отвечает и парой сообщений подряд:
// приветствие, а следом вопрос; разбор ответа, а следом новая карточка.
// Человек в этом случае смотрит на последнее, и тест делает то же самое.
func (h *harness) await(prefix string) tgtest.Button {
	h.t.Helper()

	button, ok := h.screen.FindButton(h.t, prefix)
	for attempt := 0; !ok && attempt < maxScreens; attempt++ {
		h.screen = h.api.Screen(h.t)
		button, ok = h.screen.FindButton(h.t, prefix)
	}
	if !ok {
		h.t.Fatalf("кнопка %q не появилась; последний экран: %q", prefix, h.screen.Text())
	}
	return button
}

// press нажимает кнопку, выбирая её по началу callback_data, и запоминает
// то, что бот показал в ответ.
func (h *harness) press(prefix string) tgtest.Button {
	h.t.Helper()

	button := h.await(prefix)
	h.api.Press(tester, h.screen.MessageID(h.t), button.Data)
	h.settle()
	h.screen = h.api.Screen(h.t)
	return button
}

// maxScreens — сколько экранов подряд тест готов пролистать в поисках
// нужной кнопки. Больше двух бот подряд не присылает; запас — на случай
// пояснения перед вопросом.
const maxScreens = 3

// pressStale нажимает кнопку, ответ на которую уже принят.
//
// Экран при этом не меняется: бот отдельным сообщением объясняет, что
// кнопка устарела, а разбор предыдущего ответа остаётся на месте — на нём
// ещё нажимать «дальше». Поэтому ответ бота тест забирает и выбрасывает,
// а h.screen не трогает.
func (h *harness) pressStale(data string) {
	h.t.Helper()

	h.api.Press(tester, h.screen.MessageID(h.t), data)
	h.settle()

	var replied bool
	for _, call := range h.api.Drain(h.t) {
		replied = replied || call.Method == "sendMessage"
	}
	if !replied {
		h.t.Error("на устаревшую кнопку бот ничего не ответил")
	}
}

// send отправляет текстовое сообщение — ответ в режиме ввода.
func (h *harness) send(text string) {
	h.t.Helper()

	h.api.Text(tester, text)
	h.settle()
	h.screen = h.api.Screen(h.t)
}

// introduce проходит знакомство до конца: каждое слово отмечается как
// начатое. Возвращает число заведённых карточек.
func (h *harness) introduce() int {
	h.t.Helper()

	for count := 0; count <= newPerDay; count++ {
		if _, ok := h.screen.FindButton(h.t, "rem:"); !ok {
			// Слова кончились: бот показал итог знакомства.
			return count
		}
		h.press("rem:")
	}
	h.t.Fatalf("знакомство не закончилось после %d слов", newPerDay)
	return 0
}

// reverseDirection переключает направление вопроса через /settings —
// тем же путём, каким это делает пользователь.
func (h *harness) reverseDirection() {
	h.t.Helper()

	h.command("/settings")
	h.press("ste:0:dir") // строка «направление» в настройках
	h.press("sts:0:dir") // переключить на «перевод → слово»
}

// settle дожидается, пока бот доработает последний апдейт.
//
// Без этого тест обгоняет бота: сообщение он отправляет раньше, чем
// дописывает состояние диалога, и нажатие, сделанное в тот же миг,
// достаётся предыдущему шагу. Человек этот зазор не заметил бы —
// он в миллисекунды, — а тест попадает в него каждый раз.
func (h *harness) settle() {
	h.t.Helper()

	h.sent++
	deadline := time.Now().Add(handleTimeout)
	for time.Now().Before(deadline) {
		if h.handledUpdates() >= h.sent {
			return
		}
		time.Sleep(time.Millisecond)
	}
	h.t.Fatalf("бот не доработал апдейт №%d за %s", h.sent, handleTimeout)
}

// handleTimeout — предел ожидания обработки. Аварийный: в норме апдейт
// обрабатывается за единицы миллисекунд.
const handleTimeout = 15 * time.Second

// handledUpdates читает счётчик обработанных апдейтов из тех же метрик,
// что бот отдаёт наружу.
func (h *harness) handledUpdates() int {
	h.t.Helper()

	var buf bytes.Buffer
	if err := h.registry.Expose(&buf); err != nil {
		h.t.Fatalf("прочитать метрики: %v", err)
	}

	total := 0
	for _, line := range strings.Split(buf.String(), "\n") {
		if !strings.HasPrefix(line, "lexi_updates_total{") {
			continue
		}
		fields := strings.Fields(line)
		count, err := strconv.Atoi(fields[len(fields)-1])
		if err != nil {
			h.t.Fatalf("разобрать строку метрики %q: %v", line, err)
		}
		total += count
	}
	return total
}

// --- Ответы на карточки -----------------------------------------------

// answer отвечает на карточку в режиме выбора: correct говорит, нажимать
// верный вариант или заведомо неверный. Возвращает карточку, на которую
// ответили.
//
// Правильность известна из самой кнопки: в callback_data она уезжает
// признаком, а не текстом варианта.
func (h *harness) answer(correct bool) answered {
	h.t.Helper()

	h.await("ans:")
	for _, button := range h.screen.Buttons(h.t) {
		id, right, ok := parseAnswer(button.Data)
		if !ok || right != correct {
			continue
		}
		h.api.Press(tester, h.screen.MessageID(h.t), button.Data)
		h.settle()
		h.screen = h.api.Screen(h.t)

		return answered{card: id, data: button.Data}
	}
	h.t.Fatalf("на экране %q нет варианта с правильностью %v", h.screen.Text(), correct)
	return answered{}
}

// answered — на что ответили и чем. Данные кнопки нужны, чтобы нажать
// её второй раз и убедиться, что ответ не задвоится.
type answered struct {
	card int64
	data string
}

// typeAnswer отвечает на карточку вводом текста: correct выбирает между
// настоящим словом и заведомо неверным набором букв.
//
// Ввод текстом бот предлагает кнопкой рядом с вариантами, и путь сюда
// такой же, как у пользователя: нажать «напишу сам», прочитать вопрос,
// напечатать ответ.
func (h *harness) typeAnswer(correct bool) int64 {
	h.t.Helper()

	typing := h.await("type:")
	id, ok := callbackID(typing.Data)
	if !ok {
		h.t.Fatalf("в кнопке ввода %q нет карточки", typing.Data)
	}
	h.press("type:")

	answer := "точно не перевод"
	if correct {
		answer = h.termOf(id)
	}
	h.send(answer)
	return id
}

// termOf возвращает слово, которого бот ждёт в ответ на карточку.
//
// Правильный ответ тест берёт из базы, а не разбирает с экрана: на экране
// стоят переводы, а переводов у слова несколько, и восстанавливать по ним
// слово значило бы писать в тесте второй экземпляр той же логики.
func (h *harness) termOf(card int64) string {
	h.t.Helper()

	var term string
	err := h.pool.QueryRow(context.Background(), `
		SELECT l.term
		FROM cards c
		JOIN lexemes l ON l.id = c.lexeme_id
		WHERE c.id = $1`, card).Scan(&term)
	if err != nil {
		h.t.Fatalf("узнать слово карточки %d: %v", card, err)
	}
	return term
}

// parseAnswer разбирает кнопку варианта: «ans:<карточка>:<верно>:...».
func parseAnswer(data string) (card int64, correct, ok bool) {
	parts := strings.Split(data, ":")
	if len(parts) < 3 || parts[0] != "ans" {
		return 0, false, false
	}
	id, ok := parseInt(parts[1])
	if !ok {
		return 0, false, false
	}
	return id, parts[2] == "1", true
}

// callbackID достаёт идентификатор из callback_data вида «действие:id:...».
func callbackID(data string) (int64, bool) {
	parts := strings.Split(data, ":")
	if len(parts) < 2 {
		return 0, false
	}
	return parseInt(parts[1])
}

func parseInt(s string) (int64, bool) {
	value, err := strconv.ParseInt(s, 10, 64)
	return value, err == nil
}

// --- Проверки по базе -------------------------------------------------

// cardState — то, что тест знает о карточке: срок и состояние.
type cardState struct {
	id           int64
	state        string
	dueAt        time.Time
	lastReviewed time.Time
	interval     float64
}

// cards возвращает карточки пользователя в порядке заведения.
func (h *harness) cards() []cardState {
	h.t.Helper()

	rows, err := h.pool.Query(context.Background(), `
		SELECT c.id, c.state, c.due_at, coalesce(c.last_reviewed_at, 'epoch'), c.interval_days
		FROM cards c
		ORDER BY c.id`)
	if err != nil {
		h.t.Fatalf("прочитать карточки: %v", err)
	}
	defer rows.Close()

	var out []cardState
	for rows.Next() {
		var card cardState
		if err := rows.Scan(&card.id, &card.state, &card.dueAt, &card.lastReviewed, &card.interval); err != nil {
			h.t.Fatalf("разобрать карточку: %v", err)
		}
		out = append(out, card)
	}
	if err := rows.Err(); err != nil {
		h.t.Fatalf("обойти карточки: %v", err)
	}
	return out
}

// card возвращает состояние одной карточки.
func (h *harness) card(id int64) cardState {
	h.t.Helper()

	for _, card := range h.cards() {
		if card.id == id {
			return card
		}
	}
	h.t.Fatalf("карточки %d нет в базе", id)
	return cardState{}
}

// expectMoved проверяет, что ответ сдвинул срок карточки вперёд и отметил
// момент повторения.
//
// Это и есть смысл ответа, всё остальное — оформление. Проверка «срок
// в будущем» сильнее, чем кажется: карточку только что выдали как
// просроченную, то есть её срок был позади, — значит, он переехал.
func (h *harness) expectMoved(id int64) {
	h.t.Helper()

	card := h.card(id)
	now := h.clock.Now()

	if !card.dueAt.After(now) {
		h.t.Errorf("карточка %d после ответа не уехала вперёд: due_at = %s, сейчас %s",
			id, card.dueAt, now)
	}
	if !card.lastReviewed.Equal(now) {
		h.t.Errorf("карточка %d: last_reviewed_at = %s, ожидалось %s",
			id, card.lastReviewed, now)
	}
}

// expectReview проверяет запись в журнале повторений: она должна быть
// ровно одна, в нужном режиме и с нужным исходом.
func (h *harness) expectReview(card int64, mode string, correct bool) {
	h.t.Helper()

	var (
		gotMode    string
		gotCorrect bool
		rating     string
		ratedAt    time.Time
		count      int
	)
	err := h.pool.QueryRow(context.Background(), `
		SELECT count(*) OVER (), mode, is_correct, rating, rated_at
		FROM reviews
		WHERE card_id = $1
		ORDER BY id DESC
		LIMIT 1`, card).Scan(&count, &gotMode, &gotCorrect, &rating, &ratedAt)
	if err != nil {
		h.t.Fatalf("прочитать журнал по карточке %d: %v", card, err)
	}

	if count != 1 {
		h.t.Errorf("по карточке %d записей в журнале %d, ожидалась одна", card, count)
	}
	if gotMode != mode {
		h.t.Errorf("карточка %d: режим в журнале %q, ожидался %q", card, gotMode, mode)
	}
	if gotCorrect != correct {
		h.t.Errorf("карточка %d: is_correct = %v, ожидалось %v", card, gotCorrect, correct)
	}
	if correct && rating == "again" {
		h.t.Errorf("карточка %d: верный ответ оценён как %q", card, rating)
	}
	if !correct && rating != "again" {
		h.t.Errorf("карточка %d: промах оценён как %q, ожидалось again", card, rating)
	}
	if !ratedAt.Equal(h.clock.Now()) {
		h.t.Errorf("карточка %d: rated_at = %s, ожидалось %s", card, ratedAt, h.clock.Now())
	}
}

// reviews считает записи журнала по карточке.
func (h *harness) reviews(card int64) int {
	h.t.Helper()

	var count int
	if err := h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM reviews WHERE card_id = $1`, card).Scan(&count); err != nil {
		h.t.Fatalf("посчитать журнал по карточке %d: %v", card, err)
	}
	return count
}

// totalReviews считает все записи журнала.
func (h *harness) totalReviews() int {
	h.t.Helper()

	var count int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM reviews`).Scan(&count); err != nil {
		h.t.Fatalf("посчитать журнал: %v", err)
	}
	return count
}

// answeredModes возвращает режимы, в которых были ответы.
func (h *harness) answeredModes() []string {
	h.t.Helper()

	rows, err := h.pool.Query(context.Background(),
		`SELECT DISTINCT mode FROM reviews ORDER BY mode`)
	if err != nil {
		h.t.Fatalf("прочитать режимы из журнала: %v", err)
	}
	defer rows.Close()

	var modes []string
	for rows.Next() {
		var mode string
		if err := rows.Scan(&mode); err != nil {
			h.t.Fatalf("разобрать режим: %v", err)
		}
		modes = append(modes, mode)
	}
	return modes
}

// --- Часы -------------------------------------------------------------

// testClock — управляемые часы. Приложение видит их как port.Clock и не
// знает, что время двигает тест.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

// Now возвращает текущее время стенда.
func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// advance двигает время вперёд.
func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
