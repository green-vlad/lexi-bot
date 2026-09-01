package telegram

import (
	"context"
	"errors"

	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/account"
	"lexi-bot/internal/usecase/courses"
	"lexi-bot/internal/usecase/port"
)

// Действия кнопок учётной записи.
const (
	actionDeleteConfirm = "delme"
	actionDeleteCancel  = "keepme"
)

// Account — команды /pause, /resume и /delete_me.
type Account struct {
	account   *account.Service
	courses   *courses.Service
	messenger port.Messenger
}

// NewAccount создаёт хендлер учётной записи.
func NewAccount(service *account.Service, courseService *courses.Service, messenger port.Messenger) (*Account, error) {
	switch {
	case service == nil:
		return nil, errors.New("учётной записи нужен сценарий")
	case courseService == nil:
		return nil, errors.New("учётной записи нужен сценарий курсов")
	case messenger == nil:
		return nil, errors.New("учётной записи нужен мессенджер")
	}
	return &Account{account: service, courses: courseService, messenger: messenger}, nil
}

// Register привязывает команды и кнопки к роутеру.
func (a *Account) Register(router *Router) {
	router.Command("pause", port.UpdateHandlerFunc(a.pause))
	router.Command("resume", port.UpdateHandlerFunc(a.resume))
	router.Command("delete_me", port.UpdateHandlerFunc(a.askDelete))
	router.CallbackAction(actionDeleteConfirm, port.UpdateHandlerFunc(a.confirmDelete))
	router.CallbackAction(actionDeleteCancel, port.UpdateHandlerFunc(a.cancelDelete))
}

// pause останавливает все курсы разом.
func (a *Account) pause(ctx context.Context, u *port.Update) error {
	return a.switchAll(ctx, u, a.courses.PauseAll, "account.paused", "account.already_paused")
}

// resume возвращает в строй остановленные курсы.
func (a *Account) resume(ctx context.Context, u *port.Update) error {
	return a.switchAll(ctx, u, a.courses.ResumeAll, "account.resumed", "account.nothing_paused")
}

// switchAll — общая часть /pause и /resume: обе меняют состояние всех
// курсов сразу и отвечают числом изменённых.
func (a *Account) switchAll(ctx context.Context, u *port.Update,
	apply func(context.Context, user.ID) (int, error), doneKey, emptyKey string,
) error {
	known, ok := UserFrom(ctx)
	if !ok {
		return errors.New("команда без пользователя")
	}
	localizer, err := a.localizer(ctx)
	if err != nil {
		return err
	}

	changed, err := apply(ctx, known.ID)
	if err != nil {
		return err
	}
	if changed == 0 {
		// Менять было нечего — говорим прямо, а не рапортуем «готово»
		// о несделанном.
		return Reply(a.messenger, emptyKey).Handle(ctx, u)
	}

	text := plural(localizer, doneKey, changed)
	_, err = a.messenger.SendMessage(ctx, port.OutgoingMessage{ChatID: u.Chat, Text: text})
	return err
}

// askDelete спрашивает подтверждение.
//
// Удаление необратимо, и одной команды для него мало: /delete_me человек
// может набрать из любопытства или промахнувшись по автодополнению.
func (a *Account) askDelete(ctx context.Context, u *port.Update) error {
	localizer, err := a.localizer(ctx)
	if err != nil {
		return err
	}

	text, err := localizer.T("account.delete_confirm", nil)
	if err != nil {
		return err
	}

	keyboard, err := NewKeyboard().
		Row(Button(mustText(localizer, "account.delete_keep"), Callback{Action: actionDeleteCancel})).
		Row(Button(mustText(localizer, "account.delete_yes"), Callback{Action: actionDeleteConfirm})).
		Build()
	if err != nil {
		return err
	}

	_, err = a.messenger.SendMessage(ctx, port.OutgoingMessage{
		ChatID: u.Chat, Text: text, Keyboard: keyboard,
	})
	return err
}

// confirmDelete удаляет данные.
func (a *Account) confirmDelete(ctx context.Context, u *port.Update) error {
	known, ok := UserFrom(ctx)
	if !ok {
		return errors.New("удаление без пользователя")
	}
	localizer, err := a.localizer(ctx)
	if err != nil {
		return err
	}

	if err := a.account.Delete(ctx, known.ID); err != nil {
		return err
	}

	text, err := localizer.T("account.deleted", nil)
	if err != nil {
		return err
	}
	// Правим то же сообщение: кнопка «да, удалить» под уже удалённой
	// записью — приглашение нажать её второй раз.
	return a.messenger.EditMessage(ctx, port.MessageEdit{
		ChatID: u.Chat, MessageID: u.Callback.MessageID, Text: text,
	})
}

// cancelDelete снимает вопрос.
func (a *Account) cancelDelete(ctx context.Context, u *port.Update) error {
	localizer, err := a.localizer(ctx)
	if err != nil {
		return err
	}

	text, err := localizer.T("account.delete_cancelled", nil)
	if err != nil {
		return err
	}
	return a.messenger.EditMessage(ctx, port.MessageEdit{
		ChatID: u.Chat, MessageID: u.Callback.MessageID, Text: text,
	})
}

func (a *Account) localizer(ctx context.Context) (port.Localizer, error) {
	localizer, ok := LocalizerFrom(ctx)
	if !ok {
		return nil, errors.New("нет локализатора: middleware локализации не подключён")
	}
	return localizer, nil
}
