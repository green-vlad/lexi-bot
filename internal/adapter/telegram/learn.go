package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

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

// stateTyping — шаг диалога, на котором бот ждёт напечатанный перевод.
//
// Без состояния бот не отличил бы ответ на карточку от случайного сообщения
// в чат, а «посторонняя команда во время ожидания ответа не считается
// ответом» (PLAN.md §5) обеспечивается самим движком диалогов: команда
// прерывает диалог и выполняется.
const stateTyping = "learn:typing"

// typingState — что нужно помнить, пока ждём ответ.
type typingState struct {
	CardID    int64  `json:"card"`
	Attempt   string `json:"attempt"`
	ShownAt   int64  `json:"shown"`
	MessageID int    `json:"message"`
}

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
	clock     port.Clock
	dialogs   *Dialogs
}

// NewLearn создаёт хендлер учебной сессии.
func NewLearn(service *session.Service, courses port.CourseRepo, messenger port.Messenger, catalog port.Catalog, clock port.Clock, dialogs *Dialogs) (*Learn, error) {
	switch {
	case service == nil:
		return nil, errors.New("сессии нужен сценарий")
	case courses == nil:
		return nil, errors.New("сессии нужен CourseRepo")
	case messenger == nil:
		return nil, errors.New("сессии нужен мессенджер")
	case catalog == nil:
		return nil, errors.New("сессии нужен каталог переводов")
	case clock == nil:
		return nil, errors.New("сессии нужны часы")
	case dialogs == nil:
		return nil, errors.New("сессии нужен движок диалогов")
	}

	l := &Learn{
		session: service, courses: courses, messenger: messenger,
		catalog: catalog, clock: clock, dialogs: dialogs,
	}
	dialogs.Register(stateTyping, StepFunc(l.typed))
	return l, nil
}

// Register привязывает команду и кнопки к роутеру.
func (l *Learn) Register(router *Router) {
	router.Command("learn", port.UpdateHandlerFunc(l.start))
	router.CallbackAction(actionNext, port.UpdateHandlerFunc(l.next))
	router.CallbackAction(actionShow, port.UpdateHandlerFunc(l.show))
	router.CallbackAction(actionRate, port.UpdateHandlerFunc(l.rate))
	router.CallbackAction(actionAnswer, port.UpdateHandlerFunc(l.answer))
}

// answer принимает выбранный вариант.
func (l *Learn) answer(ctx context.Context, u *port.Update) error {
	callback, ok := decodeCallback(u.Callback.Data)
	if !ok {
		return nil
	}

	correct, rest, _ := strings.Cut(callback.Param, ":")
	attempt, shown, _ := strings.Cut(rest, ":")

	outcome, err := l.session.Submit(ctx, session.Answer{
		CardID:  study.CardID(callback.ID),
		Attempt: attempt,
		Mode:    study.ModeChoice,
		Correct: correct == "1",
		Elapsed: l.elapsed(shown),
	})
	if err != nil {
		return err
	}
	if outcome.Duplicate {
		return l.stale(ctx, u)
	}

	if outcome.Correct {
		// Верный ответ не нуждается в разборе: сразу следующее слово.
		return l.continueOutsideDialog(ctx, u, outcome.CourseID, u.Callback.MessageID)
	}
	return l.explainMiss(ctx, u, &outcome)
}

// explainMiss показывает правильный ответ и кнопку «дальше».
//
// Промах — единственное место, где стоит задержаться: человеку нужно
// увидеть верный перевод, а не проскочить мимо него к следующей карточке.
func (l *Learn) explainMiss(ctx context.Context, u *port.Update, outcome *session.Outcome) error {
	localizer, err := l.localizer(ctx)
	if err != nil {
		return err
	}

	item, err := l.session.Card(ctx, outcome.CardID)
	if err != nil {
		return err
	}

	text, err := localizer.T("learn.answer_wrong", port.Args{
		"Term":        item.Lexeme.Term,
		"Translation": strings.Join(outcome.Expected, ", "),
	})
	if err != nil {
		return err
	}

	keyboard, err := NewKeyboard().Row(Button(
		mustText(localizer, "learn.next"),
		Callback{Action: actionNext, ID: int64(outcome.CourseID)},
	)).Build()
	if err != nil {
		return err
	}
	return l.render(ctx, u, u.Callback.MessageID, text, keyboard)
}

// elapsed восстанавливает, сколько человек думал над ответом.
// Испорченный или отсутствующий момент показа означает «не измеряли»:
// такой ответ не будет считаться быстрым, и это правильнее, чем выдать
// «легко» за ответ неизвестной длительности.
func (l *Learn) elapsed(shown string) time.Duration {
	at, ok := decodeTime(shown)
	if !ok {
		return 0
	}
	elapsed := l.clock.Now().Sub(at)
	if elapsed < 0 {
		return 0
	}
	return elapsed
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
	return l.continueOutsideDialog(ctx, u, course.ID, 0)
}

// next показывает следующую карточку после разбора предыдущей.
func (l *Learn) next(ctx context.Context, u *port.Update) error {
	callback, ok := decodeCallback(u.Callback.Data)
	if !ok {
		return nil
	}
	return l.continueOutsideDialog(ctx, u, study.CourseID(callback.ID), u.Callback.MessageID)
}

// showNext достаёт следующую карточку и показывает её, правя сообщение
// messageID; при нулевом messageID отправляет новое.
//
// Возвращает состояние ожидания ввода, если показанная карточка спрашивается
// текстом, — начать диалог должен вызывающий. Начинать его здесь нельзя:
// когда следующую карточку показывает шаг диалога, движок после шага
// перезапишет состояние своим, и ожидание пропадёт.
func (l *Learn) showNext(ctx context.Context, u *port.Update, courseID study.CourseID, messageID port.MessageID) (*typingState, error) {
	item, reason, err := l.session.Next(ctx, courseID)
	if err != nil {
		return nil, err
	}
	if reason != session.ReasonNone {
		return nil, l.finish(ctx, u, messageID, courseID, reason)
	}
	return l.showCard(ctx, u, messageID, &item)
}

// continueOutsideDialog показывает следующую карточку из места, где диалога
// нет: из команды или нажатия кнопки.
func (l *Learn) continueOutsideDialog(ctx context.Context, u *port.Update, courseID study.CourseID, messageID port.MessageID) error {
	waiting, err := l.showNext(ctx, u, courseID, messageID)
	if err != nil || waiting == nil {
		return err
	}
	return l.dialogs.Start(ctx, stateTyping, *waiting)
}

// showCard рисует вопрос.
func (l *Learn) showCard(ctx context.Context, u *port.Update, messageID port.MessageID, item *session.Item) (*typingState, error) {
	localizer, err := l.localizer(ctx)
	if err != nil {
		return nil, err
	}

	text, err := cardText(localizer, item)
	if err != nil {
		return nil, err
	}
	switch item.Mode {
	case study.ModeChoice:
		text += "\n\n" + mustText(localizer, "learn.choose_translation")
	case study.ModeTyping:
		text += "\n\n" + mustText(localizer, "learn.type_prompt")
	}

	shownAt := l.clock.Now()

	keyboard, err := l.questionKeyboard(localizer, item, shownAt)
	if err != nil {
		return nil, err
	}

	if item.Mode != study.ModeTyping {
		return nil, l.render(ctx, u, messageID, text, keyboard)
	}

	// В режиме ввода кнопок нет, зато бот переходит в ожидание ответа:
	// иначе он не отличил бы перевод от случайного сообщения в чат.
	sent, err := l.renderAndReturn(ctx, u, messageID, text, nil)
	if err != nil {
		return nil, err
	}
	return &typingState{
		CardID:    int64(item.Card.ID),
		Attempt:   session.Attempt(&item.Card),
		ShownAt:   shownAt.Unix(),
		MessageID: int(sent),
	}, nil
}

// typed принимает напечатанный перевод.
func (l *Learn) typed(ctx context.Context, u *port.Update, payload json.RawMessage) (StepResult, error) {
	var state typingState
	if err := UnmarshalPayload(payload, &state); err != nil {
		return StepResult{}, err
	}

	text := strings.TrimSpace(u.Text)
	if text == "" {
		// Нажатие кнопки или пустое сообщение ответом не считается:
		// продолжаем ждать.
		return Stay(state), nil
	}

	outcome, err := l.session.Submit(ctx, session.Answer{
		CardID:  study.CardID(state.CardID),
		Attempt: state.Attempt,
		Mode:    study.ModeTyping,
		Text:    text,
		Elapsed: l.clock.Now().Sub(time.Unix(state.ShownAt, 0)),
	})
	if err != nil {
		return StepResult{}, err
	}
	if outcome.Duplicate {
		if err := l.stale(ctx, u); err != nil {
			return StepResult{}, err
		}
		return Done(), nil
	}

	if err := l.explainTyped(ctx, u, &state, &outcome, text); err != nil {
		return StepResult{}, err
	}

	// Верный ответ ведёт дальше сам; после промаха человек нажмёт «дальше»,
	// посмотрев на правильный перевод.
	if !outcome.Correct || outcome.Match != lexicon.MatchExact {
		return Done(), nil
	}

	waiting, err := l.showNext(ctx, u, outcome.CourseID, 0)
	if err != nil {
		return StepResult{}, err
	}
	if waiting == nil {
		// Следующая карточка спрашивается кнопками или занятие кончилось.
		return Done(), nil
	}
	// Ожидание ввода для новой карточки ставит движок: вернуть Done здесь
	// значило бы стереть его сразу после установки.
	return Next(stateTyping, *waiting), nil
}

// explainTyped показывает разбор напечатанного ответа.
//
// Разбор нужен даже при верном ответе с опечаткой: человек должен увидеть,
// как пишется правильно, иначе он выучит свою опечатку.
func (l *Learn) explainTyped(ctx context.Context, u *port.Update, state *typingState, outcome *session.Outcome, typed string) error {
	localizer, err := l.localizer(ctx)
	if err != nil {
		return err
	}

	item, err := l.session.Card(ctx, study.CardID(state.CardID))
	if err != nil {
		return err
	}

	key := "learn.answer_typed_wrong"
	switch {
	case outcome.Match == lexicon.MatchExact:
		key = "learn.answer_typed_correct"
	case outcome.Correct:
		key = "learn.answer_typed_typo"
	}

	text, err := localizer.T(key, port.Args{
		"Term":        item.Lexeme.Term,
		"Translation": strings.Join(outcome.Expected, ", "),
		"Typed":       typed,
	})
	if err != nil {
		return err
	}

	var keyboard *port.Keyboard
	if !outcome.Correct || outcome.Match != lexicon.MatchExact {
		keyboard, err = NewKeyboard().Row(Button(
			mustText(localizer, "learn.next"),
			Callback{Action: actionNext, ID: int64(outcome.CourseID)},
		)).Build()
		if err != nil {
			return err
		}
	}

	// Правим карточку на месте: вопрос превращается в разбор, и в чате
	// не остаётся сообщения, на которое человек уже ответил.
	return l.render(ctx, u, port.MessageID(state.MessageID), text, keyboard)
}

// questionKeyboard собирает кнопки вопроса под режим проверки.
// Ввод текстом добавится в T-033.
func (l *Learn) questionKeyboard(localizer port.Localizer, item *session.Item, shownAt time.Time) (*port.Keyboard, error) {
	if item.Mode == study.ModeChoice {
		return l.choiceKeyboard(item, shownAt)
	}
	return NewKeyboard().Row(Button(
		mustText(localizer, "learn.show_translation"),
		Callback{Action: actionShow, ID: int64(item.Card.ID), Param: session.Attempt(&item.Card)},
	)).Build()
}

// choiceKeyboard собирает варианты ответа.
//
// В кнопке едет признак правильности, а не номер варианта: сами варианты
// нигде не хранятся, и восстановить по номеру, что было под ним, к моменту
// нажатия невозможно. Подделать кнопку теоретически можно, но обманет этим
// человек только себя — оценка ставится его же карточке.
func (l *Learn) choiceKeyboard(item *session.Item, shownAt time.Time) (*port.Keyboard, error) {
	attempt := session.Attempt(&item.Card)

	buttons := make([]KeyboardButton, 0, len(item.Options))
	for i, option := range item.Options {
		correct := "0"
		if i == item.Correct {
			correct = "1"
		}
		buttons = append(buttons, Button(option, Callback{
			Action: actionAnswer,
			ID:     int64(item.Card.ID),
			// Признак, токен попытки и момент показа: по последнему
			// считается, насколько быстро человек ответил.
			Param: correct + ":" + attempt + ":" + encodeTime(shownAt),
		}))
	}
	return NewKeyboard().Grid(2, buttons...).Build()
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

	return l.continueOutsideDialog(ctx, u, outcome.CourseID, u.Callback.MessageID)
}

// finish сообщает, что карточек больше нет, и показывает итог занятия.
func (l *Learn) finish(ctx context.Context, u *port.Update, messageID port.MessageID, courseID study.CourseID, reason session.Reason) error {
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

	// У курса на паузе итога нет: он и не занимался.
	if reason != session.ReasonPaused {
		summary, err := l.session.Summary(ctx, courseID)
		if err != nil {
			return err
		}
		if line := summaryText(localizer, &summary, l.clock.Now()); line != "" {
			text += "\n\n" + line
		}
	}

	// Кнопки убираем: занятие кончилось, нажимать нечего.
	return l.render(ctx, u, messageID, text, nil)
}

// summaryText собирает итог занятия.
//
// Пустая строка означает, что рассказывать не о чем: человек открыл /learn
// и не ответил ни на одну карточку, и «повторено 0 карточек, точность 0%»
// было бы не итогом, а упрёком.
func summaryText(localizer port.Localizer, summary *session.Summary, now time.Time) string {
	if summary.Reviewed == 0 {
		return nextReviewText(localizer, summary, now)
	}

	line, err := localizer.Plural("learn.summary_reviewed", summary.Reviewed, nil)
	if err != nil {
		return ""
	}
	if summary.New > 0 {
		if newLine, err := localizer.Plural("learn.summary_new", summary.New, nil); err == nil {
			line += newLine
		}
	}
	line += "."

	// Точность показывается, только если в журнале есть по чему считать:
	// «0% верных» рядом с «повторено 3 карточки» вводит в заблуждение.
	if summary.Answered > 0 {
		accuracy, err := localizer.T("learn.summary_accuracy", port.Args{
			// Округление к ближайшему: 83.4% — это 83%, а не 83.4%,
			// и уж точно не 84%.
			"Percent": int(math.Round(summary.Accuracy * 100)),
		})
		if err == nil {
			line += " " + accuracy
		}
	}

	if next := nextReviewText(localizer, summary, now); next != "" {
		line += "\n" + next
	}
	return line
}

// nextReviewText говорит, когда ждать следующую карточку.
//
// Время показывается «через сколько», а не датой: человеку важно, скоро ли
// возвращаться, а не какого числа это случится. Крупные единицы вместо
// точных минут по той же причине — «через 3 дня» полезнее, чем
// «через 4320 минут».
func nextReviewText(localizer port.Localizer, summary *session.Summary, now time.Time) string {
	if !summary.HasNext {
		return ""
	}

	left := summary.NextReview.Sub(now)
	switch {
	case left <= 0:
		text, err := localizer.T("learn.summary_next_now", nil)
		if err != nil {
			return ""
		}
		return text
	case left < time.Hour:
		return plural(localizer, "learn.summary_next_minutes", int(math.Ceil(left.Minutes())))
	case left < 24*time.Hour:
		return plural(localizer, "learn.summary_next_hours", int(math.Round(left.Hours())))
	default:
		return plural(localizer, "learn.summary_next_days", int(math.Round(left.Hours()/24)))
	}
}

func plural(localizer port.Localizer, key string, count int) string {
	text, err := localizer.Plural(key, count, nil)
	if err != nil {
		return ""
	}
	return text
}

// stale отвечает на кнопку от карточки, на которую уже ответили.
func (l *Learn) stale(ctx context.Context, u *port.Update) error {
	return Reply(l.messenger, "learn.stale_button").Handle(ctx, u)
}

// render правит сообщение, если оно известно, и отправляет новое, если нет.
func (l *Learn) render(ctx context.Context, u *port.Update, messageID port.MessageID, text string, keyboard *port.Keyboard) error {
	_, err := l.renderAndReturn(ctx, u, messageID, text, keyboard)
	return err
}

// renderAndReturn делает то же и сообщает, какое сообщение теперь на экране.
func (l *Learn) renderAndReturn(ctx context.Context, u *port.Update, messageID port.MessageID, text string, keyboard *port.Keyboard) (port.MessageID, error) {
	if messageID != 0 {
		return messageID, l.messenger.EditMessage(ctx, port.MessageEdit{
			ChatID:    u.Chat,
			MessageID: messageID,
			Text:      text,
			Keyboard:  keyboard,
		})
	}

	return l.messenger.SendMessage(ctx, port.OutgoingMessage{
		ChatID:   u.Chat,
		Text:     text,
		Keyboard: keyboard,
	})
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

// encodeTime и decodeTime переводят момент в компактную строку и обратно.
// Тридцатишестеричные секунды эпохи занимают семь байт вместо десяти —
// в callback_data это заметная разница.
func encodeTime(at time.Time) string {
	return strconv.FormatInt(at.Unix(), 36)
}

func decodeTime(encoded string) (time.Time, bool) {
	seconds, err := strconv.ParseInt(encoded, 36, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}, false
	}
	return time.Unix(seconds, 0), true
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
