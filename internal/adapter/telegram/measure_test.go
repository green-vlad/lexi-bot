package telegram_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"lexi-bot/internal/adapter/telegram"
	"lexi-bot/internal/infra/metrics"
	"lexi-bot/internal/usecase/port"
)

func TestMeasureCountsUpdatesByKind(t *testing.T) {
	t.Parallel()

	registry := metrics.New()
	router := telegram.NewRouter()
	router.Use(telegram.Measure(registry))
	router.Command("learn", port.UpdateHandlerFunc(func(context.Context, *port.Update) error { return nil }))
	router.Text(port.UpdateHandlerFunc(func(context.Context, *port.Update) error { return nil }))
	router.Callback(port.UpdateHandlerFunc(func(context.Context, *port.Update) error { return nil }))

	handle := func(u *port.Update) {
		if err := router.Handle(context.Background(), u); err != nil {
			t.Fatalf("Handle() вернул ошибку: %v", err)
		}
	}
	handle(message("/learn"))
	handle(message("дом"))
	handle(&port.Update{ID: 2, Chat: 777, Callback: &port.CallbackData{ID: "cb", Data: "rev:1"}})

	got := expose(t, registry)
	for _, want := range []string{
		`lexi_updates_total{kind="command",status="ok"} 1`,
		`lexi_updates_total{kind="text",status="ok"} 1`,
		`lexi_updates_total{kind="callback",status="ok"} 1`,
		"lexi_update_duration_seconds_count 3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("метрики не содержат %q:\n%s", want, got)
		}
	}
}

func TestMeasureCountsFailures(t *testing.T) {
	t.Parallel()

	registry := metrics.New()
	router := telegram.NewRouter()
	router.Use(telegram.Measure(registry))
	router.Command("learn", port.UpdateHandlerFunc(func(context.Context, *port.Update) error {
		return errors.New("сценарий сломался")
	}))

	if err := router.Handle(context.Background(), message("/learn")); err == nil {
		t.Fatal("ожидалась ошибка")
	}

	// Упавшая обработка — самая интересная для графика, и терять её
	// было бы обидно.
	got := expose(t, registry)
	if !strings.Contains(got, `lexi_updates_total{kind="command",status="error"} 1`) {
		t.Errorf("метрики = %s", got)
	}
	if !strings.Contains(got, "lexi_update_duration_seconds_count 1") {
		t.Errorf("время упавшей обработки не измерено:\n%s", got)
	}
}

func expose(t *testing.T, registry *metrics.Registry) string {
	t.Helper()

	var b strings.Builder
	if err := registry.Expose(&b); err != nil {
		t.Fatalf("Expose() вернул ошибку: %v", err)
	}
	return b.String()
}
