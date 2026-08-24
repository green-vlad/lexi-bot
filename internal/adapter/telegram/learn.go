package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/internal/usecase/session"
)

// Действия кнопок учебной сессии.
const (
	actionNext   = "next"
	actionShow   = "show"
	actionRate   = "rate"
	actionAnswer = "ans"
)

// Learn — команда /learn и режимы проверки.
//
// Карточка живёт в одном сообщении, которое правится на месте: слово →
// перевод с кнопками оценки → следующая карточка. Занятие из тридцати
// карточек иначе превращалось бы в сотню сообщений в чате.
type Learn struct {
	session   *session.Service
	courses   port.CourseRepo
	messenger port.Messenger
	catalog   port.Catalog
}

// NewLearn создаёт хендлер учебной сессии.
func NewLearn(service *session.Service, courses port.CourseRepo, messenger port.Messenger, catalog port.Catalog) (*Learn, error) {
	switch {
	case service == nil:
		return nil, errors.New("сессии нужен сценарий")
	case courses == nil:
		return nil, errors.New("сессии нужен CourseRepo")
	case messenger == nil:
		return nil, errors.New("сессии нужен мессенджер")
	case catalog == nil:
		return nil, errors.New("сессии нужен каталог переводов")
	}
	return &Learn{session: service, courses: courses, messenger: messenger, catalog: catalog}, nil
}

// Register привязывает команду и кнопки к роутеру.
func (l *Learn) Register(router *Router) {
	router.Command("learn", port.UpdateHandlerFunc(l.start))
	router.CallbackAction(actionNext, port.UpdateHandlerFunc(l.next))
	router.CallbackAction(actionShow, port.UpdateHandlerFunc(l.show))
	router.CallbackAction(actionRate, port.UpdateHandlerFunc(l.rate))
}

// start начинает занятие.
func (l *Learn) start(ctx context.Context, u *port.Update) error {
	known, ok := UserFrom(ctx)
	if !ok {
		return errors.New("/learn без пользователя")
	}

	courses, err := l.courses.ByUser(ctx, known.ID)
	if err != nil {
		return fmt.Errorf("получить курсы: %w", err)
	}

	course, ok := firstActive(courses)
	if !ok {
		// Учить нечего: человек ещё не выбрал курс или поставил всё на паузу.
		return Reply(l.messenger, "learn.no_course").Handle(ctx, u)
	}
	return l.showNext(ctx, u, course.ID, 0)
}

// next показывает следующую карточку после разбора предыдущей.
func (l *Learn) next(ctx context.Context, u *port.Update) error {
	callback, ok := decodeCallback(u.Callback.Data)
	if !ok {
		return nil
	}
	return l.showNext(ctx, u, study.CourseID(callback.ID), u.Callback.MessageID)
}

// showNext достаёт следующую карточку и показывает её, правя сообщение
// messageID; при нулевом messageID отправляет новое.
func (l *Learn) showNext(ctx context.Context, u *port.Update, courseID study.CourseID, messageID port.MessageID) error {
	item, reason, err := l.session.Next(ctx, courseID)
	if err != nil {
		return err
	}
	if reason != session.ReasonNone {
		return l.finish(ctx, u, messageID, reason)
	}
	return l.showCard(ctx, u, messageID, &item)
}

// showCard рисует вопрос.
func (l *Learn) showCard(ctx context.Context, u *port.Update, messageID port.MessageID, item *session.Item) error {
	localizer, err := l.localizer(ctx)
	if err != nil {
		return err
	}

	text, err := cardText(localizer, item)
	if err != nil {
		return err
	}

	keyboard, err := l.questionKeyboard(localizer, item)
	if err != nil {
		return err
	}
	return l.render(ctx, u, messageID, text, keyboard)
}

// questionKeyboard собирает кнопки вопроса. Пока режим один; выбор из
// четырёх и ввод текстом добавятся в T-032 и T-033.
func (l *Learn) questionKeyboard(localizer port.Localizer, item *session.Item) (*port.Keyboard, error) {
	return NewKeyboard().Row(Button(
		mustText(localizer, "learn.show_translation"),
		Callback{Action: actionShow, ID: int64(item.Card.ID), Param: session.Attempt(&item.Card)},
	)).Build()
}

// show открывает перевод и предлагает оценить себя.
func (l *Learn) show(ctx context.Context, u *port.Update) error {
	callback, ok := decodeCallback(u.Callback.Data)
	if !ok {
		return nil
	}

	item, err := l.session.Card(ctx, study.CardID(callback.ID))
	if err != nil {
		return err
	}
	// Токен из кнопки сверяется с карточкой: если на неё уже ответили,
	// показывать перевод и предлагать оценку поздно.
	if callback.Param != session.Attempt(&item.Card) {
		return l.stale(ctx, u)
	}

	localizer, err := l.localizer(ctx)
	if err != nil {
		return err
	}

	text, err := localizer.T("learn.revealed", port.Args{
		"Term":        item.Lexeme.Term,
		"Translation": strings.Join(translationTexts(item.Translations), ", "),
	})
	if err != nil {
		return err
	}

	keyboard, err := l.ratingKeyboard(localizer, &item)
	if err != nil {
		return err
	}
	return l.render(ctx, u, u.Callback.MessageID, text, keyboard)
}

// ratingKeyboard собирает кнопки самооценки — те самые четыре оценки SM-2.
func (l *Learn) ratingKeyboard(localizer port.Localizer, item *session.Item) (*port.Keyboard, error) {
	attempt := session.Attempt(&item.Card)

	buttons := make([]KeyboardButton, 0, len(study.Ratings()))
	for _, rating := range study.Ratings() {
		buttons = append(buttons, Button(
			mustText(localizer, "learn.rate_"+rating.String()),
			Callback{
				Action: actionRate,
				ID:     int64(item.Card.ID),
				// Оценка и токен попытки вместе: разбор идёт на три части,
				// поэтому двоеточие внутри параметра допустимо.
				Param: rating.String() + ":" + attempt,
			},
		))
	}
	return NewKeyboard().Grid(2, buttons...).Build()
}

// rate принимает самооценку и переходит к следующей карточке.
func (l *Learn) rate(ctx context.Context, u *port.Update) error {
	callback, ok := decodeCallback(u.Callback.Data)
	if !ok {
		return nil
	}

	name, attempt, _ := strings.Cut(callback.Param, ":")
	rating, valid := parseRating(name)
	if !valid {
		// Кнопка от прошлой версии бота: молча ничего не делаем.
		return nil
	}

	outcome, err := l.session.Submit(ctx, session.Answer{
		CardID:     study.CardID(callback.ID),
		Attempt:    attempt,
		Mode:       study.ModeRecall,
		SelfRating: rating,
	})
	if err != nil {
		return err
	}
	if outcome.Duplicate {
		return l.stale(ctx, u)
	}

	return l.showNext(ctx, u, outcome.CourseID, u.Callback.MessageID)
}

// finish сообщает, что карточек больше нет.
func (l *Learn) finish(ctx context.Context, u *port.Update, messageID port.MessageID, reason session.Reason) error {
	key := "learn.done_caught_up"
	switch reason {
	case session.ReasonDailyLimit:
		key = "learn.done_daily_limit"
	case session.ReasonPaused:
		key = "learn.done_paused"
	}

	localizer, err := l.localizer(ctx)
	if err != nil {
		return err
	}
	text, err := localizer.T(key, nil)
	if err != nil {
		return err
	}
	// Кнопки убираем: занятие кончилось, нажимать нечего.
	return l.render(ctx, u, messageID, text, nil)
}

// stale отвечает на кнопку от карточки, на которую уже ответили.
func (l *Learn) stale(ctx context.Context, u *port.Update) error {
	return Reply(l.messenger, "learn.stale_button").Handle(ctx, u)
}

// render правит сообщение, если оно известно, и отправляет новое, если нет.
func (l *Learn) render(ctx context.Context, u *port.Update, messageID port.MessageID, text string, keyboard *port.Keyboard) error {
	if messageID != 0 {
		return l.messenger.EditMessage(ctx, port.MessageEdit{
			ChatID:    u.Chat,
			MessageID: messageID,
			Text:      text,
			Keyboard:  keyboard,
		})
	}

	_, err := l.messenger.SendMessage(ctx, port.OutgoingMessage{
		ChatID:   u.Chat,
		Text:     text,
		Keyboard: keyboard,
	})
	return err
}

func (l *Learn) localizer(ctx context.Context) (port.Localizer, error) {
	localizer, ok := LocalizerFrom(ctx)
	if !ok {
		return nil, errors.New("нет локализатора: middleware локализации не подключён")
	}
	return localizer, nil
}

// cardText рисует вопрос: слово и, если он есть, способ его прочесть.
func cardText(localizer port.Localizer, item *session.Item) (string, error) {
	if item.Lexeme.Reading != "" {
		return localizer.T("learn.card_with_reading", port.Args{
			"Term":    item.Lexeme.Term,
			"Reading": item.Lexeme.Reading,
		})
	}
	return localizer.T("learn.card", port.Args{"Term": item.Lexeme.Term})
}

func translationTexts(translations []lexicon.Translation) []string {
	out := make([]string, 0, len(translations))
	for _, t := range translations {
		out = append(out, t.Text)
	}
	return out
}

// parseRating превращает разбор оценки в ответ «да или нет»: хендлеру
// не нужна причина, по которой кнопка не годится.
func parseRating(name string) (study.Rating, bool) {
	rating, err := study.ParseRating(name)
	return rating, err == nil
}

func firstActive(courses []study.Course) (study.Course, bool) {
	for i := range courses {
		if courses[i].IsActive() {
			return courses[i], true
		}
	}
	return study.Course{}, false
}
