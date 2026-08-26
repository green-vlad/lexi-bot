package telegram

import (
	"context"
	"errors"

	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/usecase/courses"
	"lexi-bot/internal/usecase/intro"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/internal/usecase/session"
)

// Действия кнопок меню занятия.
const (
	actionMenu   = "menu"
	actionReview = "rev"
)

// Menu — экран, с которого начинается занятие.
//
// Занятий два, и они разной природы. Повторение спрашивает слова, которые
// человек уже начал учить: там он отвечает, и бот его проверяет. Знакомство
// показывает новые слова целиком и спрашивает не перевод, а решение. Мешать
// их в одну очередь значило бы спрашивать перевод слова, которого человек
// не видел, — поэтому выбор, чем заняться, отдан ему.
type Menu struct {
	session   *session.Service
	intro     *intro.Service
	courses   *courses.Service
	messenger port.Messenger
}

// NewMenu создаёт хендлер меню.
func NewMenu(sessions *session.Service, intros *intro.Service, courseService *courses.Service, messenger port.Messenger) (*Menu, error) {
	switch {
	case sessions == nil:
		return nil, errors.New("меню нужен сценарий сессии")
	case intros == nil:
		return nil, errors.New("меню нужен сценарий знакомства")
	case courseService == nil:
		return nil, errors.New("меню нужен сценарий курсов")
	case messenger == nil:
		return nil, errors.New("меню нужен мессенджер")
	}
	return &Menu{session: sessions, intro: intros, courses: courseService, messenger: messenger}, nil
}

// Register привязывает команду и кнопку возврата к роутеру.
func (m *Menu) Register(router *Router) {
	router.Command("learn", port.UpdateHandlerFunc(m.start))
	router.CallbackAction(actionMenu, port.UpdateHandlerFunc(m.back))
}

// start открывает меню по команде.
func (m *Menu) start(ctx context.Context, u *port.Update) error {
	known, ok := UserFrom(ctx)
	if !ok {
		return errors.New("/learn без пользователя")
	}

	// Какой курс учить — решает сценарий курсов: он знает и про выбор
	// пользователя, и про то, что выбранный курс мог уехать на паузу.
	course, ok, err := m.courses.Current(ctx, known.ID)
	if err != nil {
		return err
	}
	if !ok {
		// Учить нечего: человек ещё не выбрал курс или поставил всё на паузу.
		return Reply(m.messenger, "learn.no_course").Handle(ctx, u)
	}
	return m.Show(ctx, u, course.ID, 0)
}

// back возвращает в меню по кнопке из конца занятия.
func (m *Menu) back(ctx context.Context, u *port.Update) error {
	callback, ok := decodeCallback(u.Callback.Data)
	if !ok {
		return nil
	}
	return m.Show(ctx, u, study.CourseID(callback.ID), u.Callback.MessageID)
}

// Show рисует меню, правя сообщение messageID; при нулевом отправляет новое.
func (m *Menu) Show(ctx context.Context, u *port.Update, courseID study.CourseID, messageID port.MessageID) error {
	localizer, ok := LocalizerFrom(ctx)
	if !ok {
		return errors.New("нет локализатора: middleware локализации не подключён")
	}

	due, err := m.session.DueCount(ctx, courseID)
	if err != nil {
		return err
	}
	fresh, err := m.intro.Available(ctx, courseID)
	if err != nil {
		return err
	}

	text, keyboard, err := menuScreen(localizer, courseID, due, fresh)
	if err != nil {
		return err
	}

	if messageID != 0 {
		return m.messenger.EditMessage(ctx, port.MessageEdit{
			ChatID: u.Chat, MessageID: messageID, Text: text, Keyboard: keyboard,
		})
	}
	_, err = m.messenger.SendMessage(ctx, port.OutgoingMessage{
		ChatID: u.Chat, Text: text, Keyboard: keyboard,
	})
	return err
}

// menuScreen собирает текст и кнопки меню.
//
// Кнопки, за которыми ничего нет, не показываются: обещать «повторить»
// и тут же ответить «нечего повторять» — способ потратить время человека
// на нажатие, о результате которого уже известно.
func menuScreen(localizer port.Localizer, courseID study.CourseID, due, fresh int) (string, *port.Keyboard, error) {
	if due == 0 && fresh == 0 {
		text, err := localizer.T("menu.nothing", nil)
		return text, nil, err
	}

	text, err := localizer.T("menu.title", nil)
	if err != nil {
		return "", nil, err
	}

	keyboard := NewKeyboard()
	if due > 0 {
		label, err := localizer.Plural("menu.review", due, port.Args{"Count": due})
		if err != nil {
			return "", nil, err
		}
		keyboard = keyboard.Row(Button(label, Callback{Action: actionReview, ID: int64(courseID)}))
	}
	if fresh > 0 {
		label, err := localizer.Plural("menu.new_words", fresh, port.Args{"Count": fresh})
		if err != nil {
			return "", nil, err
		}
		keyboard = keyboard.Row(Button(label, Callback{Action: actionNewWord, ID: int64(courseID)}))
	}

	built, err := keyboard.Build()
	if err != nil {
		return "", nil, err
	}
	return text, built, nil
}
