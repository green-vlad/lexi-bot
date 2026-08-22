package telegram

import (
	"context"
	"fmt"

	"lexi-bot/internal/usecase/port"
)

// Ping отвечает pong. Команда не документирована для пользователей: она
// существует, чтобы одним сообщением убедиться, что бот жив и отвечает
// именно этот процесс, а не оставшийся с прошлого запуска.
func Ping(messenger port.Messenger) port.UpdateHandler {
	return port.UpdateHandlerFunc(func(ctx context.Context, u *port.Update) error {
		_, err := messenger.SendMessage(ctx, port.OutgoingMessage{ChatID: u.Chat, Text: "pong"})
		return err
	})
}

// UnknownCommand отвечает на команду, которой у бота нет.
//
// Молчать здесь нельзя: пользователь не различает «команды не существует»
// и «бот сломался», и в обоих случаях решает, что бот сломался.
func UnknownCommand(messenger port.Messenger) port.UpdateHandler {
	return Reply(messenger, "common.unknown_command")
}

// Reply отправляет в чат сообщение по ключу перевода.
func Reply(messenger port.Messenger, key string) port.UpdateHandler {
	return port.UpdateHandlerFunc(func(ctx context.Context, u *port.Update) error {
		localizer, ok := LocalizerFrom(ctx)
		if !ok {
			return fmt.Errorf("нет локализатора для сообщения %q", key)
		}

		text, err := localizer.T(key, nil)
		if err != nil {
			return err
		}

		_, err = messenger.SendMessage(ctx, port.OutgoingMessage{ChatID: u.Chat, Text: text})
		return err
	})
}
