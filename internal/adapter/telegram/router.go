package telegram

import (
	"context"
	"strings"

	"lexi-bot/internal/usecase/port"
)

// Router раскладывает апдейты по хендлерам.
//
// Маршрутов три вида, и они не пересекаются: команда, нажатие кнопки и
// обычный текст. Порядок разбора обратный порядку частоты: сначала кнопки
// (их больше всего в учебной сессии), потом команды, потом текст.
type Router struct {
	commands  map[string]port.UpdateHandler
	actions   map[string]port.UpdateHandler
	callback  port.UpdateHandler
	text      port.UpdateHandler
	unknown   port.UpdateHandler
	middlewar []Middleware
}

var _ port.UpdateHandler = (*Router)(nil)

// NewRouter создаёт пустой роутер.
func NewRouter() *Router {
	return &Router{
		commands: map[string]port.UpdateHandler{},
		actions:  map[string]port.UpdateHandler{},
	}
}

// Use добавляет middleware. Они оборачивают хендлеры в порядке добавления:
// первый добавленный оказывается снаружи и видит апдейт первым.
//
// Снаружи должен стоять тот, кто обязан отработать при любом исходе, —
// восстановление после паники и логирование; внутри тот, кому нужны
// результаты предыдущих, — локализация после определения пользователя.
func (r *Router) Use(middleware ...Middleware) {
	r.middlewar = append(r.middlewar, middleware...)
}

// Command привязывает хендлер к команде: имя без слеша, в нижнем регистре.
func (r *Router) Command(name string, handler port.UpdateHandler) {
	r.commands[strings.ToLower(strings.TrimPrefix(name, "/"))] = handler
}

// CallbackAction привязывает хендлер к кнопкам с указанным действием.
//
// Кнопок в боте много и принадлежат они разным частям: оценка карточки,
// выбор колоды, смена языка. Разбирать действие в одном общем хендлере
// значило бы собрать там switch по всему приложению.
func (r *Router) CallbackAction(action string, handler port.UpdateHandler) {
	r.actions[action] = handler
}

// Callback привязывает хендлер ко всем остальным нажатиям — тем, действие
// которых не привязано отдельно.
func (r *Router) Callback(handler port.UpdateHandler) { r.callback = handler }

// Text привязывает хендлер к обычному тексту: ответы в режиме ввода
// и шаги диалогов приходят именно так.
func (r *Router) Text(handler port.UpdateHandler) { r.text = handler }

// Unknown привязывает хендлер к неизвестным командам. Без него такая
// команда молча игнорируется, а пользователь остаётся в недоумении.
func (r *Router) Unknown(handler port.UpdateHandler) { r.unknown = handler }

// Handle разбирает апдейт и передаёт его нужному хендлеру, прогнав через
// цепочку middleware.
//
// Middleware применяются к найденному маршруту, а не к поиску: апдейт,
// для которого маршрута нет, всё равно должен попасть в лог и не должен
// ронять процесс.
func (r *Router) Handle(ctx context.Context, u *port.Update) error {
	return r.wrap(r.route(u)).Handle(ctx, u)
}

// route выбирает хендлер для апдейта.
func (r *Router) route(u *port.Update) port.UpdateHandler {
	switch {
	case u.Callback != nil:
		// Кнопка могла остаться от прошлой версии бота: разобрать её данные
		// не получится, и это не повод падать — такие нажатия достаются
		// общему хендлеру, если он есть.
		if callback, ok := decodeCallback(u.Callback.Data); ok {
			if handler, found := r.actions[callback.Action]; found {
				return handler
			}
		}
		return orNoop(r.callback)
	case u.IsCommand():
		if handler, ok := r.commands[u.Command]; ok {
			return handler
		}
		return orNoop(r.unknown)
	case u.Text != "":
		return orNoop(r.text)
	default:
		return noop{}
	}
}

// wrap оборачивает хендлер цепочкой middleware.
func (r *Router) wrap(handler port.UpdateHandler) port.UpdateHandler {
	// В обратном порядке: последний добавленный оказывается ближе всех
	// к хендлеру, первый — снаружи.
	for i := len(r.middlewar) - 1; i >= 0; i-- {
		handler = r.middlewar[i](handler)
	}
	return handler
}

// noop — хендлер, который ничего не делает: им закрываются маршруты,
// для которых обработчик не назначен.
type noop struct{}

func (noop) Handle(context.Context, *port.Update) error { return nil }

func orNoop(handler port.UpdateHandler) port.UpdateHandler {
	if handler == nil {
		return noop{}
	}
	return handler
}
