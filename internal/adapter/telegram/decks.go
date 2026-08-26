package telegram

import (
	"context"
	"errors"
	"strings"

	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/courses"
	"lexi-bot/internal/usecase/port"
)

// Действия кнопок в списке курсов.
const (
	actionCourseLearn   = "cl"
	actionCoursePause   = "cp"
	actionCourseResume  = "cr"
	actionCourseArchive = "ca"
	actionCourseAdd     = "cadd"
)

// Decks — команда /decks: список курсов и что с ними можно сделать.
type Decks struct {
	courses   *courses.Service
	messenger port.Messenger
}

// NewDecks создаёт хендлер курсов.
func NewDecks(service *courses.Service, messenger port.Messenger) (*Decks, error) {
	switch {
	case service == nil:
		return nil, errors.New("курсам нужен сценарий")
	case messenger == nil:
		return nil, errors.New("курсам нужен мессенджер")
	}
	return &Decks{courses: service, messenger: messenger}, nil
}

// Register привязывает команду и кнопки к роутеру.
func (d *Decks) Register(router *Router) {
	router.Command("decks", port.UpdateHandlerFunc(d.list))
	router.CallbackAction(actionCourseLearn, port.UpdateHandlerFunc(d.learn))
	router.CallbackAction(actionCoursePause, d.status(study.CoursePaused, "decks.paused"))
	router.CallbackAction(actionCourseResume, d.status(study.CourseActive, "decks.switched"))
	router.CallbackAction(actionCourseArchive, d.status(study.CourseArchived, "decks.archived"))
}

// list показывает курсы пользователя.
func (d *Decks) list(ctx context.Context, u *port.Update) error {
	known, ok := UserFrom(ctx)
	if !ok {
		return errors.New("/decks без пользователя")
	}

	summaries, err := d.courses.List(ctx, known.ID)
	if err != nil {
		return err
	}

	localizer, err := d.localizer(ctx)
	if err != nil {
		return err
	}

	if len(summaries) == 0 {
		return Reply(d.messenger, "decks.empty").Handle(ctx, u)
	}

	text, keyboard, err := d.render(localizer, summaries)
	if err != nil {
		return err
	}

	messageID := port.MessageID(0)
	if u.Callback != nil {
		// Пришли из кнопки — правим тот же список, а не плодим новые.
		messageID = u.Callback.MessageID
	}
	return d.show(ctx, u, messageID, text, keyboard)
}

// render собирает список и кнопки к нему.
//
// Архивные курсы показываются строкой, но без кнопок: они убраны с глаз
// намеренно, и «продолжить» рядом с ними вернуло бы их в оборот случайным
// нажатием. Вернуть архивный курс можно, заведя его заново — карточки
// и прогресс при этом на месте.
func (d *Decks) render(localizer port.Localizer, summaries []courses.Summary) (string, *port.Keyboard, error) {
	lines := make([]string, 0, len(summaries)+1)
	lines = append(lines, mustText(localizer, "decks.title"), "")

	keyboard := NewKeyboard()
	for i := range summaries {
		summary := &summaries[i]
		args := port.Args{
			"Deck":    deckTitle(localizer, &summary.Deck),
			"Learned": summary.Learned,
			"Total":   summary.Total,
		}

		line, err := localizer.T(lineKey(summary), args)
		if err != nil {
			return "", nil, err
		}
		lines = append(lines, line)

		for _, button := range courseButtons(localizer, summary, args) {
			keyboard.Row(button)
		}
	}

	keyboard.Row(Button(mustText(localizer, "decks.add"), Callback{Action: actionCourseAdd}))

	built, err := keyboard.Build()
	if err != nil {
		return "", nil, err
	}
	return strings.Join(lines, "\n"), built, nil
}

// lineKey выбирает, как показать строку курса.
func lineKey(summary *courses.Summary) string {
	switch {
	case summary.Course.Status == study.CourseArchived:
		return "decks.line_archived"
	case summary.Course.Status == study.CoursePaused:
		return "decks.line_paused"
	case summary.Current:
		return "decks.line_current"
	default:
		return "decks.line_active"
	}
}

// courseButtons собирает кнопки одного курса.
func courseButtons(localizer port.Localizer, summary *courses.Summary, args port.Args) []KeyboardButton {
	id := int64(summary.Course.ID)

	switch summary.Course.Status {
	case study.CourseArchived:
		return nil
	case study.CoursePaused:
		return []KeyboardButton{
			button(localizer, "decks.resume", args, Callback{Action: actionCourseResume, ID: id}),
		}
	default:
		buttons := make([]KeyboardButton, 0, 3)
		if !summary.Current {
			// Текущему курсу кнопка «учить» не нужна: он и так текущий.
			buttons = append(buttons,
				button(localizer, "decks.learn", args, Callback{Action: actionCourseLearn, ID: id}))
		}
		return append(buttons,
			button(localizer, "decks.pause", args, Callback{Action: actionCoursePause, ID: id}),
			button(localizer, "decks.archive", args, Callback{Action: actionCourseArchive, ID: id}),
		)
	}
}

// learn делает курс текущим.
func (d *Decks) learn(ctx context.Context, u *port.Update) error {
	return d.apply(ctx, u, func(ctx context.Context, userID user.ID, courseID study.CourseID) error {
		return d.courses.SetCurrent(ctx, userID, courseID)
	}, "decks.switched")
}

// status меняет состояние курса.
func (d *Decks) status(status study.CourseStatus, key string) port.UpdateHandler {
	return port.UpdateHandlerFunc(func(ctx context.Context, u *port.Update) error {
		if status == study.CourseActive {
			// «Продолжить» — это не просто снять паузу, а вернуться к курсу:
			// человек нажимает её, чтобы заниматься.
			return d.learn(ctx, u)
		}
		return d.apply(ctx, u, func(ctx context.Context, userID user.ID, courseID study.CourseID) error {
			return d.courses.SetStatus(ctx, userID, courseID, status)
		}, key)
	})
}

// apply выполняет действие над курсом и перерисовывает список.
func (d *Decks) apply(ctx context.Context, u *port.Update, action func(context.Context, user.ID, study.CourseID) error, key string) error {
	known, ok := UserFrom(ctx)
	if !ok {
		return errors.New("действие над курсом без пользователя")
	}

	callback, ok := decodeCallback(u.Callback.Data)
	if !ok {
		return nil
	}
	courseID := study.CourseID(callback.ID)

	if err := action(ctx, known.ID, courseID); err != nil {
		if errors.Is(err, port.ErrNotFound) {
			// Курса нет или он чужой: кнопка устарела либо подделана.
			return Reply(d.messenger, "learn.stale_button").Handle(ctx, u)
		}
		return err
	}

	// Сначала подтверждение отдельным сообщением, потом обновлённый список
	// на месте старого: так видно и что случилось, и как теперь обстоят дела.
	if err := d.confirm(ctx, u, courseID, key); err != nil {
		return err
	}
	return d.list(ctx, u)
}

// confirm сообщает, что действие выполнено.
func (d *Decks) confirm(ctx context.Context, u *port.Update, courseID study.CourseID, key string) error {
	known, _ := UserFrom(ctx)

	summaries, err := d.courses.List(ctx, known.ID)
	if err != nil {
		return err
	}

	localizer, err := d.localizer(ctx)
	if err != nil {
		return err
	}

	title := ""
	for i := range summaries {
		if summaries[i].Course.ID == courseID {
			title = deckTitle(localizer, &summaries[i].Deck)
		}
	}

	text, err := localizer.T(key, port.Args{"Deck": title})
	if err != nil {
		return err
	}
	_, err = d.messenger.SendMessage(ctx, port.OutgoingMessage{ChatID: u.Chat, Text: text})
	return err
}

func (d *Decks) show(ctx context.Context, u *port.Update, messageID port.MessageID, text string, keyboard *port.Keyboard) error {
	if messageID != 0 {
		return d.messenger.EditMessage(ctx, port.MessageEdit{
			ChatID:    u.Chat,
			MessageID: messageID,
			Text:      text,
			Keyboard:  keyboard,
		})
	}

	_, err := d.messenger.SendMessage(ctx, port.OutgoingMessage{
		ChatID:   u.Chat,
		Text:     text,
		Keyboard: keyboard,
	})
	return err
}

func (d *Decks) localizer(ctx context.Context) (port.Localizer, error) {
	localizer, ok := LocalizerFrom(ctx)
	if !ok {
		return nil, errors.New("нет локализатора: middleware локализации не подключён")
	}
	return localizer, nil
}

// button собирает кнопку с переведённой надписью.
func button(localizer port.Localizer, key string, args port.Args, callback Callback) KeyboardButton {
	text, err := localizer.T(key, args)
	if err != nil {
		text = key
	}
	return Button(text, callback)
}
