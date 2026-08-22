package telegram

import (
	"context"
	"errors"

	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// actionUILangSwitch — действие кнопки смены языка интерфейса. Отличается
// от кнопки в онбординге: там язык выбирают до знакомства, здесь меняют
// у того, кто уже занимается, и ответ должен прийти на новом языке сразу.
const actionUILangSwitch = "lang"

// Language — команды /help и /language.
//
// Обе делают по одному действию, поэтому сценария у них нет: заводить
// use-case ради одного вызова репозитория значило бы городить церемонию
// вокруг присваивания.
type Language struct {
	users     port.UserRepo
	messenger port.Messenger
	catalog   port.Catalog
}

// NewLanguage создаёт хендлер справки и смены языка.
func NewLanguage(users port.UserRepo, messenger port.Messenger, catalog port.Catalog) (*Language, error) {
	switch {
	case users == nil:
		return nil, errors.New("хендлеру языка нужен UserRepo")
	case messenger == nil:
		return nil, errors.New("хендлеру языка нужен мессенджер")
	case catalog == nil:
		return nil, errors.New("хендлеру языка нужен каталог переводов")
	}
	return &Language{users: users, messenger: messenger, catalog: catalog}, nil
}

// Register привязывает команды и кнопки к роутеру.
func (l *Language) Register(router *Router) {
	router.Command("help", Reply(l.messenger, "help.text"))
	router.Command("language", port.UpdateHandlerFunc(l.ask))
	router.CallbackAction(actionUILangSwitch, port.UpdateHandlerFunc(l.switchTo))
}

// ask показывает выбор языка интерфейса.
func (l *Language) ask(ctx context.Context, u *port.Update) error {
	localizer, ok := LocalizerFrom(ctx)
	if !ok {
		return errors.New("нет локализатора: middleware локализации не подключён")
	}

	buttons := make([]KeyboardButton, 0, len(user.SupportedUILangs()))
	for _, lang := range user.SupportedUILangs() {
		// Название языка пишется на нём самом: тот, кто не читает
		// по-русски, должен узнать «English» в списке.
		buttons = append(buttons, Button(langName(l.catalog.For(lang), lang.String()),
			Callback{Action: actionUILangSwitch, Param: lang.String()}))
	}

	keyboard, err := NewKeyboard().Grid(2, buttons...).Build()
	if err != nil {
		return err
	}

	text, err := localizer.T("language.choose", nil)
	if err != nil {
		return err
	}

	_, err = l.messenger.SendMessage(ctx, port.OutgoingMessage{ChatID: u.Chat, Text: text, Keyboard: keyboard})
	return err
}

// switchTo меняет язык интерфейса.
func (l *Language) switchTo(ctx context.Context, u *port.Update) error {
	known, ok := UserFrom(ctx)
	if !ok {
		return errors.New("смена языка без пользователя")
	}

	callback, ok := decodeCallback(u.Callback.Data)
	if !ok {
		return nil
	}
	lang, valid := parseUILang(callback.Param)
	if !valid {
		// Кнопка от прошлой версии бота: молча ничего не меняем.
		return nil
	}

	if err := l.users.SetUILang(ctx, known.ID, lang); err != nil {
		return err
	}

	// Подтверждение приходит уже на новом языке: локализатор в контексте
	// подобран до смены и говорит на прежнем.
	text, err := l.catalog.For(lang).T("language.changed", nil)
	if err != nil {
		return err
	}

	// Правим сообщение с кнопками: выбор сделан, и держать их незачем.
	if u.Callback.MessageID != 0 {
		return l.messenger.EditMessage(ctx, port.MessageEdit{
			ChatID:    u.Chat,
			MessageID: u.Callback.MessageID,
			Text:      text,
		})
	}

	_, err = l.messenger.SendMessage(ctx, port.OutgoingMessage{ChatID: u.Chat, Text: text})
	return err
}
