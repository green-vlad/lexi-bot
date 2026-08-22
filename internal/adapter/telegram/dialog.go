package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/infra/logger"
	"lexi-bot/internal/usecase/port"
)

// DefaultDialogMaxAge — после какого простоя диалог считается брошенным.
//
// Шесть часов: человек, начавший добавлять слово утром и вернувшийся
// вечером, ждёт от бота меню, а не вопроса «а перевод?», о котором давно
// забыл. Уборка протухших строк в базе (T-019) идёт отдельно и реже —
// здесь важно не показать пользователю мёртвый шаг, а не освободить место.
const DefaultDialogMaxAge = 6 * time.Hour

// DefaultCancelCommand — команда, отменяющая любой диалог.
const DefaultCancelCommand = "cancel"

// ErrResetDialog возвращается шагом, который понял, что продолжать нечем:
// в payload оказалось не то, что он туда клал, или состояние потеряло смысл.
// Движок сбрасывает диалог, вместо того чтобы оставлять пользователя
// в ловушке, где каждое сообщение уходит в сломанный шаг.
var ErrResetDialog = errors.New("диалог сброшен")

// StepResult — что делать после шага.
type StepResult struct {
	next    string
	payload any
	done    bool
}

// Next переводит диалог на следующий шаг с новым состоянием payload.
func Next(state string, payload any) StepResult {
	return StepResult{next: state, payload: payload}
}

// Stay оставляет диалог на том же шаге: так отвечают на неподходящий ввод,
// когда переспросить правильнее, чем всё бросить.
func Stay(payload any) StepResult {
	return StepResult{payload: payload}
}

// Done завершает диалог.
func Done() StepResult { return StepResult{done: true} }

// Step обрабатывает один шаг диалога.
//
// payload — то, что предыдущий шаг положил в состояние; у первого шага это
// то, с чем диалог начали.
type Step interface {
	Handle(ctx context.Context, u *port.Update, payload json.RawMessage) (StepResult, error)
}

// StepFunc превращает функцию в Step.
type StepFunc func(ctx context.Context, u *port.Update, payload json.RawMessage) (StepResult, error)

// Handle вызывает саму функцию.
func (f StepFunc) Handle(ctx context.Context, u *port.Update, payload json.RawMessage) (StepResult, error) {
	return f(ctx, u, payload)
}

// DialogsConfig — параметры движка диалогов.
type DialogsConfig struct {
	Sessions  port.SessionRepo
	Messenger port.Messenger
	Clock     port.Clock
	// MaxAge — после какого простоя диалог считается брошенным.
	MaxAge time.Duration
	// CancelCommand — команда отмены без слеша.
	CancelCommand string
	Logger        *slog.Logger
}

// Dialogs ведёт диалоги в несколько шагов поверх SessionRepo.
//
// Состояние лежит в базе, а не в памяти процесса: диалог обязан пережить
// перезапуск бота, иначе каждый деплой обрывал бы всех, кто в этот момент
// добавлял слово.
type Dialogs struct {
	sessions  port.SessionRepo
	messenger port.Messenger
	clock     port.Clock
	maxAge    time.Duration
	cancelCmd string
	log       *slog.Logger
	steps     map[string]Step
}

// NewDialogs создаёт движок диалогов. Параметры передаются указателем,
// как и в остальных конструкторах адаптера.
func NewDialogs(cfg *DialogsConfig) (*Dialogs, error) {
	if cfg == nil || cfg.Sessions == nil {
		return nil, errors.New("хранилище диалогов не задано")
	}

	clock := cfg.Clock
	if clock == nil {
		clock = port.ClockFunc(time.Now)
	}
	maxAge := cfg.MaxAge
	if maxAge <= 0 {
		maxAge = DefaultDialogMaxAge
	}
	cancel := cfg.CancelCommand
	if cancel == "" {
		cancel = DefaultCancelCommand
	}

	return &Dialogs{
		sessions:  cfg.Sessions,
		messenger: cfg.Messenger,
		clock:     clock,
		maxAge:    maxAge,
		cancelCmd: cancel,
		log:       orDefault(cfg.Logger),
		steps:     map[string]Step{},
	}, nil
}

// Register привязывает шаг к имени состояния.
func (d *Dialogs) Register(state string, step Step) { d.steps[state] = step }

// Start начинает диалог с указанного шага. Первое сообщение пользователю
// отправляет вызывающий: движок не знает, что именно нужно спросить.
func (d *Dialogs) Start(ctx context.Context, state string, payload any) error {
	known, ok := UserFrom(ctx)
	if !ok {
		return errors.New("диалог нельзя начать без пользователя")
	}
	if _, exists := d.steps[state]; !exists {
		return fmt.Errorf("шаг диалога %q не зарегистрирован", state)
	}
	return d.save(ctx, known.ID, state, payload)
}

// Cancel сбрасывает текущий диалог, если он есть.
func (d *Dialogs) Cancel(ctx context.Context) error {
	known, ok := UserFrom(ctx)
	if !ok {
		return nil
	}
	return d.sessions.Delete(ctx, known.ID)
}

// Middleware встраивает диалоги в конвейер роутера.
//
// Пока диалог идёт, обычные сообщения и нажатия кнопок достаются его шагу,
// а не маршруту роутера. Команды — наоборот: они прерывают диалог, потому
// что человек, набравший /stats посреди добавления слова, передумал,
// а не отвечает на вопрос про перевод (PLAN.md §5).
func (d *Dialogs) Middleware() Middleware {
	return func(next port.UpdateHandler) port.UpdateHandler {
		return port.UpdateHandlerFunc(func(ctx context.Context, u *port.Update) error {
			known, ok := UserFrom(ctx)
			if !ok {
				return next.Handle(ctx, u)
			}

			session, err := d.sessions.Get(ctx, known.ID)
			switch {
			case errors.Is(err, port.ErrNotFound):
				return next.Handle(ctx, u)
			case err != nil:
				return err
			}

			if u.Command == d.cancelCmd {
				return d.cancelled(ctx, known.ID, u)
			}
			if u.IsCommand() {
				// Диалог прерван: команда выполняется, состояние не остаётся
				// висеть, чтобы следующее сообщение не ушло в брошенный шаг.
				if err := d.sessions.Delete(ctx, known.ID); err != nil {
					return err
				}
				return next.Handle(ctx, u)
			}

			if d.expired(session) {
				logger.FromContext(ctx, d.log).Info("брошенный диалог сброшен",
					slog.String("state", session.State),
					slog.Time("updated_at", session.UpdatedAt))
				if err := d.sessions.Delete(ctx, known.ID); err != nil {
					return err
				}
				return next.Handle(ctx, u)
			}

			step, exists := d.steps[session.State]
			if !exists {
				// Состояние из базы, которого движок не знает: так бывает
				// после выката, убравшего шаг. Сбрасываем — иначе человек
				// застрянет навсегда.
				logger.FromContext(ctx, d.log).Warn("неизвестный шаг диалога сброшен",
					slog.String("state", session.State))
				if err := d.sessions.Delete(ctx, known.ID); err != nil {
					return err
				}
				return next.Handle(ctx, u)
			}

			result, err := step.Handle(ctx, u, session.Payload)
			if errors.Is(err, ErrResetDialog) {
				logger.FromContext(ctx, d.log).Warn("шаг диалога сбросил состояние",
					slog.String("state", session.State),
					slog.Any("error", err))
				return d.sessions.Delete(ctx, known.ID)
			}
			if err != nil {
				return err
			}

			return d.apply(ctx, known.ID, session.State, result)
		})
	}
}

// apply сохраняет то, что вернул шаг.
func (d *Dialogs) apply(ctx context.Context, userID user.ID, current string, result StepResult) error {
	if result.done {
		return d.sessions.Delete(ctx, userID)
	}

	state := result.next
	if state == "" {
		state = current
	}
	return d.save(ctx, userID, state, result.payload)
}

// save записывает состояние диалога.
func (d *Dialogs) save(ctx context.Context, userID user.ID, state string, payload any) error {
	raw, err := marshalPayload(payload)
	if err != nil {
		return err
	}

	return d.sessions.Save(ctx, port.Session{
		UserID:    userID,
		State:     state,
		Payload:   raw,
		UpdatedAt: d.clock.Now(),
	})
}

// cancelled обрабатывает /cancel.
func (d *Dialogs) cancelled(ctx context.Context, userID user.ID, u *port.Update) error {
	if err := d.sessions.Delete(ctx, userID); err != nil {
		return err
	}
	if d.messenger == nil {
		return nil
	}
	return Reply(d.messenger, "common.cancelled").Handle(ctx, u)
}

// expired сообщает, что диалог заброшен.
func (d *Dialogs) expired(session port.Session) bool {
	if session.UpdatedAt.IsZero() {
		return false
	}
	return d.clock.Now().Sub(session.UpdatedAt) > d.maxAge
}

// marshalPayload превращает состояние шага в JSON. Пустое состояние — это
// пустой объект, а не отсутствие данных: колонка объявлена NOT NULL.
func marshalPayload(payload any) (json.RawMessage, error) {
	if payload == nil {
		return json.RawMessage(`{}`), nil
	}
	if raw, ok := payload.(json.RawMessage); ok {
		return raw, nil
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("сохранить состояние диалога: %w", err)
	}
	return encoded, nil
}

// UnmarshalPayload разбирает состояние шага.
//
// Ошибка разбора превращается в ErrResetDialog: значит, в базе лежит
// не то, что шаг туда клал, и продолжать нечем.
func UnmarshalPayload(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("%w: состояние шага не разбирается: %w", ErrResetDialog, err)
	}
	return nil
}
