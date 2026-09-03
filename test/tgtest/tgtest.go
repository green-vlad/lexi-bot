//go:build integration

// Package tgtest поднимает фейковый Telegram Bot API на httptest.
//
// Он говорит по тому же протоколу, что и настоящий: multipart-запрос
// на /bot<токен>/<метод>, ответ вида {"ok":true,"result":...}. Поэтому
// приложение в тесте собирается ровно так же, как в проде, — подменяется
// один только адрес сервера, и весь путь от getUpdates до sendMessage
// остаётся настоящим.
//
// Сервер двусторонний: тест кладёт апдейты в очередь (Command, Text, Press)
// и разбирает исходящие вызовы (Next). Живой сети при этом нет, и ждать
// нечего — апдейт забирается ближайшим getUpdates.
//
// Пакет собирается только с тегом integration: обычному go test он не нужен.
package tgtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Token — токен фейкового бота. Настоящий формат — «цифры:буквы»; проверять
// его сервер не станет, но пусть выглядит как настоящий.
const Token = "424242:TEST-BOT-TOKEN" //nolint:gosec // фейковый токен фейкового бота

// maxRequestBytes — предел размера запроса к фейковому Bot API. Мегабайта
// хватает с запасом: самое крупное, что шлёт бот, — выгрузка словаря.
const maxRequestBytes = 1 << 20

// waitForCall — сколько Next ждёт исходящий вызов, прежде чем признать,
// что бот не ответил. Предел аварийный: в норме ответ приходит за
// миллисекунды, и большое значение ничего не замедляет.
const waitForCall = 15 * time.Second

// Server — фейковый Bot API.
type Server struct {
	// URL — адрес для telegram.Config.ServerURL.
	URL string

	updates chan json.RawMessage
	calls   chan Call

	// nextID раздаёт идентификаторы апдейтам и отправленным сообщениям.
	// Общий счётчик на то и другое: пересекаться им негде, а путаницы
	// в логах теста меньше, когда каждое число встречается один раз.
	nextID atomic.Int64
}

// New поднимает сервер и останавливает его по завершении теста.
func New(t *testing.T) *Server {
	t.Helper()

	s := &Server{
		// Очереди с запасом: тест кладёт апдейты по одному, но хендлер
		// на один апдейт отвечает несколькими вызовами.
		updates: make(chan json.RawMessage, 64),
		calls:   make(chan Call, 256),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/bot"+Token+"/", s.handle(t))
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)

	s.URL = httpServer.URL
	return s
}

// handle разбирает вызов метода Bot API.
func (s *Server) handle(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]

		// Библиотека шлёт всё multipart-формой, в том числе там, где
		// у Telegram документирован JSON. Размер ограничен: сервер хоть
		// и тестовый, а читает тело запроса в память.
		fields := map[string]string{}
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
		//nolint:gosec // тело уже ограничено MaxBytesReader строкой выше
		if err := r.ParseMultipartForm(maxRequestBytes); err == nil {
			for name, values := range r.MultipartForm.Value {
				if len(values) > 0 {
					fields[name] = values[0]
				}
			}
		}

		result, ok := s.dispatch(method, fields)
		if !ok {
			// Метод, которого фейк не знает, — не повод молча вернуть
			// ошибку: тест должен узнать, что бот пошёл незнакомой дорогой.
			t.Errorf("фейковый Bot API не знает метода %q, поля: %v", method, fields)
			respond(w, `{"ok":false,"error_code":501,"description":"метод не реализован"}`)
			return
		}

		if method != "getUpdates" && method != "getMe" {
			s.record(t, Call{Method: method, Fields: fields, Result: result})
		}
		respond(w, fmt.Sprintf(`{"ok":true,"result":%s}`, result))
	}
}

// dispatch отвечает на метод Bot API, возвращая тело поля result.
func (s *Server) dispatch(method string, fields map[string]string) (result string, known bool) {
	switch method {
	case "getMe":
		return `{"id":424242,"is_bot":true,"first_name":"lexi","username":"lexi_test_bot"}`, true

	case "getUpdates":
		return s.poll(fields), true

	case "sendMessage", "sendDocument":
		// Идентификатор нового сообщения важен: по нему тест нажимает
		// кнопки под нужным сообщением, а бот правит его на месте.
		id := s.nextID.Add(1)
		return fmt.Sprintf(`{"message_id":%d,"date":%d,"chat":{"id":%s,"type":"private"},"text":%s}`,
			id, stamp(), value(fields, "chat_id", "0"), quote(fields["text"])), true

	case "editMessageText", "editMessageReplyMarkup":
		return fmt.Sprintf(`{"message_id":%s,"date":%d,"chat":{"id":%s,"type":"private"},"text":%s}`,
			value(fields, "message_id", "0"), stamp(), value(fields, "chat_id", "0"), quote(fields["text"])), true

	case "answerCallbackQuery", "deleteMessage", "setMyCommands":
		return "true", true

	default:
		return "", false
	}
}

// poll отдаёт накопившиеся апдейты, ожидая их не дольше запрошенного
// таймаута long polling.
//
// Параметр offset игнорируется намеренно: настоящий Telegram по нему
// подтверждает доставку и переспрашивает неподтверждённое, а очередь фейка
// отдаёт каждый апдейт ровно один раз и хранить его повторно незачем.
func (s *Server) poll(fields map[string]string) string {
	timeout := time.Second
	if seconds, err := json.Number(fields["timeout"]).Int64(); err == nil && seconds > 0 {
		timeout = time.Duration(seconds) * time.Second
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var batch []json.RawMessage
	select {
	case first := <-s.updates:
		batch = append(batch, first)
	case <-timer.C:
		return "[]"
	}

	// Остальное, что успело накопиться, уезжает тем же ответом —
	// как это делает и настоящий getUpdates.
	for {
		select {
		case next := <-s.updates:
			batch = append(batch, next)
			continue
		default:
		}
		break
	}

	encoded, err := json.Marshal(batch)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

// record кладёт вызов в очередь для теста.
func (s *Server) record(t *testing.T, call Call) {
	if tracing {
		t.Logf("→ %s %s %s", call.Method, short(call.Text()), describe(call.Buttons(t)))
	}

	select {
	case s.calls <- call:
	default:
		t.Errorf("очередь вызовов переполнена, потерян %s", call.Method)
	}
}

// Call — вызов Bot API, сделанный ботом.
type Call struct {
	// Method — имя метода: sendMessage, editMessageText, answerCallbackQuery.
	Method string
	// Fields — поля запроса как они пришли в форме. Строковые лежат
	// как есть, остальные — в JSON.
	Fields map[string]string
	// Result — то, что сервер вернул в ответ.
	Result string
}

// Text возвращает текст сообщения.
func (c Call) Text() string { return c.Fields["text"] }

// MessageID возвращает идентификатор сообщения: у sendMessage — выданный
// сервером, у editMessageText — правленный. По нему тест нажимает кнопки.
func (c Call) MessageID(t *testing.T) int {
	t.Helper()

	if c.Method == "sendMessage" || c.Method == "sendDocument" {
		var sent struct {
			ID int `json:"message_id"`
		}
		if err := json.Unmarshal([]byte(c.Result), &sent); err != nil {
			t.Fatalf("разобрать ответ %s: %v", c.Method, err)
		}
		return sent.ID
	}

	id, err := json.Number(c.Fields["message_id"]).Int64()
	if err != nil {
		t.Fatalf("у вызова %s нет message_id: %v", c.Method, err)
	}
	return int(id)
}

// Button — кнопка инлайн-клавиатуры в том виде, в каком её увидел бы
// пользователь: подпись и то, что уедет обратно при нажатии.
type Button struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}

// Buttons возвращает кнопки под сообщением, развёрнутые в один список:
// тест ищет нужную по callback_data, а не по месту в раскладке.
func (c Call) Buttons(t *testing.T) []Button {
	t.Helper()

	markup, ok := c.Fields["reply_markup"]
	if !ok || markup == "" {
		return nil
	}

	var keyboard struct {
		Rows [][]Button `json:"inline_keyboard"`
	}
	if err := json.Unmarshal([]byte(markup), &keyboard); err != nil {
		t.Fatalf("разобрать клавиатуру %q: %v", markup, err)
	}

	var flat []Button
	for _, row := range keyboard.Rows {
		flat = append(flat, row...)
	}
	return flat
}

// Button находит кнопку, чьё callback_data начинается с prefix.
//
// Поиск по данным, а не по подписи: подпись переводится и меняется вместе
// с формулировками, а действие в callback_data — это контракт между
// кнопкой и роутером.
func (c Call) Button(t *testing.T, prefix string) Button {
	t.Helper()

	button, ok := c.FindButton(t, prefix)
	if !ok {
		t.Fatalf("под сообщением %q нет кнопки с данными %q; есть: %s",
			short(c.Text()), prefix, describe(c.Buttons(t)))
	}
	return button
}

// FindButton ищет кнопку, не роняя тест, если её нет.
func (c Call) FindButton(t *testing.T, prefix string) (Button, bool) {
	t.Helper()

	for _, button := range c.Buttons(t) {
		if strings.HasPrefix(button.Data, prefix) {
			return button, true
		}
	}
	return Button{}, false
}

// Next возвращает следующий вызов Bot API, дожидаясь его появления.
func (s *Server) Next(t *testing.T) Call {
	t.Helper()

	select {
	case call := <-s.calls:
		return call
	case <-time.After(waitForCall):
		t.Fatalf("бот не обратился к Bot API за %s", waitForCall)
		return Call{}
	}
}

// Screen возвращает следующий экран — отправленное или правленное сообщение.
//
// Учебная сессия правит одно сообщение на месте, а первое приходит
// отправкой, и тесту эта разница безразлична: ему нужно то, что человек
// видит перед собой.
func (s *Server) Screen(t *testing.T) Call {
	t.Helper()

	for range cap(s.calls) {
		call := s.Next(t)
		if call.Method == "sendMessage" || call.Method == "editMessageText" {
			return call
		}
	}
	t.Fatalf("экрана не дождались")
	return Call{}
}

// Drain забирает всё, что бот успел сказать, ничего не дожидаясь.
//
// Нужен там, где ответ бота проверяется не по экрану, а по базе: калитку
// надо закрыть за собой, иначе эти вызовы достанутся следующему шагу теста
// и он примет их за ответ на своё действие.
func (s *Server) Drain(t *testing.T) []Call {
	t.Helper()

	var calls []Call
	for {
		select {
		case call := <-s.calls:
			calls = append(calls, call)
		default:
			return calls
		}
	}
}

// Quiet проверяет, что бот больше ничего не сказал.
//
// Ждать здесь нечего: если бы вызов был, он бы уже лежал в очереди —
// тест доходит сюда, только разобрав всё, что бот прислал раньше.
func (s *Server) Quiet(t *testing.T) {
	t.Helper()

	select {
	case call := <-s.calls:
		t.Errorf("лишний вызов %s: %q", call.Method, short(call.Text()))
	default:
	}
}

// User — от чьего имени приходят апдейты.
type User struct {
	ID       int64
	Username string
	// Language — язык клиента Telegram: по нему выбирается язык интерфейса
	// до того, как человек выберет его сам.
	Language string
}

// Command ставит в очередь команду вида «/learn».
//
// Команда размечается сущностью bot_command, как это делает Telegram:
// приложение разбирает команды по разметке, а не по слешу в тексте.
func (s *Server) Command(from User, text string) {
	entities := fmt.Sprintf(`,"entities":[{"type":"bot_command","offset":0,"length":%d}]`,
		len([]rune(strings.Fields(text)[0])))
	s.enqueue(fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"date":%d,"from":%s,"chat":%s,"text":%s%s}}`,
		s.nextID.Add(1), s.nextID.Add(1), stamp(), from.json(), from.chat(), quote(text), entities))
}

// Text ставит в очередь обычное текстовое сообщение.
func (s *Server) Text(from User, text string) {
	s.enqueue(fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"date":%d,"from":%s,"chat":%s,"text":%s}}`,
		s.nextID.Add(1), s.nextID.Add(1), stamp(), from.json(), from.chat(), quote(text)))
}

// Press ставит в очередь нажатие кнопки под сообщением messageID.
//
// Поле date у вложенного сообщения обязано быть ненулевым: по нему
// библиотека отличает доступное сообщение от слишком старого, у которого
// не видно ни чата, ни идентификатора.
func (s *Server) Press(from User, messageID int, data string) {
	id := s.nextID.Add(1)
	s.enqueue(fmt.Sprintf(
		`{"update_id":%d,"callback_query":{"id":"cb-%d","from":%s,"chat_instance":"1","data":%s,`+
			`"message":{"message_id":%d,"date":%d,"chat":%s}}}`,
		id, id, from.json(), quote(data), messageID, stamp(), from.chat()))
}

func (s *Server) enqueue(update string) {
	s.updates <- json.RawMessage(update)
}

// tracing включает построчный протокол диалога с ботом. Разбирать
// сквозной тест по логу приложения тяжело: там нет ни кнопок, ни того,
// что человек видел на экране.
var tracing = os.Getenv("TGTEST_TRACE") != ""

func (u User) json() string {
	return fmt.Sprintf(`{"id":%d,"is_bot":false,"first_name":"tester","username":%s,"language_code":%s}`,
		u.ID, quote(u.Username), quote(u.Language))
}

// chat — приватный чат с пользователем: идентификатор чата совпадает
// с идентификатором пользователя, как у настоящего Telegram.
func (u User) chat() string {
	return fmt.Sprintf(`{"id":%d,"type":"private"}`, u.ID)
}

func respond(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

// stamp — момент отправки в формате Telegram. Настоящее время здесь
// безобидно: приложение эту отметку не читает, а библиотеке нужно лишь
// её ненулевое значение.
func stamp() int64 { return time.Now().Unix() }

func quote(s string) string {
	encoded, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(encoded)
}

func value(fields map[string]string, name, fallback string) string {
	if v, ok := fields[name]; ok && v != "" {
		return v
	}
	return fallback
}

func describe(buttons []Button) string {
	if len(buttons) == 0 {
		return "кнопок нет"
	}
	out := make([]string, 0, len(buttons))
	for _, button := range buttons {
		out = append(out, fmt.Sprintf("%q → %s", button.Text, button.Data))
	}
	return strings.Join(out, "; ")
}

// short укорачивает текст для сообщения об ошибке: экраны бота длинные,
// а в отчёте нужна опознавательная строка, а не всё сообщение.
func short(text string) string {
	text = strings.ReplaceAll(text, "\n", " ")
	if runes := []rune(text); len(runes) > 60 {
		return string(runes[:60]) + "…"
	}
	return text
}
