package telegram_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"lexi-bot/internal/adapter/telegram"
	"lexi-bot/internal/usecase/port"
)

const testToken = "12345:test-token"

// fakeAPI — подставной Bot API. Он же пригодится сквозному тесту в T-055,
// поэтому отвечает как настоящий: конвертом {"ok":true,"result":…}.
type fakeAPI struct {
	server *httptest.Server

	mu       sync.Mutex
	calls    []call
	updates  []json.RawMessage
	fileData []byte
}

type call struct {
	method string
	params map[string]any
	body   string
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()

	api := &fakeAPI{}
	api.server = httptest.NewServer(http.HandlerFunc(api.handle))
	t.Cleanup(api.server.Close)
	return api
}

func (a *fakeAPI) handle(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	method := parts[len(parts)-1]

	// Скачивание файла идёт не методом API, а по пути /file/bot<token>/…
	if strings.Contains(r.URL.Path, "/file/") {
		a.mu.Lock()
		data := a.fileData
		a.mu.Unlock()
		_, _ = w.Write(data)
		return
	}

	// Библиотека отправляет всё как multipart/form-data, поэтому тело
	// читается дважды: сырым — для проверок вроде «клавиатура доехала»,
	// и разобранным — для проверок отдельных полей.
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))

	params := map[string]any{}
	switch {
	case strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/"):
		if err := r.ParseMultipartForm(8 << 20); err == nil {
			for name, values := range r.MultipartForm.Value {
				params[name] = values[0]
			}
		}
	case json.Valid(body):
		_ = json.Unmarshal(body, &params)
	}

	a.mu.Lock()
	a.calls = append(a.calls, call{method: method, params: params, body: string(body)})
	a.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	switch method {
	case "getMe":
		fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"username":"lexi_test_bot"}}`)
	case "getUpdates":
		a.writeUpdates(w)
	case "sendMessage", "editMessageText", "sendDocument":
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":42,"date":0,"chat":{"id":777,"type":"private"}}}`)
	case "getFile":
		fmt.Fprint(w, `{"ok":true,"result":{"file_id":"file-1","file_size":11,"file_path":"documents/words.csv"}}`)
	default:
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	}
}

// writeUpdates отдаёт накопленные апдейты по одному разу: повторная выдача
// заставила бы бота обрабатывать их бесконечно.
func (a *fakeAPI) writeUpdates(w http.ResponseWriter) {
	a.mu.Lock()
	pending := a.updates
	a.updates = nil
	a.mu.Unlock()

	if len(pending) == 0 {
		// Пустой ответ — обычное дело для long polling: событий пока нет.
		fmt.Fprint(w, `{"ok":true,"result":[]}`)
		return
	}

	joined := make([]string, 0, len(pending))
	for _, u := range pending {
		joined = append(joined, string(u))
	}
	fmt.Fprintf(w, `{"ok":true,"result":[%s]}`, strings.Join(joined, ","))
}

func (a *fakeAPI) push(update string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.updates = append(a.updates, json.RawMessage(update))
}

func (a *fakeAPI) lastCall(t *testing.T, method string) call {
	t.Helper()

	a.mu.Lock()
	defer a.mu.Unlock()
	for i := len(a.calls) - 1; i >= 0; i-- {
		if a.calls[i].method == method {
			return a.calls[i]
		}
	}
	t.Fatalf("вызова %s не было; были: %v", method, a.methods())
	return call{}
}

func (a *fakeAPI) methods() []string {
	out := make([]string, 0, len(a.calls))
	for _, c := range a.calls {
		out = append(out, c.method)
	}
	return out
}

func newTransport(t *testing.T, api *fakeAPI) *telegram.Transport {
	t.Helper()

	transport, err := telegram.New(telegram.Config{
		Token:           testToken,
		ServerURL:       api.server.URL,
		ShutdownTimeout: 2 * time.Second,
		PollTimeout:     100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() вернул ошибку: %v", err)
	}
	return transport
}

// runTransport запускает цикл получения апдейтов и возвращает функцию
// остановки, которая дожидается завершения Run.
func runTransport(t *testing.T, transport *telegram.Transport, handler port.UpdateHandlerFunc) func() {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- transport.Run(ctx, handler) }()

	return func() {
		t.Helper()

		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run() вернул ошибку: %v", err)
		}
	}
}

func TestNewChecksInput(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)

	if _, err := telegram.New(telegram.Config{ServerURL: api.server.URL}); err == nil {
		t.Error("пустой токен должен быть ошибкой")
	}

	transport := newTransport(t, api)
	if err := transport.Run(context.Background(), nil); err == nil {
		t.Error("Run() без обработчика должен быть ошибкой")
	}
}

func TestSendMessage(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	transport := newTransport(t, api)

	id, err := transport.SendMessage(context.Background(), port.OutgoingMessage{
		ChatID: 777,
		Text:   "привет",
		Keyboard: &port.Keyboard{Rows: [][]port.Button{
			{{Text: "Помню", Data: "rate:good"}, {Text: "Не помню", Data: "rate:again"}},
		}},
		DisablePreview: true,
	})
	if err != nil {
		t.Fatalf("SendMessage() вернул ошибку: %v", err)
	}
	if id != 42 {
		t.Errorf("MessageID = %d, ожидалось 42", id)
	}

	sent := api.lastCall(t, "sendMessage")
	if sent.params["text"] != "привет" {
		t.Errorf("text = %v", sent.params["text"])
	}
	if fmt.Sprint(sent.params["chat_id"]) != "777" {
		t.Errorf("chat_id = %v", sent.params["chat_id"])
	}
	if !strings.Contains(sent.body, "rate:good") || !strings.Contains(sent.body, "Не помню") {
		t.Errorf("клавиатура не доехала: %s", sent.body)
	}
}

func TestSendMessageWithoutKeyboard(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	transport := newTransport(t, api)

	// Клавиатура из нуля кнопок Telegram не нравится: её быть не должно.
	_, err := transport.SendMessage(context.Background(), port.OutgoingMessage{
		ChatID:   777,
		Text:     "привет",
		Keyboard: &port.Keyboard{},
	})
	if err != nil {
		t.Fatalf("SendMessage() вернул ошибку: %v", err)
	}

	if body := api.lastCall(t, "sendMessage").body; strings.Contains(body, "reply_markup") {
		t.Errorf("в запросе оказалась пустая клавиатура: %s", body)
	}
}

func TestEditMessageAndAnswerCallback(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	transport := newTransport(t, api)
	ctx := context.Background()

	err := transport.EditMessage(ctx, port.MessageEdit{
		ChatID:    777,
		MessageID: 42,
		Text:      "дом",
		Keyboard:  &port.Keyboard{Rows: [][]port.Button{{{Text: "Дальше", Data: "next"}}}},
	})
	if err != nil {
		t.Fatalf("EditMessage() вернул ошибку: %v", err)
	}

	edit := api.lastCall(t, "editMessageText")
	if edit.params["text"] != "дом" || fmt.Sprint(edit.params["message_id"]) != "42" {
		t.Errorf("правка ушла с параметрами %v", edit.params)
	}
	if !strings.Contains(edit.body, "Дальше") {
		t.Errorf("клавиатура при правке потерялась: %s", edit.body)
	}

	if err := transport.AnswerCallback(ctx, port.CallbackAnswer{ID: "cb-1", Text: "верно", Alert: true}); err != nil {
		t.Fatalf("AnswerCallback() вернул ошибку: %v", err)
	}
	answer := api.lastCall(t, "answerCallbackQuery")
	// Значения приходят строками: это форма, а не JSON.
	if answer.params["callback_query_id"] != "cb-1" || answer.params["show_alert"] != "true" {
		t.Errorf("ответ на нажатие ушёл с параметрами %v", answer.params)
	}
}

func TestSendDocument(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	transport := newTransport(t, api)

	err := transport.SendDocument(context.Background(), port.Document{
		ChatID:   777,
		FileName: "errors.csv",
		Caption:  "строки с ошибками",
		Content:  []byte("строка,причина\n"),
	})
	if err != nil {
		t.Fatalf("SendDocument() вернул ошибку: %v", err)
	}

	// Файл уходит multipart, поэтому проверяем сырое тело.
	doc := api.lastCall(t, "sendDocument")
	if !strings.Contains(doc.body, "errors.csv") || !strings.Contains(doc.body, "строки с ошибками") {
		t.Errorf("файл ушёл без имени или подписи: %s", doc.body)
	}
}

func TestDownloadFile(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	api.fileData = []byte("слово,перевод")
	transport := newTransport(t, api)
	ctx := context.Background()

	content, err := transport.DownloadFile(ctx, "file-1", 1024)
	if err != nil {
		t.Fatalf("DownloadFile() вернул ошибку: %v", err)
	}
	if string(content) != "слово,перевод" {
		t.Errorf("скачано %q", content)
	}

	// Предел проверяется по описанию файла: качать заведомо большой файл
	// незачем.
	if _, err := transport.DownloadFile(ctx, "file-1", 5); err == nil {
		t.Error("слишком большой файл должен быть отклонён")
	}
}

func TestDownloadFileDistrustsDeclaredSize(t *testing.T) {
	t.Parallel()

	// Описание говорит 11 байт, а приходит больше: содержимое из внешнего
	// мира, и верить описанию на слово нельзя.
	api := newFakeAPI(t)
	api.fileData = []byte(strings.Repeat("a", 4096))
	transport := newTransport(t, api)

	if _, err := transport.DownloadFile(context.Background(), "file-1", 100); err == nil {
		t.Error("файл, оказавшийся больше обещанного, должен быть отклонён")
	}
}

func TestRunConvertsUpdates(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)

	received := make(chan port.Update, 4)
	transport := newTransport(t, api)
	handler := port.UpdateHandlerFunc(func(_ context.Context, u *port.Update) error {
		received <- *u
		return nil
	})

	// Команда с упоминанием бота — так она приходит из групп.
	api.push(`{"update_id":1,"message":{"message_id":10,"date":0,
		"chat":{"id":777,"type":"private"},
		"from":{"id":555,"is_bot":false,"username":"durov","language_code":"ru-RU"},
		"text":"/learn@lexi_test_bot корейский",
		"entities":[{"type":"bot_command","offset":0,"length":21}]}}`)
	// Ненулевая дата обязательна: нулём Telegram помечает сообщение,
	// до которого боту уже не дотянуться, и править его нельзя.
	api.push(`{"update_id":2,"callback_query":{"id":"cb-7","data":"rate:good",
		"from":{"id":555,"is_bot":false,"username":"durov"},
		"message":{"message_id":10,"date":1755000000,"chat":{"id":777,"type":"private"}}}}`)
	api.push(`{"update_id":3,"message":{"message_id":11,"date":0,
		"chat":{"id":777,"type":"private"},
		"from":{"id":555,"is_bot":false},
		"caption":"мои слова",
		"document":{"file_id":"file-1","file_name":"words.csv","mime_type":"text/csv","file_size":2048}}}`)

	stop := runTransport(t, transport, handler)

	updates := make([]port.Update, 0, 3)
	for len(updates) < 3 {
		select {
		case u := <-received:
			updates = append(updates, u)
		case <-time.After(5 * time.Second):
			t.Fatalf("получено %d апдейтов из трёх", len(updates))
		}
	}
	stop()

	byID := map[int64]port.Update{}
	for _, u := range updates {
		byID[u.ID] = u
	}

	command := byID[1]
	if command.Command != "learn" {
		t.Errorf("Command = %q, ожидалось learn (упоминание бота отбрасывается)", command.Command)
	}
	if command.Args != "корейский" {
		t.Errorf("Args = %q, ожидалось «корейский»", command.Args)
	}
	if !command.IsCommand() {
		t.Error("апдейт должен считаться командой")
	}
	if command.Sender.TelegramID != 555 || command.Sender.Username != "durov" {
		t.Errorf("отправитель = %+v", command.Sender)
	}
	if command.Sender.LanguageCode != "ru-RU" {
		t.Errorf("LanguageCode = %q", command.Sender.LanguageCode)
	}
	if command.Chat != 777 {
		t.Errorf("Chat = %d", command.Chat)
	}
	if command.ReceivedAt.IsZero() {
		t.Error("ReceivedAt не проставлен")
	}

	callback := byID[2]
	if callback.Callback == nil {
		t.Fatal("нажатие кнопки не распознано")
	}
	if callback.Callback.Data != "rate:good" || callback.Callback.ID != "cb-7" {
		t.Errorf("callback = %+v", callback.Callback)
	}
	if callback.Callback.MessageID != 10 || callback.Chat != 777 {
		t.Errorf("сообщение под кнопкой = %d в чате %d", callback.Callback.MessageID, callback.Chat)
	}
	if callback.IsCommand() {
		t.Error("нажатие кнопки не команда")
	}

	document := byID[3]
	if document.Document == nil {
		t.Fatal("присланный файл не распознан")
	}
	if document.Document.FileName != "words.csv" || document.Document.Size != 2048 {
		t.Errorf("файл = %+v", document.Document)
	}
	// Подпись к файлу становится текстом: пользователь пишет её вместо
	// отдельного сообщения.
	if document.Text != "мои слова" {
		t.Errorf("Text = %q, ожидалась подпись к файлу", document.Text)
	}
}

func TestPlainTextIsNotACommand(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)
	received := make(chan port.Update, 1)
	transport := newTransport(t, api)
	handler := port.UpdateHandlerFunc(func(_ context.Context, u *port.Update) error {
		received <- *u
		return nil
	})

	// Слеш в середине фразы командой не является — Telegram размечает
	// команды сущностями, и мы полагаемся на разметку, а не на первый символ.
	api.push(`{"update_id":1,"message":{"message_id":10,"date":0,
		"chat":{"id":777,"type":"private"},"from":{"id":555,"is_bot":false},
		"text":"посмотри /help потом"}}`)

	stop := runTransport(t, transport, handler)
	defer stop()

	select {
	case u := <-received:
		if u.IsCommand() {
			t.Errorf("текст %q принят за команду %q", u.Text, u.Command)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("апдейт не дошёл до обработчика")
	}
}

func TestGracefulShutdownWaitsForHandlers(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)

	started := make(chan struct{})
	finished := make(chan struct{})
	transport := newTransport(t, api)
	handler := port.UpdateHandlerFunc(func(ctx context.Context, _ *port.Update) error {
		close(started)
		// Обработка идёт дольше, чем живёт контекст опроса: по SIGTERM
		// начатый ответ пользователю обязан дописаться.
		time.Sleep(300 * time.Millisecond)
		if ctx.Err() != nil {
			t.Errorf("контекст обработки отменён вместе с опросом: %v", ctx.Err())
		}
		close(finished)
		return nil
	})

	api.push(`{"update_id":1,"message":{"message_id":10,"date":0,
		"chat":{"id":777,"type":"private"},"from":{"id":555,"is_bot":false},"text":"привет"}}`)

	stop := runTransport(t, transport, handler)

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("обработчик не начал работу")
	}
	stop()

	select {
	case <-finished:
	default:
		t.Error("Run() вернулся, не дождавшись начатой обработки")
	}
}

func TestHandlerErrorDoesNotStopPolling(t *testing.T) {
	t.Parallel()

	api := newFakeAPI(t)

	seen := make(chan int64, 2)
	transport := newTransport(t, api)
	handler := port.UpdateHandlerFunc(func(_ context.Context, u *port.Update) error {
		seen <- u.ID
		return errors.New("сценарий сломался")
	})

	api.push(`{"update_id":1,"message":{"message_id":10,"date":0,
		"chat":{"id":777,"type":"private"},"from":{"id":555,"is_bot":false},"text":"раз"}}`)

	stop := runTransport(t, transport, handler)
	defer stop()

	select {
	case <-seen:
	case <-time.After(5 * time.Second):
		t.Fatal("первый апдейт не дошёл")
	}

	// Ошибка одного апдейта не должна ронять цикл: следующий обязан дойти.
	api.push(`{"update_id":2,"message":{"message_id":11,"date":0,
		"chat":{"id":777,"type":"private"},"from":{"id":555,"is_bot":false},"text":"два"}}`)

	select {
	case id := <-seen:
		if id != 2 {
			t.Errorf("получен апдейт %d, ожидался второй", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("после ошибки обработки цикл встал")
	}
}
