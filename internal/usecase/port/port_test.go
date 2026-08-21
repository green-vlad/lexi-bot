package port_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/usecase/port"
)

// Порт случайности обязан годиться планировщику: иначе в сборке приложения
// пришлось бы держать два источника случайности вместо одного.
var _ study.Rand = port.Rand(nil)

func TestClockFunc(t *testing.T) {
	t.Parallel()

	moment := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	clock := port.ClockFunc(func() time.Time { return moment })

	var c port.Clock = clock
	if !c.Now().Equal(moment) {
		t.Errorf("Now() = %v, ожидалось %v", c.Now(), moment)
	}
}

func TestErrNotFoundIsMatchable(t *testing.T) {
	t.Parallel()

	// Репозиторий оборачивает ошибку своим контекстом, а сценарий обязан
	// узнать её через errors.Is — на этом стоит развилка «создать или взять».
	wrapped := fmt.Errorf("прочитать настройки пользователя 42: %w", port.ErrNotFound)
	if !errors.Is(wrapped, port.ErrNotFound) {
		t.Error("обёрнутая ErrNotFound не опознаётся через errors.Is")
	}
}
