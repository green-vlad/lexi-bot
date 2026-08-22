package telegram

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"lexi-bot/internal/usecase/port"
)

// SendMessage отправляет сообщение и возвращает его идентификатор.
func (t *Transport) SendMessage(ctx context.Context, msg port.OutgoingMessage) (port.MessageID, error) {
	params := &bot.SendMessageParams{
		ChatID:      int64(msg.ChatID),
		Text:        msg.Text,
		ReplyMarkup: keyboard(msg.Keyboard),
	}
	if msg.DisablePreview {
		params.LinkPreviewOptions = &models.LinkPreviewOptions{IsDisabled: bot.True()}
	}

	sent, err := t.api.SendMessage(ctx, params)
	if err != nil {
		return 0, fmt.Errorf("отправить сообщение в чат %d: %w", msg.ChatID, err)
	}
	return port.MessageID(sent.ID), nil
}

// EditMessage правит текст и клавиатуру отправленного сообщения.
func (t *Transport) EditMessage(ctx context.Context, edit port.MessageEdit) error {
	params := &bot.EditMessageTextParams{
		ChatID:    int64(edit.ChatID),
		MessageID: int(edit.MessageID),
		Text:      edit.Text,
	}
	// Правка принимает только инлайн-разметку, поэтому приведение здесь
	// безопасно, но проверка стоит: молча отправить сообщение без кнопок
	// хуже, чем заметить, что клавиатура собралась не той.
	if markup, ok := keyboard(edit.Keyboard).(models.InlineKeyboardMarkup); ok {
		params.ReplyMarkup = markup
	}

	if _, err := t.api.EditMessageText(ctx, params); err != nil {
		return fmt.Errorf("исправить сообщение %d: %w", edit.MessageID, err)
	}
	return nil
}

// AnswerCallback отвечает на нажатие кнопки.
func (t *Transport) AnswerCallback(ctx context.Context, answer port.CallbackAnswer) error {
	_, err := t.api.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: answer.ID,
		Text:            answer.Text,
		ShowAlert:       answer.Alert,
	})
	if err != nil {
		return fmt.Errorf("ответить на нажатие кнопки: %w", err)
	}
	return nil
}

// SendDocument отправляет файл.
func (t *Transport) SendDocument(ctx context.Context, doc port.Document) error {
	_, err := t.api.SendDocument(ctx, &bot.SendDocumentParams{
		ChatID:  int64(doc.ChatID),
		Caption: doc.Caption,
		Document: &models.InputFileUpload{
			Filename: doc.FileName,
			Data:     bytes.NewReader(doc.Content),
		},
	})
	if err != nil {
		return fmt.Errorf("отправить файл %q: %w", doc.FileName, err)
	}
	return nil
}

// DownloadFile скачивает присланный пользователем файл.
//
// Размер проверяется дважды: сначала по описанию файла от Telegram, потом
// при чтении. Первая проверка спасает от бесполезной работы, вторая — от
// того, что описание соврало: содержимое приходит из внешнего мира, и верить
// ему на слово нельзя.
func (t *Transport) DownloadFile(ctx context.Context, fileID string, maxBytes int64) ([]byte, error) {
	file, err := t.api.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, fmt.Errorf("получить описание файла: %w", err)
	}
	if maxBytes > 0 && file.FileSize > maxBytes {
		return nil, fmt.Errorf("файл слишком большой: %d байт при пределе %d", file.FileSize, maxBytes)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.api.FileDownloadLink(file), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("собрать запрос за файлом: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("скачать файл: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("скачать файл: Telegram ответил %s", resp.Status)
	}

	reader := io.Reader(resp.Body)
	if maxBytes > 0 {
		// Лишний байт нужен, чтобы отличить «ровно предел» от «больше предела».
		reader = io.LimitReader(resp.Body, maxBytes+1)
	}
	content, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("прочитать файл: %w", err)
	}
	if maxBytes > 0 && int64(len(content)) > maxBytes {
		return nil, fmt.Errorf("файл слишком большой: предел %d байт", maxBytes)
	}
	return content, nil
}

// keyboard переводит нашу клавиатуру в разметку Telegram. Пустая клавиатура
// даёт nil: разметка из нуля кнопок Telegram не нравится.
func keyboard(k *port.Keyboard) models.ReplyMarkup {
	if k == nil || len(k.Rows) == 0 {
		return nil
	}

	rows := make([][]models.InlineKeyboardButton, 0, len(k.Rows))
	for _, row := range k.Rows {
		buttons := make([]models.InlineKeyboardButton, 0, len(row))
		for _, b := range row {
			buttons = append(buttons, models.InlineKeyboardButton{Text: b.Text, CallbackData: b.Data})
		}
		if len(buttons) > 0 {
			rows = append(rows, buttons)
		}
	}
	if len(rows) == 0 {
		return nil
	}
	return models.InlineKeyboardMarkup{InlineKeyboard: rows}
}
