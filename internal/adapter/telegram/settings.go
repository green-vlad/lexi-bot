package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/internal/usecase/settings"
)

// Действия кнопок настроек.
const (
	actionSettings     = "st"
	actionSettingsEdit = "ste"
	actionSettingsSet  = "sts"
)

// Поля настроек — они же параметры кнопок «изменить».
const (
	fieldNewPerDay    = "new"
	fieldReviewCap    = "rev"
	fieldModes        = "mode"
	fieldDirection    = "dir"
	fieldReminder     = "rem"
	fieldTimezone     = "tz"
	fieldNewPerDayAsk = "newask"
	fieldReminderAsk  = "remask"
)

// Шаги диалога: значения, которые проще напечатать, чем выбрать кнопкой.
const (
	stateSettingsNewPerDay = "settings:new_per_day"
	stateSettingsReminder  = "settings:reminder"
	stateSettingsTimezone  = "settings:timezone"
)

// Готовые значения. Кнопками закрыты частые случаи, а редкие набираются
// руками: восемь кнопок с числами — это выбор, тридцать — это список.
var (
	newPerDayPresets = []int{3, 5, 10, 20}
	reviewCapPresets = []int{50, 100, 200, 500}
	reminderPresets  = []string{"09:00", "13:00", "19:00", "21:30"}
)

// Settings — экран /settings.
//
// Экран один и правится на месте: настроек шесть, и каждая правка,
// уходящая новым сообщением, превращала бы чат в ленту из полудюжины
// почти одинаковых списков.
type Settings struct {
	service   *settings.Service
	dialogs   *Dialogs
	messenger port.Messenger
}

// NewSettings создаёт хендлер настроек.
func NewSettings(service *settings.Service, dialogs *Dialogs, messenger port.Messenger) (*Settings, error) {
	switch {
	case service == nil:
		return nil, errors.New("настройкам нужен сценарий")
	case dialogs == nil:
		return nil, errors.New("настройкам нужен движок диалогов")
	case messenger == nil:
		return nil, errors.New("настройкам нужен мессенджер")
	}

	s := &Settings{service: service, dialogs: dialogs, messenger: messenger}
	dialogs.Register(stateSettingsNewPerDay, StepFunc(s.typedNewPerDay))
	dialogs.Register(stateSettingsReminder, StepFunc(s.typedReminder))
	dialogs.Register(stateSettingsTimezone, StepFunc(s.typedTimezone))
	return s, nil
}

// Register привязывает команду и кнопки к роутеру.
func (s *Settings) Register(router *Router) {
	router.Command("settings", port.UpdateHandlerFunc(s.start))
	router.CallbackAction(actionSettings, port.UpdateHandlerFunc(s.back))
	router.CallbackAction(actionSettingsEdit, port.UpdateHandlerFunc(s.edit))
	router.CallbackAction(actionSettingsSet, port.UpdateHandlerFunc(s.set))
}

// settingsState — сообщение, которое диалог правит после ввода.
type settingsState struct {
	MessageID int `json:"message,omitempty"`
}

// start показывает настройки по команде.
func (s *Settings) start(ctx context.Context, u *port.Update) error {
	return s.show(ctx, u, 0)
}

// back возвращает к списку настроек из редактора одного поля.
func (s *Settings) back(ctx context.Context, u *port.Update) error {
	return s.show(ctx, u, u.Callback.MessageID)
}

// show рисует список настроек.
func (s *Settings) show(ctx context.Context, u *port.Update, messageID port.MessageID) error {
	known, localizer, err := s.context(ctx)
	if err != nil {
		return err
	}

	current, err := s.service.Get(ctx, known.ID)
	if err != nil {
		return err
	}

	text, keyboard, err := settingsScreen(localizer, &current)
	if err != nil {
		return err
	}
	_, err = s.render(ctx, u, messageID, text, keyboard)
	return err
}

// settingsScreen собирает список настроек и кнопки к ним.
func settingsScreen(localizer port.Localizer, current *user.Settings) (string, *port.Keyboard, error) {
	lines := []string{mustText(localizer, "settings.title"), ""}

	rows := []struct {
		key   string
		field string
		args  port.Args
	}{
		{"settings.new_per_day", fieldNewPerDay, port.Args{"Count": current.NewPerDay}},
		{"settings.review_cap", fieldReviewCap, port.Args{"Count": current.MaxReviewsPerDay}},
		{"settings.modes", fieldModes, port.Args{"Modes": modeNames(localizer, current.QuizModes)}},
		{"settings.direction", fieldDirection, port.Args{"Direction": directionName(localizer, current)}},
		{"settings.reminder", fieldReminder, port.Args{"Time": reminderName(localizer, current)}},
		{"settings.timezone", fieldTimezone, port.Args{"Timezone": current.Timezone.String()}},
	}

	keyboard := NewKeyboard()
	for _, row := range rows {
		line, err := localizer.T(row.key, row.args)
		if err != nil {
			return "", nil, err
		}
		lines = append(lines, line)
		keyboard.Row(Button(line, Callback{Action: actionSettingsEdit, Param: row.field}))
	}

	built, err := keyboard.Build()
	if err != nil {
		return "", nil, err
	}
	return strings.Join(lines, "\n"), built, nil
}

// edit открывает редактор одного поля.
func (s *Settings) edit(ctx context.Context, u *port.Update) error {
	callback, ok := decodeCallback(u.Callback.Data)
	if !ok {
		return nil
	}

	known, localizer, err := s.context(ctx)
	if err != nil {
		return err
	}

	current, err := s.service.Get(ctx, known.ID)
	if err != nil {
		return err
	}

	switch callback.Param {
	case fieldNewPerDay:
		return s.choose(ctx, u, localizer, "settings.ask_new_per_day",
			numberButtons(fieldNewPerDay, newPerDayPresets, current.NewPerDay),
			Button(mustText(localizer, "settings.custom"),
				Callback{Action: actionSettingsEdit, Param: fieldNewPerDayAsk}))

	case fieldReviewCap:
		return s.choose(ctx, u, localizer, "settings.ask_review_cap",
			numberButtons(fieldReviewCap, reviewCapPresets, current.MaxReviewsPerDay))

	case fieldModes:
		return s.choose(ctx, u, localizer, "settings.ask_modes", modeButtons(localizer, &current))

	case fieldDirection:
		return s.choose(ctx, u, localizer, "settings.ask_direction", directionButtons(localizer, &current))

	case fieldReminder:
		return s.choose(ctx, u, localizer, "settings.ask_reminder",
			reminderButtons(current),
			Button(mustText(localizer, "settings.reminder_off"),
				Callback{Action: actionSettingsSet, Param: fieldReminder + ":off"}),
			Button(mustText(localizer, "settings.custom"),
				Callback{Action: actionSettingsEdit, Param: fieldReminderAsk}))

	case fieldNewPerDayAsk:
		return s.ask(ctx, u, "settings.type_new_per_day", stateSettingsNewPerDay)
	case fieldReminderAsk:
		return s.ask(ctx, u, "settings.type_reminder", stateSettingsReminder)
	case fieldTimezone:
		return s.ask(ctx, u, "settings.type_timezone", stateSettingsTimezone)
	}
	return nil
}

// set применяет выбранное кнопкой значение.
func (s *Settings) set(ctx context.Context, u *port.Update) error {
	callback, ok := decodeCallback(u.Callback.Data)
	if !ok {
		return nil
	}
	field, value, _ := strings.Cut(callback.Param, ":")

	known, _, err := s.context(ctx)
	if err != nil {
		return err
	}

	switch field {
	case fieldNewPerDay:
		n, valid := parseNumber(value)
		if !valid {
			return nil
		}
		if _, err := s.service.SetNewPerDay(ctx, known.ID, n); err != nil {
			return s.refuse(ctx, u, err)
		}

	case fieldReviewCap:
		n, valid := parseNumber(value)
		if !valid {
			return nil
		}
		if _, err := s.service.SetMaxReviewsPerDay(ctx, known.ID, n); err != nil {
			return s.refuse(ctx, u, err)
		}

	case fieldModes:
		mode, valid := parseMode(value)
		if !valid {
			return nil
		}
		_, ok, err := s.service.ToggleMode(ctx, known.ID, mode)
		if err != nil {
			return err
		}
		if !ok {
			// Последний включённый режим выключить нельзя: карточку
			// станет нечем показать.
			return Reply(s.messenger, "settings.last_mode").Handle(ctx, u)
		}

	case fieldDirection:
		if _, err := s.service.ToggleDirection(ctx, known.ID); err != nil {
			return err
		}

	case fieldReminder:
		at := user.TimeOfDay{}
		if value != "off" {
			parsed, valid := parseTimeOfDay(value)
			if !valid {
				return nil
			}
			at = parsed
		}
		if _, err := s.service.SetReminderAt(ctx, known.ID, at); err != nil {
			return s.refuse(ctx, u, err)
		}
	}

	// Правка применена — показываем список целиком, чтобы человек увидел
	// новое значение на своём месте, а не поверил на слово.
	return s.show(ctx, u, u.Callback.MessageID)
}

// choose рисует редактор поля с кнопками и возвратом к списку.
func (s *Settings) choose(ctx context.Context, u *port.Update, localizer port.Localizer,
	key string, buttons []KeyboardButton, extra ...KeyboardButton,
) error {
	text, err := localizer.T(key, nil)
	if err != nil {
		return err
	}

	keyboard := NewKeyboard().Grid(2, buttons...)
	for _, button := range extra {
		keyboard.Row(button)
	}
	keyboard.Row(Button(mustText(localizer, "settings.back"), Callback{Action: actionSettings}))

	built, err := keyboard.Build()
	if err != nil {
		return err
	}
	_, err = s.render(ctx, u, u.Callback.MessageID, text, built)
	return err
}

// ask переводит бота в ожидание напечатанного значения.
func (s *Settings) ask(ctx context.Context, u *port.Update, key, state string) error {
	_, localizer, err := s.context(ctx)
	if err != nil {
		return err
	}

	text, err := localizer.T(key, nil)
	if err != nil {
		return err
	}

	keyboard, err := NewKeyboard().Row(Button(
		mustText(localizer, "settings.back"), Callback{Action: actionSettings},
	)).Build()
	if err != nil {
		return err
	}

	sent, err := s.render(ctx, u, u.Callback.MessageID, text, keyboard)
	if err != nil {
		return err
	}
	return s.dialogs.Start(ctx, state, settingsState{MessageID: int(sent)})
}

// typedNewPerDay принимает напечатанную норму новых слов.
func (s *Settings) typedNewPerDay(ctx context.Context, u *port.Update, payload json.RawMessage) (StepResult, error) {
	return s.typed(ctx, u, payload, func(known *user.User, text string) error {
		n, err := strconv.Atoi(text)
		if err != nil {
			return errNotANumber
		}
		_, err = s.service.SetNewPerDay(ctx, known.ID, n)
		return err
	})
}

// typedReminder принимает напечатанное время напоминания.
func (s *Settings) typedReminder(ctx context.Context, u *port.Update, payload json.RawMessage) (StepResult, error) {
	return s.typed(ctx, u, payload, func(known *user.User, text string) error {
		at, err := user.ParseTimeOfDay(text)
		if err != nil {
			return err
		}
		_, err = s.service.SetReminderAt(ctx, known.ID, at)
		return err
	})
}

// typedTimezone принимает напечатанную таймзону.
func (s *Settings) typedTimezone(ctx context.Context, u *port.Update, payload json.RawMessage) (StepResult, error) {
	return s.typed(ctx, u, payload, func(known *user.User, text string) error {
		tz, err := user.ParseTimezone(text)
		if err != nil {
			return err
		}
		_, err = s.service.SetTimezone(ctx, known.ID, tz)
		return err
	})
}

// errNotANumber — то, что человек напечатал, числом не является.
var errNotANumber = errors.New("это не число")

// typed — общая часть шагов ввода: разобрать состояние, применить значение
// и вернуться к списку настроек.
//
// Непонятое значение оставляет диалог на месте: сбрасывать его и заставлять
// человека начинать заново из-за опечатки было бы наказанием.
func (s *Settings) typed(ctx context.Context, u *port.Update, payload json.RawMessage,
	apply func(known *user.User, text string) error,
) (StepResult, error) {
	var state settingsState
	if err := UnmarshalPayload(payload, &state); err != nil {
		return StepResult{}, err
	}

	text := strings.TrimSpace(u.Text)
	if text == "" {
		return Stay(state), nil
	}

	known, ok := UserFrom(ctx)
	if !ok {
		return StepResult{}, errors.New("настройки без пользователя")
	}

	if err := apply(&known, text); err != nil {
		if !isUserInput(err) {
			return StepResult{}, err
		}
		return Stay(state), Reply(s.messenger, "settings.bad_value").Handle(ctx, u)
	}
	return Done(), s.show(ctx, u, port.MessageID(state.MessageID))
}

// isUserInput отличает придирку к введённому от поломки бота.
func isUserInput(err error) bool {
	return errors.Is(err, errNotANumber) ||
		errors.Is(err, user.ErrOutOfRange) ||
		errors.Is(err, user.ErrInvalid) ||
		errors.Is(err, user.ErrRequired)
}

// refuse объясняет, почему значение не подошло.
func (s *Settings) refuse(ctx context.Context, u *port.Update, err error) error {
	if !isUserInput(err) {
		return err
	}
	return Reply(s.messenger, "settings.bad_value").Handle(ctx, u)
}

// parseNumber, parseMode и parseTimeOfDay превращают разбор значения кнопки
// в ответ «да или нет».
//
// Причина отказа хендлеру не нужна: непонятое значение означает кнопку
// от прошлой версии бота, и делать по ней нечего — ни отвечать человеку,
// ни писать в лог. Ошибку здесь было бы некому прочитать.
func parseNumber(value string) (int, bool) {
	n, err := strconv.Atoi(value)
	return n, err == nil
}

func parseMode(value string) (study.Mode, bool) {
	mode, err := study.ParseMode(value)
	return mode, err == nil
}

func parseTimeOfDay(value string) (user.TimeOfDay, bool) {
	at, err := user.ParseTimeOfDay(value)
	return at, err == nil
}

// numberButtons собирает кнопки с готовыми числами, отмечая текущее.
func numberButtons(field string, presets []int, current int) []KeyboardButton {
	buttons := make([]KeyboardButton, 0, len(presets))
	for _, n := range presets {
		label := strconv.Itoa(n)
		if n == current {
			label = "• " + label
		}
		buttons = append(buttons, Button(label, Callback{
			Action: actionSettingsSet, Param: field + ":" + strconv.Itoa(n),
		}))
	}
	return buttons
}

// modeButtons собирает переключатели режимов проверки.
func modeButtons(localizer port.Localizer, current *user.Settings) []KeyboardButton {
	buttons := make([]KeyboardButton, 0, len(study.Modes()))
	for _, mode := range study.Modes() {
		label := modeName(localizer, mode)
		if current.ModeEnabled(mode) {
			label = "✓ " + label
		}
		buttons = append(buttons, Button(label, Callback{
			Action: actionSettingsSet, Param: fieldModes + ":" + mode.String(),
		}))
	}
	return buttons
}

// directionButtons собирает переключатель направления.
func directionButtons(localizer port.Localizer, current *user.Settings) []KeyboardButton {
	label := mustText(localizer, "settings.direction_recognize")
	if !current.ReverseDirection {
		label = mustText(localizer, "settings.direction_produce")
	}
	return []KeyboardButton{Button(label, Callback{
		Action: actionSettingsSet, Param: fieldDirection,
	})}
}

// reminderButtons собирает кнопки с готовым временем напоминания.
func reminderButtons(current user.Settings) []KeyboardButton {
	buttons := make([]KeyboardButton, 0, len(reminderPresets))
	for _, at := range reminderPresets {
		label := at
		if current.RemindersEnabled() && current.ReminderAt.String() == at {
			label = "• " + at
		}
		buttons = append(buttons, Button(label, Callback{
			Action: actionSettingsSet, Param: fieldReminder + ":" + at,
		}))
	}
	return buttons
}

// modeNames перечисляет включённые режимы через запятую.
func modeNames(localizer port.Localizer, modes []study.Mode) string {
	names := make([]string, 0, len(modes))
	for _, mode := range modes {
		names = append(names, modeName(localizer, mode))
	}
	return strings.Join(names, ", ")
}

func modeName(localizer port.Localizer, mode study.Mode) string {
	return mustText(localizer, "settings.mode_"+mode.String())
}

// directionName описывает направление словами, а не кодом.
func directionName(localizer port.Localizer, current *user.Settings) string {
	if current.ReverseDirection {
		return mustText(localizer, "settings.direction_produce")
	}
	return mustText(localizer, "settings.direction_recognize")
}

// reminderName показывает время напоминания или «выключено».
func reminderName(localizer port.Localizer, current *user.Settings) string {
	if !current.RemindersEnabled() {
		return mustText(localizer, "settings.reminder_off")
	}
	return current.ReminderAt.String()
}

// render правит сообщение, если оно известно, и отправляет новое, если нет.
func (s *Settings) render(ctx context.Context, u *port.Update, messageID port.MessageID,
	text string, keyboard *port.Keyboard,
) (port.MessageID, error) {
	if messageID != 0 {
		return messageID, s.messenger.EditMessage(ctx, port.MessageEdit{
			ChatID: u.Chat, MessageID: messageID, Text: text, Keyboard: keyboard,
		})
	}
	return s.messenger.SendMessage(ctx, port.OutgoingMessage{
		ChatID: u.Chat, Text: text, Keyboard: keyboard,
	})
}

// context достаёт пользователя и локализатор — без них экран не собрать.
func (s *Settings) context(ctx context.Context) (user.User, port.Localizer, error) {
	known, ok := UserFrom(ctx)
	if !ok {
		return user.User{}, nil, errors.New("настройки без пользователя")
	}
	localizer, ok := LocalizerFrom(ctx)
	if !ok {
		return user.User{}, nil, errors.New("нет локализатора: middleware локализации не подключён")
	}
	return known, localizer, nil
}
