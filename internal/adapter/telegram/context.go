package telegram

import (
	"context"

	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// Значения, которые middleware кладут в контекст обработки апдейта.
//
// Контекст, а не поля апдейта: пользователь и локализатор нужны почти
// каждому хендлеру, но ни один из них не является частью того, что прислал
// Telegram. Собственные типы ключей не дают им столкнуться с чужими.
type (
	userKey      struct{}
	localizerKey struct{}
)

// withUser кладёт пользователя в контекст.
func withUser(ctx context.Context, u user.User) context.Context {
	return context.WithValue(ctx, userKey{}, u)
}

// UserFrom достаёт пользователя, определённого middleware.
//
// Второе значение — false, если апдейт пришёл без отправителя (такое бывает
// у служебных сообщений) или middleware определения пользователя не подключён.
func UserFrom(ctx context.Context) (user.User, bool) {
	u, ok := ctx.Value(userKey{}).(user.User)
	return u, ok
}

// withLocalizer кладёт локализатор в контекст.
func withLocalizer(ctx context.Context, l port.Localizer) context.Context {
	return context.WithValue(ctx, localizerKey{}, l)
}

// LocalizerFrom достаёт локализатор языка пользователя.
func LocalizerFrom(ctx context.Context) (port.Localizer, bool) {
	l, ok := ctx.Value(localizerKey{}).(port.Localizer)
	return l, ok
}
