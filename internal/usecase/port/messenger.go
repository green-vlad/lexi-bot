package port

import (
	"context"
	"time"

	"lexi-bot/internal/domain/user"
)

// ChatID — идентификатор чата в Telegram.
type ChatID int64

// MessageID — идентификатор сообщения внутри чата.
type MessageID int

// Button — кнопка инлайн-клавиатуры.
//
// Data уезжает в callback_data, где Telegram отводит 64 байта на всё
// сообщение; компактный формат и проверка длины — забота кодека из T-025.
type Button struct {
	Text string
	Data string
}

// Keyboard — инлайн-клавиатура: строки кнопок.
type Keyboard struct {
	Rows [][]Button
}

// OutgoingMessage — сообщение к отправке.
type OutgoingMessage struct {
	ChatID ChatID
	Text   string
	// Keyboard — кнопки под сообщением; nil означает «без кнопок».
	Keyboard *Keyboard
	// DisablePreview убирает раскрытие ссылок: в карточках со словами
	// предпросмотр только мешает.
	DisablePreview bool
}

// MessageEdit — правка уже отправленного сообщения.
//
// Правка вместо новой отправки — то, чем учебная сессия отличается
// от простыни сообщений: карточка меняется на месте.
type MessageEdit struct {
	ChatID    ChatID
	MessageID MessageID
	Text      string
	Keyboard  *Keyboard
}

// CallbackAnswer — ответ на нажатие кнопки.
//
// Отвечать обязательно на каждое нажатие, иначе у пользователя висят
// «часики» до таймаута Telegram.
type CallbackAnswer struct {
	ID string
	// Text — короткое всплывающее уведомление; пусто — просто снять часики.
	Text string
	// Alert показывает текст модальным окном вместо всплывающей подсказки.
	Alert bool
}

// Document — файл к отправке: отчёт об ошибках импорта или выгрузка словаря.
type Document struct {
	ChatID   ChatID
	FileName string
	Caption  string
	Content  []byte
}

// Messenger — всё, что приложение делает с Telegram.
//
// Интерфейс узкий намеренно: хендлеры тестируются на фейке в пару десятков
// строк, а замена библиотеки затрагивает один пакет.
type Messenger interface {
	// SendMessage отправляет сообщение и возвращает его идентификатор —
	// по нему сообщение потом правится.
	SendMessage(ctx context.Context, msg OutgoingMessage) (MessageID, error)

	// EditMessage правит текст и клавиатуру отправленного сообщения.
	EditMessage(ctx context.Context, edit MessageEdit) error

	// AnswerCallback отвечает на нажатие кнопки.
	AnswerCallback(ctx context.Context, answer CallbackAnswer) error

	// SendDocument отправляет файл.
	SendDocument(ctx context.Context, doc Document) error

	// DownloadFile скачивает присланный пользователем файл. Ограничение
	// размера — часть подписи, а не совет: файл приходит из внешнего мира,
	// и читать его в память целиком без предела нельзя.
	DownloadFile(ctx context.Context, fileID string, maxBytes int64) ([]byte, error)
}

// Sender — кто прислал апдейт.
type Sender struct {
	TelegramID user.TelegramID
	Username   string
	// LanguageCode — язык клиента Telegram (ru, en-US). По нему выбирается
	// язык интерфейса до того, как пользователь выберет его сам.
	LanguageCode string
}

// CallbackData — нажатие кнопки.
type CallbackData struct {
	ID   string
	Data string
	// MessageID — сообщение, под которым нажали кнопку: его и правим.
	MessageID MessageID
}

// IncomingDocument — присланный пользователем файл.
type IncomingDocument struct {
	FileID   string
	FileName string
	MIMEType string
	Size     int64
}

// Update — входящее событие в том виде, в каком его понимает приложение.
type Update struct {
	ID     int64
	Chat   ChatID
	Sender Sender
	// Text — текст сообщения целиком, включая команду.
	Text string
	// Command — команда без слеша и без упоминания бота: у «/learn@lexi_bot»
	// это «learn». Пусто, если сообщение командой не является.
	Command string
	// Args — то, что шло после команды.
	Args string
	// Callback заполнен, если это нажатие кнопки.
	Callback *CallbackData
	// Document заполнен, если прислан файл.
	Document *IncomingDocument
	// ReceivedAt — момент получения апдейта нами, а не отправки его
	// пользователем: по нему считается время ответа в учебной сессии.
	ReceivedAt time.Time
}

// IsCommand сообщает, что апдейт — это команда.
func (u *Update) IsCommand() bool { return u.Command != "" }

// UpdateHandler обрабатывает один апдейт.
//
// Ошибка означает, что обработка сорвалась: транспорт её логирует, а извиняться
// перед пользователем — дело роутера, который знает, что именно сломалось.
//
// Апдейт передаётся указателем: он проходит через всю цепочку middleware,
// и копировать его на каждом слое незачем.
type UpdateHandler interface {
	Handle(ctx context.Context, u *Update) error
}

// UpdateHandlerFunc превращает функцию в UpdateHandler.
type UpdateHandlerFunc func(ctx context.Context, u *Update) error

// Handle вызывает саму функцию.
func (f UpdateHandlerFunc) Handle(ctx context.Context, u *Update) error { return f(ctx, u) }
