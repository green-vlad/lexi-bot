package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/onboarding"
	"lexi-bot/internal/usecase/port"
)

// Шаги диалога онбординга.
const (
	stateUILang      = "onboarding:ui_lang"
	stateLearning    = "onboarding:learning"
	stateDeck        = "onboarding:deck"
	stateTranslation = "onboarding:translation"
)

// Действия кнопок. Названия короткие: на callback_data отведено 64 байта,
// и тратить их на слова вроде «onboarding_choose_language» жалко.
const (
	actionUILang      = "obui"
	actionLearning    = "oblang"
	actionDeck        = "obdeck"
	actionTranslation = "obtr"
	actionBack        = "obback"
)

// Onboarding — хендлер /start и шаги знакомства.
//
// Диалог правит одно и то же сообщение вместо отправки новых: человек видит
// один экран, который меняется, а не простыню из четырёх вопросов, среди
// которых непонятно, какой актуален.
type Onboarding struct {
	service   *onboarding.Service
	dialogs   *Dialogs
	messenger port.Messenger
	catalog   port.Catalog
}

// NewOnboarding создаёт хендлер знакомства.
func NewOnboarding(service *onboarding.Service, dialogs *Dialogs, messenger port.Messenger, catalog port.Catalog) (*Onboarding, error) {
	switch {
	case service == nil:
		return nil, errors.New("хендлеру знакомства нужен сценарий")
	case dialogs == nil:
		return nil, errors.New("хендлеру знакомства нужен движок диалогов")
	case messenger == nil:
		return nil, errors.New("хендлеру знакомства нужен мессенджер")
	case catalog == nil:
		return nil, errors.New("хендлеру знакомства нужен каталог переводов")
	}

	o := &Onboarding{service: service, dialogs: dialogs, messenger: messenger, catalog: catalog}
	dialogs.Register(stateUILang, StepFunc(o.stepUILang))
	dialogs.Register(stateLearning, StepFunc(o.stepLearning))
	dialogs.Register(stateDeck, StepFunc(o.stepDeck))
	dialogs.Register(stateTranslation, StepFunc(o.stepTranslation))
	return o, nil
}

// Register привязывает команду к роутеру.
func (o *Onboarding) Register(router *Router) {
	router.Command("start", port.UpdateHandlerFunc(o.start))
	// Добавление курса из /decks — тот же самый диалог: выбор языка,
	// колоды и языка перевода. Второй такой же городить незачем.
	router.CallbackAction(actionCourseAdd, port.UpdateHandlerFunc(o.addCourse))
}

// addCourse начинает выбор ещё одного курса.
func (o *Onboarding) addCourse(ctx context.Context, u *port.Update) error {
	state, err := o.showLearning(ctx, u, dialogState{})
	if err != nil || state.MessageID == 0 {
		return err
	}
	return o.dialogs.Start(ctx, stateLearning, state)
}

// dialogState — что пользователь успел выбрать.
//
// Хранится весь выбор целиком, а не только текущий шаг: тогда кнопка «назад»
// это просто смена состояния, а не откат с потерей всего остального.
type dialogState struct {
	Learning string `json:"learning,omitempty"`
	DeckID   int64  `json:"deck,omitempty"`
	// MessageID — сообщение, которое диалог правит на каждом шаге.
	MessageID int `json:"message,omitempty"`
}

// start обрабатывает /start.
func (o *Onboarding) start(ctx context.Context, u *port.Update) error {
	known, ok := UserFrom(ctx)
	if !ok {
		return errors.New("/start без пользователя")
	}

	started, err := o.service.HasCourses(ctx, known.ID)
	if err != nil {
		return err
	}
	if started {
		// Гонять по кнопкам заново того, кто уже учит, незачем: он вернулся
		// не знакомиться, а заниматься.
		return o.greetReturning(ctx, u, &known)
	}

	localizer, err := o.localizer(ctx)
	if err != nil {
		return err
	}

	greeting, err := localizer.T("start.greeting", port.Args{"Name": displayName(localizer, u, &known)})
	if err != nil {
		return err
	}
	if _, err := o.messenger.SendMessage(ctx, port.OutgoingMessage{ChatID: u.Chat, Text: greeting}); err != nil {
		return err
	}

	// Язык интерфейса спрашиваем только у тех, чей клиент говорит на языке,
	// которого мы не знаем: остальным он уже подобран.
	if _, matched := user.MatchUILang(u.Sender.LanguageCode); !matched {
		return o.askUILang(ctx, u)
	}

	state, err := o.showLearning(ctx, u, dialogState{})
	if err != nil || state.MessageID == 0 {
		// Экран не показался — объяснение пользователь уже получил,
		// и диалог начинать нечем.
		return err
	}
	return o.dialogs.Start(ctx, stateLearning, state)
}

// greetReturning отвечает тому, у кого курс уже есть.
func (o *Onboarding) greetReturning(ctx context.Context, u *port.Update, known *user.User) error {
	courses, err := o.service.Courses(ctx, known.ID)
	if err != nil {
		return err
	}

	title := ""
	if len(courses) > 0 {
		title = courses[0].Deck.Title
	}

	text, err := o.text(ctx, "onboarding.already_started", port.Args{"Deck": title})
	if err != nil {
		return err
	}
	_, err = o.messenger.SendMessage(ctx, port.OutgoingMessage{ChatID: u.Chat, Text: text})
	return err
}

// askUILang показывает выбор языка интерфейса.
func (o *Onboarding) askUILang(ctx context.Context, u *port.Update) error {
	buttons := make([]KeyboardButton, 0, len(user.SupportedUILangs()))
	for _, lang := range user.SupportedUILangs() {
		buttons = append(buttons, Button(langName(o.catalog.For(lang), lang.String()),
			Callback{Action: actionUILang, Param: lang.String()}))
	}

	keyboard, err := NewKeyboard().Grid(2, buttons...).Build()
	if err != nil {
		return err
	}

	messageID, err := o.ask(ctx, u, 0, "start.choose_ui_lang", keyboard)
	if err != nil {
		return err
	}
	return o.dialogs.Start(ctx, stateUILang, dialogState{MessageID: int(messageID)})
}

// showLearning рисует экран выбора языка изучения и возвращает обновлённое
// состояние диалога.
//
// Рисование отделено от сохранения намеренно: этот экран показывают и из
// команды /start, где диалога ещё нет, и из шагов, где состояние сохраняет
// сам движок. Если бы экран сохранял состояние сам, из шага получилось бы
// две записи подряд — и вторая, от движка, затёрла бы первую.
func (o *Onboarding) showLearning(ctx context.Context, u *port.Update, state dialogState) (dialogState, error) {
	langs, err := o.service.LearningLanguages(ctx)
	if err != nil {
		return state, o.explain(ctx, u, err)
	}

	localizer, err := o.localizer(ctx)
	if err != nil {
		return state, err
	}

	buttons := make([]KeyboardButton, 0, len(langs))
	for _, lang := range langs {
		buttons = append(buttons, Button(langName(localizer, lang.String()),
			Callback{Action: actionLearning, Param: lang.String()}))
	}

	keyboard, err := NewKeyboard().Grid(2, buttons...).Build()
	if err != nil {
		return state, err
	}

	messageID, err := o.ask(ctx, u, port.MessageID(state.MessageID), "onboarding.choose_learning", keyboard)
	if err != nil {
		return state, err
	}

	state.MessageID = int(messageID)
	state.Learning = ""
	state.DeckID = 0
	return state, nil
}

// askDeck показывает колоды выбранного языка.
func (o *Onboarding) askDeck(ctx context.Context, u *port.Update, state dialogState) (StepResult, error) {
	lang, err := lexicon.ParseLanguage(state.Learning)
	if err != nil {
		return StepResult{}, fmt.Errorf("%w: язык изучения %q не разбирается", ErrResetDialog, state.Learning)
	}

	decks, err := o.service.Decks(ctx, lang)
	if err != nil {
		return StepResult{}, o.explain(ctx, u, err)
	}

	localizer, err := o.localizer(ctx)
	if err != nil {
		return StepResult{}, err
	}

	buttons := make([]KeyboardButton, 0, len(decks))
	for _, deck := range decks {
		buttons = append(buttons, Button(deck.Title, Callback{Action: actionDeck, ID: int64(deck.ID)}))
	}

	keyboard, err := NewKeyboard().
		Grid(1, buttons...).
		Row(Button(mustText(localizer, "onboarding.back"), Callback{Action: actionBack, Param: stateLearning})).
		Build()
	if err != nil {
		return StepResult{}, err
	}

	messageID, err := o.ask(ctx, u, port.MessageID(state.MessageID), "onboarding.choose_deck", keyboard)
	if err != nil {
		return StepResult{}, err
	}

	state.MessageID = int(messageID)
	state.DeckID = 0
	return Next(stateDeck, state), nil
}

// askTranslation показывает языки перевода выбранной колоды.
func (o *Onboarding) askTranslation(ctx context.Context, u *port.Update, state dialogState) (StepResult, error) {
	langs, err := o.service.TranslationLanguages(ctx, lexicon.DeckID(state.DeckID))
	if err != nil {
		return StepResult{}, o.explain(ctx, u, err)
	}

	localizer, err := o.localizer(ctx)
	if err != nil {
		return StepResult{}, err
	}

	buttons := make([]KeyboardButton, 0, len(langs))
	for _, lang := range langs {
		buttons = append(buttons, Button(langName(localizer, lang.String()),
			Callback{Action: actionTranslation, Param: lang.String()}))
	}

	keyboard, err := NewKeyboard().
		Grid(2, buttons...).
		Row(Button(mustText(localizer, "onboarding.back"), Callback{Action: actionBack, Param: stateDeck})).
		Build()
	if err != nil {
		return StepResult{}, err
	}

	messageID, err := o.ask(ctx, u, port.MessageID(state.MessageID), "onboarding.choose_translation", keyboard)
	if err != nil {
		return StepResult{}, err
	}

	state.MessageID = int(messageID)
	return Next(stateTranslation, state), nil
}

// stepUILang принимает выбор языка интерфейса.
func (o *Onboarding) stepUILang(ctx context.Context, u *port.Update, payload json.RawMessage) (StepResult, error) {
	state, callback, ok, err := o.expect(ctx, u, payload, actionUILang)
	if err != nil || !ok {
		return Stay(state), err
	}

	known, _ := UserFrom(ctx)
	lang, ok := parseUILang(callback.Param)
	if !ok {
		// Кнопка от прошлой версии бота или подделка: переспрашиваем.
		return Stay(state), nil
	}
	if err := o.service.SetUILang(ctx, known.ID, lang); err != nil {
		return StepResult{}, err
	}

	// Дальше говорим уже на выбранном языке: локализатор в контексте
	// подобрали до того, как язык сменился.
	ctx = withLocalizer(ctx, o.catalog.For(lang))

	next, err := o.showLearning(ctx, u, state)
	if err != nil {
		return StepResult{}, err
	}
	return Next(stateLearning, next), nil
}

// stepLearning принимает выбор языка изучения.
func (o *Onboarding) stepLearning(ctx context.Context, u *port.Update, payload json.RawMessage) (StepResult, error) {
	state, callback, ok, err := o.expect(ctx, u, payload, actionLearning)
	if err != nil || !ok {
		return Stay(state), err
	}

	if _, ok := parseLanguage(callback.Param); !ok {
		return Stay(state), nil
	}
	state.Learning = callback.Param
	return o.askDeck(ctx, u, state)
}

// stepDeck принимает выбор колоды.
func (o *Onboarding) stepDeck(ctx context.Context, u *port.Update, payload json.RawMessage) (StepResult, error) {
	state, callback, ok, err := o.expect(ctx, u, payload, actionDeck, actionBack)
	if err != nil || !ok {
		return Stay(state), err
	}

	if callback.Action == actionBack {
		next, err := o.showLearning(ctx, u, state)
		if err != nil {
			return StepResult{}, err
		}
		return Next(stateLearning, next), nil
	}

	state.DeckID = callback.ID
	return o.askTranslation(ctx, u, state)
}

// stepTranslation принимает выбор языка перевода и заводит курс.
func (o *Onboarding) stepTranslation(ctx context.Context, u *port.Update, payload json.RawMessage) (StepResult, error) {
	state, callback, ok, err := o.expect(ctx, u, payload, actionTranslation, actionBack)
	if err != nil || !ok {
		return Stay(state), err
	}

	if callback.Action == actionBack {
		return o.askDeck(ctx, u, state)
	}

	known, _ := UserFrom(ctx)
	lang, valid := parseLanguage(callback.Param)
	if !valid {
		return Stay(state), nil
	}

	result, err := o.service.Complete(ctx, onboarding.Choice{
		UserID:          known.ID,
		DeckID:          lexicon.DeckID(state.DeckID),
		TranslationLang: lang,
	})
	if err != nil {
		return StepResult{}, o.explain(ctx, u, err)
	}

	localizer, err := o.localizer(ctx)
	if err != nil {
		return StepResult{}, err
	}

	text, err := localizer.T("onboarding.done", port.Args{
		"Deck":     result.Deck.Title,
		"Language": langName(localizer, lang.String()),
		"PerDay":   user.DefaultNewPerDay,
	})
	if err != nil {
		return StepResult{}, err
	}

	// Кнопки убираем: выбор сделан, и нажимать их больше незачем.
	if err := o.edit(ctx, u, port.MessageID(state.MessageID), text, nil); err != nil {
		return StepResult{}, err
	}
	return Done(), nil
}

// expect разбирает нажатие кнопки и проверяет, что оно относится к этому шагу.
//
// Второе значение — false, если апдейт шагу не подходит: пользователь написал
// текст вместо нажатия или нажал кнопку от другого сообщения. В обоих случаях
// диалог остаётся на месте — переспросить лучше, чем всё бросить.
func (o *Onboarding) expect(ctx context.Context, u *port.Update, payload json.RawMessage, actions ...string) (dialogState, Callback, bool, error) {
	var state dialogState
	if err := UnmarshalPayload(payload, &state); err != nil {
		return state, Callback{}, false, err
	}
	if u.Callback == nil {
		return state, Callback{}, false, nil
	}

	callback, ok := decodeCallback(u.Callback.Data)
	if !ok {
		// Данные кнопки не разбираются: она осталась от прошлой версии
		// бота или подделана. Шаг переспросит, а не сломается.
		return state, Callback{}, false, nil
	}
	for _, action := range actions {
		if callback.Action == action {
			return state, callback, true, nil
		}
	}
	return state, Callback{}, false, nil
}

// ask показывает вопрос: правит прежнее сообщение, если оно известно,
// и отправляет новое, если нет.
func (o *Onboarding) ask(ctx context.Context, u *port.Update, messageID port.MessageID, key string, keyboard *port.Keyboard) (port.MessageID, error) {
	text, err := o.text(ctx, key, nil)
	if err != nil {
		return 0, err
	}

	if messageID != 0 {
		if err := o.edit(ctx, u, messageID, text, keyboard); err != nil {
			return 0, err
		}
		return messageID, nil
	}
	return o.messenger.SendMessage(ctx, port.OutgoingMessage{ChatID: u.Chat, Text: text, Keyboard: keyboard})
}

func (o *Onboarding) edit(ctx context.Context, u *port.Update, messageID port.MessageID, text string, keyboard *port.Keyboard) error {
	return o.messenger.EditMessage(ctx, port.MessageEdit{
		ChatID:    u.Chat,
		MessageID: messageID,
		Text:      text,
		Keyboard:  keyboard,
	})
}

// explain превращает ошибку сценария в понятное пользователю сообщение.
//
// Ошибки, о которых человеку есть что сказать, объясняются словами;
// остальные уходят наверх и станут «что-то пошло не так».
func (o *Onboarding) explain(ctx context.Context, u *port.Update, cause error) error {
	var key string
	switch {
	case errors.Is(cause, onboarding.ErrNothingToLearn), errors.Is(cause, onboarding.ErrNoTranslations):
		key = "onboarding.nothing_to_learn"
	case errors.Is(cause, port.ErrNotFound), errors.Is(cause, onboarding.ErrSameLanguage), errors.Is(cause, onboarding.ErrNoLanguage):
		key = "onboarding.expired"
	default:
		return cause
	}

	text, err := o.text(ctx, key, nil)
	if err != nil {
		return err
	}
	if _, err := o.messenger.SendMessage(ctx, port.OutgoingMessage{ChatID: u.Chat, Text: text}); err != nil {
		return err
	}
	return nil
}

func (o *Onboarding) localizer(ctx context.Context) (port.Localizer, error) {
	localizer, ok := LocalizerFrom(ctx)
	if !ok {
		return nil, errors.New("нет локализатора: middleware локализации не подключён")
	}
	return localizer, nil
}

func (o *Onboarding) text(ctx context.Context, key string, args port.Args) (string, error) {
	localizer, err := o.localizer(ctx)
	if err != nil {
		return "", err
	}
	return localizer.T(key, args)
}

// parseLanguage, parseUILang и decodeCallback превращают разбор внешних
// данных в ответ «да или нет»: шагу диалога не нужна причина, по которой
// нажатие не годится, — ему нужно решение, переспрашивать или нет.
func parseLanguage(code string) (lexicon.Language, bool) {
	lang, err := lexicon.ParseLanguage(code)
	return lang, err == nil
}

func parseUILang(code string) (user.UILang, bool) {
	lang, err := user.ParseUILang(code)
	return lang, err == nil
}

func decodeCallback(data string) (Callback, bool) {
	callback, err := DecodeCallback(data)
	return callback, err == nil
}

// langName переводит код языка в название на языке пользователя.
// Неизвестный код показывается как есть: это лучше, чем пустая кнопка.
func langName(localizer port.Localizer, code string) string {
	if name, err := localizer.T("lang."+code, nil); err == nil {
		return name
	}
	return code
}

// deckTitle даёт название колоды на языке интерфейса.
//
// У встроенных колод оно записано в базе один раз и на всех, а у личной
// собирается на месте: заведена она была на том языке, на котором человек
// говорил в тот день, а язык интерфейса он может и сменить.
func deckTitle(localizer port.Localizer, deck *lexicon.Deck) string {
	if deck.IsBuiltin() {
		return deck.Title
	}

	title, err := localizer.T("decks.personal", port.Args{
		"Language": langName(localizer, deck.Lang.String()),
	})
	if err != nil {
		return deck.Title
	}
	return title
}

// mustText возвращает перевод или ключ: надпись на кнопке не повод
// прерывать диалог.
func mustText(localizer port.Localizer, key string) string {
	if text, err := localizer.T(key, nil); err == nil {
		return text
	}
	return key
}

// displayName выбирает, как обратиться к человеку. Имени в Telegram может
// не быть вовсе, и тогда здоровается бот безлично — но на языке пользователя.
func displayName(localizer port.Localizer, u *port.Update, known *user.User) string {
	if known.Username != "" {
		return known.Username
	}
	if u.Sender.Username != "" {
		return u.Sender.Username
	}
	return mustText(localizer, "common.friend")
}
