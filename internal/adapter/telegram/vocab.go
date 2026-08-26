package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/internal/usecase/vocab"
)

// Шаги диалога добавления слова.
const (
	stateAddTerm        = "add:term"
	stateAddTranslation = "add:translation"
	stateAddReading     = "add:reading"
	stateAddExample     = "add:example"
)

// actionAddSkip — кнопка «пропустить» на необязательных шагах.
const actionAddSkip = "askip"

// addState — что человек успел ввести.
//
// Хранится весь ввод целиком, а не только текущий шаг: слово записывается
// в самом конце, одним вызовом сценария, и до тех пор ничего в базе нет.
// Поэтому /cancel посреди диалога и не оставляет следов — оставлять нечего.
type addState struct {
	Term         string   `json:"term"`
	Translations []string `json:"tr,omitempty"`
	Reading      string   `json:"reading,omitempty"`
}

// Vocab — диалог /add: добавление своего слова.
type Vocab struct {
	service   *vocab.Service
	dialogs   *Dialogs
	messenger port.Messenger
}

// NewVocab создаёт хендлер личного словаря.
func NewVocab(service *vocab.Service, dialogs *Dialogs, messenger port.Messenger) (*Vocab, error) {
	switch {
	case service == nil:
		return nil, errors.New("личному словарю нужен сценарий")
	case dialogs == nil:
		return nil, errors.New("личному словарю нужен движок диалогов")
	case messenger == nil:
		return nil, errors.New("личному словарю нужен мессенджер")
	}

	v := &Vocab{service: service, dialogs: dialogs, messenger: messenger}
	dialogs.Register(stateAddTerm, StepFunc(v.stepTerm))
	dialogs.Register(stateAddTranslation, StepFunc(v.stepTranslation))
	dialogs.Register(stateAddReading, StepFunc(v.stepReading))
	dialogs.Register(stateAddExample, StepFunc(v.stepExample))
	return v, nil
}

// Register привязывает команду к роутеру.
func (v *Vocab) Register(router *Router) {
	router.Command("add", port.UpdateHandlerFunc(v.start))
}

// start начинает диалог.
//
// Слово можно передать сразу — «/add 냉장고», — и тогда первый шаг
// пропускается: человек уже ответил на вопрос, который ему собирались задать.
func (v *Vocab) start(ctx context.Context, u *port.Update) error {
	term := strings.TrimSpace(u.Args)
	if term == "" {
		if err := v.ask(ctx, u, "add.ask_term", false); err != nil {
			return err
		}
		return v.dialogs.Start(ctx, stateAddTerm, addState{})
	}

	if err := v.ask(ctx, u, "add.ask_translation", false); err != nil {
		return err
	}
	return v.dialogs.Start(ctx, stateAddTranslation, addState{Term: term})
}

// stepTerm принимает слово.
func (v *Vocab) stepTerm(ctx context.Context, u *port.Update, payload json.RawMessage) (StepResult, error) {
	state, text, ok, err := v.input(u, payload)
	if err != nil || !ok {
		return Stay(state), err
	}

	state.Term = text
	if err := v.ask(ctx, u, "add.ask_translation", false); err != nil {
		return StepResult{}, err
	}
	return Next(stateAddTranslation, state), nil
}

// stepTranslation принимает переводы.
func (v *Vocab) stepTranslation(ctx context.Context, u *port.Update, payload json.RawMessage) (StepResult, error) {
	state, text, ok, err := v.input(u, payload)
	if err != nil || !ok {
		return Stay(state), err
	}

	state.Translations = splitTranslations(text)
	if len(state.Translations) == 0 {
		// Строка из одних разделителей: переспрашиваем, а не сохраняем пустоту.
		return Stay(state), v.ask(ctx, u, "add.ask_translation", false)
	}

	if err := v.ask(ctx, u, "add.ask_reading", true); err != nil {
		return StepResult{}, err
	}
	return Next(stateAddReading, state), nil
}

// stepReading принимает чтение или пропуск.
func (v *Vocab) stepReading(ctx context.Context, u *port.Update, payload json.RawMessage) (StepResult, error) {
	state, text, ok, err := v.input(u, payload)
	if err != nil {
		return StepResult{}, err
	}
	if !ok && !skipped(u) {
		return Stay(state), nil
	}

	state.Reading = text
	if err := v.ask(ctx, u, "add.ask_example", true); err != nil {
		return StepResult{}, err
	}
	return Next(stateAddExample, state), nil
}

// stepExample принимает пример или пропуск и сохраняет слово.
func (v *Vocab) stepExample(ctx context.Context, u *port.Update, payload json.RawMessage) (StepResult, error) {
	state, text, ok, err := v.input(u, payload)
	if err != nil {
		return StepResult{}, err
	}
	if !ok && !skipped(u) {
		return Stay(state), nil
	}

	return Done(), v.save(ctx, u, &state, text)
}

// save отдаёт собранное слово сценарию и отвечает человеку.
func (v *Vocab) save(ctx context.Context, u *port.Update, state *addState, example string) error {
	known, ok := UserFrom(ctx)
	if !ok {
		return errors.New("/add без пользователя")
	}

	added, err := v.service.Add(ctx, known.ID, &vocab.Word{
		Term:         state.Term,
		Translations: state.Translations,
		Reading:      state.Reading,
		Example:      example,
	})
	switch {
	case errors.Is(err, vocab.ErrNoCourse):
		// Учить нечего: языки брать неоткуда, и слово девать некуда.
		return Reply(v.messenger, "add.no_course").Handle(ctx, u)
	case errors.Is(err, lexicon.ErrRequired), errors.Is(err, lexicon.ErrTooLong), errors.Is(err, lexicon.ErrInvalid):
		// Слово или перевод не прошли проверку — это ввод человека,
		// а не поломка бота.
		return Reply(v.messenger, "add.invalid").Handle(ctx, u)
	case err != nil:
		return err
	}

	localizer, err := v.localizer(ctx)
	if err != nil {
		return err
	}

	text, err := localizer.T(addedKey(added.Outcome), port.Args{
		"Term":        added.Lexeme.Term,
		"Translation": strings.Join(state.Translations, ", "),
	})
	if err != nil {
		return err
	}
	_, err = v.messenger.SendMessage(ctx, port.OutgoingMessage{ChatID: u.Chat, Text: text})
	return err
}

// addedKey подбирает ответ под исход добавления.
func addedKey(outcome vocab.Outcome) string {
	switch outcome {
	case vocab.OutcomeAlreadyPersonal:
		return "add.already"
	case vocab.OutcomeInCourse:
		return "add.in_course"
	default:
		return "add.saved"
	}
}

// input достаёт состояние диалога и текст сообщения.
//
// Пустой текст — это нажатие кнопки или пустое сообщение; шаг решает сам,
// считать это ответом или продолжать ждать.
func (v *Vocab) input(u *port.Update, payload json.RawMessage) (state addState, text string, filled bool, err error) {
	if err := UnmarshalPayload(payload, &state); err != nil {
		return addState{}, "", false, err
	}

	text = strings.TrimSpace(u.Text)
	return state, text, text != "", nil
}

// skipped сообщает, что человек нажал «пропустить».
func skipped(u *port.Update) bool {
	if u.Callback == nil {
		return false
	}
	callback, ok := decodeCallback(u.Callback.Data)
	return ok && callback.Action == actionAddSkip
}

// ask задаёт вопрос шага, при надобности с кнопкой «пропустить».
func (v *Vocab) ask(ctx context.Context, u *port.Update, key string, optional bool) error {
	localizer, err := v.localizer(ctx)
	if err != nil {
		return err
	}

	text, err := localizer.T(key, nil)
	if err != nil {
		return err
	}

	var keyboard *port.Keyboard
	if optional {
		keyboard, err = NewKeyboard().Row(Button(
			mustText(localizer, "add.skip"), Callback{Action: actionAddSkip},
		)).Build()
		if err != nil {
			return err
		}
	}

	_, err = v.messenger.SendMessage(ctx, port.OutgoingMessage{
		ChatID: u.Chat, Text: text, Keyboard: keyboard,
	})
	return err
}

// splitTranslations разбирает строку переводов.
//
// Разделители — точка с запятой и косая черта, те же, что в словарных
// файлах. Запятая разделителем не служит намеренно: она чаще стоит внутри
// значения («то, что нужно»), и делить по ней значило бы резать переводы
// пополам.
func splitTranslations(text string) []string {
	parts := strings.FieldsFunc(text, func(r rune) bool { return r == ';' || r == '/' })

	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func (v *Vocab) localizer(ctx context.Context) (port.Localizer, error) {
	localizer, ok := LocalizerFrom(ctx)
	if !ok {
		return nil, errors.New("нет локализатора: middleware локализации не подключён")
	}
	return localizer, nil
}
