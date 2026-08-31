package delivery

import (
	"context"
	"time"
)

// Limiter пропускает не больше заданного числа отправок в секунду.
//
// Интерфейс, а не конкретный тип: тест не должен ждать реального времени,
// чтобы проверить, что рассылка спрашивает разрешения перед каждой
// отправкой.
type Limiter interface {
	// Wait ждёт своей очереди. Ошибка означает отменённый контекст:
	// рассылку остановили, и слать больше нечего.
	Wait(ctx context.Context) error
}

// TickerLimiter — ограничитель на равномерных интервалах.
//
// Равномерных, а не с накоплением: Telegram считает не среднюю скорость
// за минуту, а плотность запросов, и залп из тридцати сообщений подряд
// упрётся в ограничение, даже если минутное среднее его не превышает.
type TickerLimiter struct {
	ticker *time.Ticker
}

// NewTickerLimiter создаёт ограничитель на perSecond отправок в секунду.
func NewTickerLimiter(perSecond int) *TickerLimiter {
	if perSecond <= 0 {
		perSecond = 1
	}
	return &TickerLimiter{ticker: time.NewTicker(time.Second / time.Duration(perSecond))}
}

// Wait ждёт следующего окна отправки.
func (l *TickerLimiter) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.ticker.C:
		return nil
	}
}

// Stop останавливает ограничитель.
func (l *TickerLimiter) Stop() { l.ticker.Stop() }
