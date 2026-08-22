// Package telegram связывает приложение с Telegram Bot API.
//
// Здесь и только здесь известно про библиотеку go-telegram/bot, про формат
// апдейтов и про то, что кнопки называются inline_keyboard. Выше по стеку
// живут port.Messenger и port.Update — их и видят сценарии.
//
// Транспорт — long polling: он не требует публичного HTTPS-домена и на сотне
// пользователей работает с огромным запасом (PLAN.md §1). Webhook при желании
// станет второй реализацией этого же пакета.
package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// Значения по умолчанию.
const (
	// DefaultPollTimeout — сколько Telegram держит запрос getUpdates,
	// пока нет событий. Долгое ожидание вместо частых опросов.
	DefaultPollTimeout = 30 * time.Second
	// DefaultShutdownTimeout — сколько ждём завершения начатых обработок
	// после сигнала остановки.
	DefaultShutdownTimeout = 20 * time.Second
	// DefaultWorkers — сколько апдейтов обрабатывается одновременно.
	// Сотня пользователей столько параллельных нажатий не создаёт,
	// но очередь из них не должна выстраиваться в затылок.
	DefaultWorkers = 8
)

// Config — параметры транспорта.
type Config struct {
	Token string
	// PollTimeout — время ожидания в getUpdates.
	PollTimeout time.Duration
	// ShutdownTimeout ограничивает ожидание начатых обработок при остановке.
	ShutdownTimeout time.Duration
	Workers         int
	Logger          *slog.Logger
	// ServerURL подменяет адрес Bot API: так тесты подставляют свой сервер
	// вместо настоящего Telegram.
	ServerURL string
}

// Transport принимает апдейты и отправляет сообщения.
type Transport struct {
	api             *bot.Bot
	log             *slog.Logger
	shutdownTimeout time.Duration

	// inFlight считает начатые обработки, чтобы остановка их дождалась.
	inFlight sync.WaitGroup
	// handler ставится в Run: обработчику нужен Messenger, а Messenger —
	// это сам транспорт, и на этапе создания его ещё нет. Читается
	// атомарно, потому что библиотека вызывает обработку из своих горутин.
	handler atomic.Pointer[port.UpdateHandler]
}

var _ port.Messenger = (*Transport)(nil)

// New создаёт транспорт и проверяет токен обращением к Telegram.
//
// Проверка при старте не формальность: с испорченным токеном процесс иначе
// поднялся бы, отрапортовал о готовности и молчал, а причина обнаружилась бы
// только по жалобе «бот не отвечает».
func New(cfg Config) (*Transport, error) {
	if cfg.Token == "" {
		return nil, errors.New("токен бота не задан")
	}

	t := &Transport{
		log:             orDefault(cfg.Logger),
		shutdownTimeout: orDuration(cfg.ShutdownTimeout, DefaultShutdownTimeout),
	}

	opts := []bot.Option{
		bot.WithDefaultHandler(t.dispatch),
		bot.WithWorkers(orInt(cfg.Workers, DefaultWorkers)),
		// Просим только то, что умеем обрабатывать: остальные типы апдейтов
		// не будут занимать очередь и мозолить глаза в логе.
		bot.WithAllowedUpdates(bot.AllowedUpdates{"message", "callback_query"}),
		bot.WithErrorsHandler(func(err error) {
			t.log.Error("ошибка Telegram API", slog.Any("error", err))
		}),
	}
	if cfg.PollTimeout > 0 {
		// Таймаут клиента должен быть заметно больше времени ожидания
		// в getUpdates: иначе клиент оборвёт запрос ровно тогда, когда
		// Telegram законно держит его, ожидая событий.
		client := &http.Client{Timeout: cfg.PollTimeout + 15*time.Second}
		opts = append(opts, bot.WithHTTPClient(cfg.PollTimeout, client))
	}
	if cfg.ServerURL != "" {
		opts = append(opts, bot.WithServerURL(cfg.ServerURL))
	}

	api, err := bot.New(cfg.Token, opts...)
	if err != nil {
		return nil, fmt.Errorf("подключиться к Telegram: %w", err)
	}
	t.api = api
	return t, nil
}

// Run крутит цикл получения апдейтов, пока не отменят контекст.
//
// После отмены новые апдейты не берутся, а начатые обработки доводятся
// до конца — но не дольше ShutdownTimeout: висящий запрос к базе не должен
// превращать штатную остановку в зависание, за которым придёт SIGKILL.
func (t *Transport) Run(ctx context.Context, handler port.UpdateHandler) error {
	if handler == nil {
		return errors.New("обработчик апдейтов не задан")
	}
	t.handler.Store(&handler)

	t.log.Info("бот слушает обновления")
	t.api.Start(ctx)

	done := make(chan struct{})
	go func() {
		t.inFlight.Wait()
		close(done)
	}()

	select {
	case <-done:
		t.log.Info("все начатые обработки завершены")
		return nil
	case <-time.After(t.shutdownTimeout):
		return fmt.Errorf("обработка апдейтов не завершилась за %s", t.shutdownTimeout)
	}
}

// dispatch превращает апдейт библиотеки в наш и отдаёт его обработчику.
func (t *Transport) dispatch(ctx context.Context, _ *bot.Bot, raw *models.Update) {
	handler := t.handler.Load()
	if handler == nil {
		return
	}

	update, ok := convert(raw)
	if !ok {
		return
	}

	t.inFlight.Add(1)
	defer t.inFlight.Done()

	// Контекст обработки отвязан от контекста опроса: по SIGTERM polling
	// прекращается сразу, а начатый ответ пользователю обязан дописаться —
	// иначе транзакция оборвётся на середине сессии. Ограничение
	// по времени при этом остаётся.
	handleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), t.shutdownTimeout)
	defer cancel()

	if err := (*handler).Handle(handleCtx, &update); err != nil {
		t.log.Error("обработка апдейта не удалась",
			slog.Int64("update_id", update.ID),
			slog.Any("error", err))
	}
}

// convert переводит апдейт Telegram в наш. Второе значение — false, если
// апдейт нам неинтересен.
func convert(raw *models.Update) (port.Update, bool) {
	switch {
	case raw == nil:
		return port.Update{}, false

	case raw.CallbackQuery != nil:
		cb := raw.CallbackQuery
		update := port.Update{
			ID:         raw.ID,
			Sender:     sender(&cb.From),
			ReceivedAt: time.Now(),
			Callback: &port.CallbackData{
				ID:   cb.ID,
				Data: cb.Data,
			},
		}
		// Сообщение под кнопкой может быть недоступно (слишком старое) —
		// тогда править нечего, но нажатие всё равно нужно обработать.
		if cb.Message.Message != nil {
			update.Chat = port.ChatID(cb.Message.Message.Chat.ID)
			update.Callback.MessageID = port.MessageID(cb.Message.Message.ID)
		}
		return update, true

	case raw.Message != nil:
		msg := raw.Message
		update := port.Update{
			ID:         raw.ID,
			Chat:       port.ChatID(msg.Chat.ID),
			Text:       msg.Text,
			ReceivedAt: time.Now(),
		}
		if msg.From != nil {
			update.Sender = sender(msg.From)
		}
		if msg.Document != nil {
			update.Document = &port.IncomingDocument{
				FileID:   msg.Document.FileID,
				FileName: msg.Document.FileName,
				MIMEType: msg.Document.MimeType,
				Size:     msg.Document.FileSize,
			}
			if update.Text == "" {
				update.Text = msg.Caption
			}
		}
		update.Command, update.Args = parseCommand(msg)
		return update, true

	default:
		return port.Update{}, false
	}
}

// parseCommand достаёт команду из сообщения.
//
// Telegram размечает команды сущностями, а не «текстом со слеша»: так «/help»
// в середине фразы или в цитате командой не считается. В группах к команде
// добавляется имя бота («/learn@lexi_bot») — его отбрасываем.
func parseCommand(msg *models.Message) (command, args string) {
	for _, entity := range msg.Entities {
		if entity.Type != models.MessageEntityTypeBotCommand || entity.Offset != 0 {
			continue
		}

		runes := []rune(msg.Text)
		if entity.Offset+entity.Length > len(runes) {
			return "", ""
		}

		command = strings.TrimPrefix(string(runes[entity.Offset:entity.Offset+entity.Length]), "/")
		if at := strings.IndexByte(command, '@'); at >= 0 {
			command = command[:at]
		}
		args = strings.TrimSpace(string(runes[entity.Offset+entity.Length:]))
		return strings.ToLower(command), args
	}
	return "", ""
}

func sender(from *models.User) port.Sender {
	return port.Sender{
		TelegramID:   user.TelegramID(from.ID),
		Username:     from.Username,
		LanguageCode: from.LanguageCode,
	}
}

func orDefault(log *slog.Logger) *slog.Logger {
	if log != nil {
		return log
	}
	return slog.Default()
}

func orDuration(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

func orInt(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}
