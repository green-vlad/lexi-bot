package metrics_test

import (
	"strings"
	"sync"
	"testing"

	"lexi-bot/internal/infra/metrics"
)

// render отдаёт метрики строкой.
func render(t *testing.T, r *metrics.Registry) string {
	t.Helper()

	var b strings.Builder
	if err := r.Expose(&b); err != nil {
		t.Fatalf("Expose() вернул ошибку: %v", err)
	}
	return b.String()
}

func TestCounterWithLabels(t *testing.T) {
	t.Parallel()

	r := metrics.New()
	answers := r.NewCounter("lexi_answers_total", "Ответы по режимам", "mode", "correct")

	answers.Inc("choice", "true")
	answers.Inc("choice", "true")
	answers.Inc("typing", "false")

	got := render(t, r)
	for _, want := range []string{
		"# HELP lexi_answers_total Ответы по режимам",
		"# TYPE lexi_answers_total counter",
		`lexi_answers_total{mode="choice",correct="true"} 2`,
		`lexi_answers_total{mode="typing",correct="false"} 1`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("вывод не содержит %q:\n%s", want, got)
		}
	}
}

func TestCounterIgnoresWrongLabelCount(t *testing.T) {
	t.Parallel()

	r := metrics.New()
	answers := r.NewCounter("lexi_answers_total", "Ответы", "mode")

	// Метрика — не тот повод, из-за которого стоит ронять обработку
	// апдейта: лишние и недостающие метки молча отбрасываются.
	answers.Inc()
	answers.Inc("choice", "лишнее")
	answers.Inc("choice")

	got := render(t, r)
	if !strings.Contains(got, `lexi_answers_total{mode="choice"} 1`) {
		t.Errorf("вывод = %s", got)
	}
	if strings.Count(got, "lexi_answers_total{") != 1 {
		t.Errorf("в выводе лишние строки:\n%s", got)
	}
}

func TestCounterWithoutLabels(t *testing.T) {
	t.Parallel()

	r := metrics.New()
	errs := r.NewCounter("lexi_telegram_errors_total", "Ошибки Telegram API")

	errs.Add(3)

	if got := render(t, r); !strings.Contains(got, "lexi_telegram_errors_total 3") {
		t.Errorf("вывод = %s", got)
	}
}

func TestGaugeReadsAtRenderTime(t *testing.T) {
	t.Parallel()

	r := metrics.New()
	value := 0.0
	r.NewGauge("lexi_pool_acquired", "Занятые соединения", func() float64 { return value })

	// Датчик спрашивает значение в момент отдачи, а не хранит копию,
	// которая успеет устареть.
	value = 7
	if got := render(t, r); !strings.Contains(got, "lexi_pool_acquired 7") {
		t.Errorf("вывод = %s", got)
	}

	value = 3
	if got := render(t, r); !strings.Contains(got, "lexi_pool_acquired 3") {
		t.Errorf("вывод = %s", got)
	}
}

func TestHistogramBucketsAreCumulative(t *testing.T) {
	t.Parallel()

	r := metrics.New()
	h := r.NewHistogram("lexi_update_duration_seconds", "Обработка апдейта",
		[]float64{0.01, 0.1, 1})

	h.Observe(0.005) // попадает во все три корзины
	h.Observe(0.05)  // в две последние
	h.Observe(2)     // ни в одну, но в +Inf

	got := render(t, r)
	for _, want := range []string{
		`lexi_update_duration_seconds_bucket{le="0.01"} 1`,
		`lexi_update_duration_seconds_bucket{le="0.1"} 2`,
		`lexi_update_duration_seconds_bucket{le="1"} 2`,
		`lexi_update_duration_seconds_bucket{le="+Inf"} 3`,
		"lexi_update_duration_seconds_sum 2.055",
		"lexi_update_duration_seconds_count 3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("вывод не содержит %q:\n%s", want, got)
		}
	}
}

func TestHistogramDefaultBuckets(t *testing.T) {
	t.Parallel()

	r := metrics.New()
	h := r.NewHistogram("lexi_db_duration_seconds", "Задержки БД", nil)
	h.Observe(0.002)

	got := render(t, r)
	if !strings.Contains(got, `le="0.005"`) {
		t.Errorf("границы по умолчанию не применились:\n%s", got)
	}
}

func TestOutputIsOrderedByName(t *testing.T) {
	t.Parallel()

	r := metrics.New()
	r.NewCounter("zeta_total", "Последняя").Inc()
	r.NewCounter("alpha_total", "Первая").Inc()

	// Одинаковый вывод при одинаковом состоянии проще читать глазами
	// и сравнивать в тесте.
	got := render(t, r)
	if strings.Index(got, "alpha_total") > strings.Index(got, "zeta_total") {
		t.Errorf("метрики не упорядочены:\n%s", got)
	}
}

func TestLabelValuesAreEscaped(t *testing.T) {
	t.Parallel()

	r := metrics.New()
	errs := r.NewCounter("lexi_errors_total", "Ошибки", "reason")
	errs.Inc(`кавычка " и слеш \`)

	got := render(t, r)
	if !strings.Contains(got, `reason="кавычка \" и слеш \\"`) {
		t.Errorf("значение метки не экранировано:\n%s", got)
	}
}

func TestConcurrentUpdates(t *testing.T) {
	t.Parallel()

	r := metrics.New()
	counter := r.NewCounter("lexi_updates_total", "Апдейты", "kind")
	h := r.NewHistogram("lexi_update_duration_seconds", "Обработка", nil)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			counter.Inc("message")
			h.Observe(0.01)
		}()
	}
	wg.Wait()

	got := render(t, r)
	if !strings.Contains(got, `lexi_updates_total{kind="message"} 50`) {
		t.Errorf("счётчик потерял значения:\n%s", got)
	}
	if !strings.Contains(got, "lexi_update_duration_seconds_count 50") {
		t.Errorf("гистограмма потеряла измерения:\n%s", got)
	}
}
