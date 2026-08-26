package telegram

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/usecase/intro"
	"lexi-bot/internal/usecase/port"
)

// Действия кнопок знакомства с новыми словами.
const (
	actionNewWord  = "new"
	actionRemember = "rem"
	actionKnown    = "kno"
	actionSkip     = "skp"
)

// Intro — экран знакомства с новым словом.
//
// Слово показывается целиком: написание, чтение, все переводы и пример
// употребления. Спрашивают не перевод, а решение — начать учить, отметить
// знакомым или отложить. Проверять человека на слове, которого он не видел,
// бессмысленно: любой ответ будет угадыванием.
type Intro struct {
	intro     *intro.Service
	messenger port.Messenger
	menu      *Menu
}

// NewIntro создаёт хендлер знакомства.
func NewIntro(service *intro.Service, messenger port.Messenger, menu *Menu) (*Intro, error) {
	switch {
	case service == nil:
		return nil, errors.New("знакомству нужен сценарий")
	case messenger == nil:
		return nil, errors.New("знакомству нужен мессенджер")
	case menu == nil:
		return nil, errors.New("знакомству нужно меню занятия")
	}
	return &Intro{intro: service, messenger: messenger, menu: menu}, nil
}

// Register привязывает кнопки к роутеру.
func (i *Intro) Register(router *Router) {
	router.CallbackAction(actionNewWord, port.UpdateHandlerFunc(i.next))
	router.CallbackAction(actionRemember, port.UpdateHandlerFunc(i.decide(i.remember)))
	router.CallbackAction(actionKnown, port.UpdateHandlerFunc(i.decide(i.known)))
	router.CallbackAction(actionSkip, port.UpdateHandlerFunc(i.decide(i.skip)))
}

// next показывает следующее новое слово.
func (i *Intro) next(ctx context.Context, u *port.Update) error {
	callback, ok := decodeCallback(u.Callback.Data)
	if !ok {
		return nil
	}
	return i.show(ctx, u, study.CourseID(callback.ID))
}

// decision — что сделать с показанным словом.
type decision func(ctx context.Context, courseID study.CourseID, lexemeID lexicon.LexemeID) (string, error)

// decide превращает решение в обработчик кнопки: разбор кнопки, вызов
// сценария и переход к следующему слову у всех трёх одинаковы.
func (i *Intro) decide(apply decision) func(context.Context, *port.Update) error {
	return func(ctx context.Context, u *port.Update) error {
		callback, ok := decodeCallback(u.Callback.Data)
		if !ok {
			return nil
		}
		courseID, ok := decodeCourse(callback.Param)
		if !ok {
			// Кнопка от прошлой версии бота: молча ничего не делаем.
			return nil
		}

		note, err := apply(ctx, courseID, lexicon.LexemeID(callback.ID))
		if err != nil {
			return err
		}
		if note != "" {
			// Дневная норма кончилась между показом слова и нажатием:
			// решение не применилось, и молчать об этом нельзя.
			return i.finish(ctx, u, courseID, note)
		}
		return i.show(ctx, u, courseID)
	}
}

// remember — «запомнил»: слово уходит на первый шаг обучения.
func (i *Intro) remember(ctx context.Context, courseID study.CourseID, lexemeID lexicon.LexemeID) (string, error) {
	accepted, err := i.intro.Remember(ctx, courseID, lexemeID)
	if err != nil {
		return "", err
	}
	if !accepted {
		return "intro.done_daily_limit", nil
	}
	return "", nil
}

// known — «я уже знаю это слово»: оно не попадёт ни в знакомство,
// ни в повторения.
func (i *Intro) known(ctx context.Context, courseID study.CourseID, lexemeID lexicon.LexemeID) (string, error) {
	return "", i.intro.AlreadyKnow(ctx, courseID, lexemeID)
}

// skip — «пропустить»: слово вернётся завтра.
func (i *Intro) skip(ctx context.Context, courseID study.CourseID, lexemeID lexicon.LexemeID) (string, error) {
	return "", i.intro.Skip(ctx, courseID, lexemeID)
}

// show рисует карточку следующего нового слова.
func (i *Intro) show(ctx context.Context, u *port.Update, courseID study.CourseID) error {
	localizer, err := i.localizer(ctx)
	if err != nil {
		return err
	}

	word, reason, err := i.intro.Next(ctx, courseID)
	if err != nil {
		return err
	}
	if reason != intro.ReasonNone {
		return i.finish(ctx, u, courseID, finishKey(reason))
	}

	text, err := wordText(localizer, &word)
	if err != nil {
		return err
	}

	keyboard, err := NewKeyboard().
		Row(Button(mustText(localizer, "intro.remember"), decisionCallback(actionRemember, courseID, &word))).
		Row(Button(mustText(localizer, "intro.known"), decisionCallback(actionKnown, courseID, &word))).
		Row(Button(mustText(localizer, "intro.skip"), decisionCallback(actionSkip, courseID, &word))).
		Build()
	if err != nil {
		return err
	}
	return i.render(ctx, u, text, keyboard)
}

// decisionCallback собирает кнопку решения.
//
// В кнопке едет слово, а не карточка: карточки у нового слова может ещё
// не быть — она заводится тем самым решением, которое человек сейчас примет.
func decisionCallback(action string, courseID study.CourseID, word *intro.Word) Callback {
	return Callback{Action: action, ID: int64(word.Lexeme.ID), Param: strconv.FormatInt(int64(courseID), 36)}
}

// decodeCourse разбирает курс из параметра кнопки.
func decodeCourse(param string) (study.CourseID, bool) {
	id, err := strconv.ParseInt(param, 36, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return study.CourseID(id), true
}

// finishKey объясняет, почему новых слов больше нет.
func finishKey(reason intro.Reason) string {
	switch reason {
	case intro.ReasonDailyLimit:
		return "intro.done_daily_limit"
	case intro.ReasonPaused:
		return "intro.done_paused"
	default:
		return "intro.done_deck"
	}
}

// finish сообщает, что новых слов больше нет, и возвращает в меню.
func (i *Intro) finish(ctx context.Context, u *port.Update, courseID study.CourseID, key string) error {
	localizer, err := i.localizer(ctx)
	if err != nil {
		return err
	}

	text, err := localizer.T(key, nil)
	if err != nil {
		return err
	}

	keyboard, err := NewKeyboard().Row(Button(
		mustText(localizer, "menu.back"),
		Callback{Action: actionMenu, ID: int64(courseID)},
	)).Build()
	if err != nil {
		return err
	}
	return i.render(ctx, u, text, keyboard)
}

// wordText рисует карточку слова.
//
// Пример показывается, только если он есть: пустая строка после перевода
// выглядела бы обрывом, а у слов из личного словаря примеров не бывает.
func wordText(localizer port.Localizer, word *intro.Word) (string, error) {
	args := port.Args{
		"Term":        word.Lexeme.Term,
		"Translation": strings.Join(translationTexts(word.Translations), ", "),
	}

	key := "intro.card"
	switch {
	case word.Lexeme.Reading != "" && word.Lexeme.Example != "":
		key = "intro.card_full"
		args["Reading"] = word.Lexeme.Reading
		args["Example"] = word.Lexeme.Example
	case word.Lexeme.Reading != "":
		key = "intro.card_with_reading"
		args["Reading"] = word.Lexeme.Reading
	case word.Lexeme.Example != "":
		key = "intro.card_with_example"
		args["Example"] = word.Lexeme.Example
	}
	return localizer.T(key, args)
}

// translationTexts вытаскивает тексты переводов.
func translationTexts(translations []lexicon.Translation) []string {
	out := make([]string, 0, len(translations))
	for i := range translations {
		out = append(out, translations[i].Text)
	}
	return out
}

// render правит сообщение под кнопкой: карточка знакомства живёт в одном
// сообщении, как и карточка повторения.
func (i *Intro) render(ctx context.Context, u *port.Update, text string, keyboard *port.Keyboard) error {
	if u.Callback != nil && u.Callback.MessageID != 0 {
		return i.messenger.EditMessage(ctx, port.MessageEdit{
			ChatID: u.Chat, MessageID: u.Callback.MessageID, Text: text, Keyboard: keyboard,
		})
	}
	_, err := i.messenger.SendMessage(ctx, port.OutgoingMessage{
		ChatID: u.Chat, Text: text, Keyboard: keyboard,
	})
	return err
}

func (i *Intro) localizer(ctx context.Context) (port.Localizer, error) {
	localizer, ok := LocalizerFrom(ctx)
	if !ok {
		return nil, errors.New("нет локализатора: middleware локализации не подключён")
	}
	return localizer, nil
}
