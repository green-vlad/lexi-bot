// Package logger настраивает структурированное логирование на базе log/slog.
//
// В проде вывод — JSON (машинно читаемый, удобен для docker logs и любого сборщика),
// в разработке — текст. Прикладной код никогда не создаёт логгер сам, а получает
// его в конструкторе, поэтому тесты подставляют буфер вместо stdout.
package logger

import (
	"context"
	"io"
	"log/slog"
)

// Format определяет представление записей.
type Format int

const (
	// FormatJSON — по одной JSON-записи на строку.
	FormatJSON Format = iota
	// FormatText — человекочитаемый вывод для локальной разработки.
	FormatText
)

// New создаёт логгер с указанным уровнем и форматом.
func New(w io.Writer, level slog.Level, format Format) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	if format == FormatText {
		handler = slog.NewTextHandler(w, opts)
	} else {
		handler = slog.NewJSONHandler(w, opts)
	}
	return slog.New(handler)
}

// Ключи контекста для сквозных атрибутов. Отдельный тип не даёт им столкнуться
// с ключами из других пакетов.
type contextKey int

const (
	userIDKey contextKey = iota
	telegramIDKey
	updateIDKey
)

// WithUserID кладёт наш внутренний идентификатор пользователя в контекст,
// чтобы он попал во все последующие записи, сделанные через FromContext.
//
// Идентификаторов у пользователя два, и путать их нельзя: по внутреннему
// он ищется в нашей базе, по телеграмному — в переписке. Поэтому они живут
// под разными ключами (см. WithTelegramID).
func WithUserID(ctx context.Context, userID int64) context.Context {
	return context.WithValue(ctx, userIDKey, userID)
}

// WithTelegramID кладёт идентификатор пользователя в Telegram.
func WithTelegramID(ctx context.Context, telegramID int64) context.Context {
	return context.WithValue(ctx, telegramIDKey, telegramID)
}

// WithUpdateID кладёт идентификатор апдейта Telegram в контекст: по нему
// восстанавливается вся цепочка обработки одного сообщения.
func WithUpdateID(ctx context.Context, updateID int64) context.Context {
	return context.WithValue(ctx, updateIDKey, updateID)
}

// FromContext возвращает логгер, дополненный атрибутами из контекста.
func FromContext(ctx context.Context, base *slog.Logger) *slog.Logger {
	if userID, ok := ctx.Value(userIDKey).(int64); ok {
		base = base.With(slog.Int64("user_id", userID))
	}
	if telegramID, ok := ctx.Value(telegramIDKey).(int64); ok {
		base = base.With(slog.Int64("tg_user_id", telegramID))
	}
	if updateID, ok := ctx.Value(updateIDKey).(int64); ok {
		base = base.With(slog.Int64("update_id", updateID))
	}
	return base
}
