package telegram

import (
	"context"
	"time"

	"lexi-bot/internal/infra/metrics"
	"lexi-bot/internal/usecase/port"
)

// Measure считает апдейты и время их обработки.
//
// Стоит близко к краю, сразу за восстановлением после паники: измерять надо
// всё, что дошло до бота, включая то, на чём он споткнулся. Упавшая
// обработка — самая интересная для графика, и терять её было бы обидно.
func Measure(registry *metrics.Registry) Middleware {
	updates := registry.NewCounter("lexi_updates_total",
		"Полученные апдейты по виду и исходу", "kind", "status")
	duration := registry.NewHistogram("lexi_update_duration_seconds",
		"Время обработки апдейта", nil)

	return func(next port.UpdateHandler) port.UpdateHandler {
		return port.UpdateHandlerFunc(func(ctx context.Context, u *port.Update) error {
			started := time.Now()
			err := next.Handle(ctx, u)
			duration.Observe(time.Since(started).Seconds())

			status := "ok"
			if err != nil {
				status = "error"
			}
			updates.Inc(updateKind(u), status)
			return err
		})
	}
}

// updateKind описывает апдейт одним словом.
//
// Метка должна быть из небольшого набора: значения меток разводят метрику
// на отдельные ряды, и подставлять туда, например, текст команды значило бы
// завести ряд на каждую опечатку пользователя.
func updateKind(u *port.Update) string {
	switch {
	case u.Callback != nil:
		return "callback"
	case u.IsCommand():
		return "command"
	case u.Document != nil:
		return "document"
	case u.Text != "":
		return "text"
	default:
		return "other"
	}
}
