// Package metrics собирает счётчики приложения и отдаёт их в текстовом
// формате Prometheus.
//
// Без клиентской библиотеки: нам нужны счётчики, датчики и одна гистограмма,
// а формат экспозиции — это несколько строк на метрику. Библиотека принесла
// бы protobuf, сбор системных метрик и своё дерево зависимостей ради того,
// что укладывается в двести строк и покрывается тестом целиком. Образ бота
// при этом остаётся маленьким, а он у нас считанных мегабайт (T-050).
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// DefaultBuckets — границы гистограмм в секундах.
//
// От миллисекунды до пяти: быстрее миллисекунды не бывает ничего, что стоит
// мерить, а всё, что дольше пяти секунд, одинаково плохо, и различать там
// нечего.
var DefaultBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

// collector — то, что умеет записать себя в текстовом формате.
type collector interface {
	name() string
	writeTo(w io.Writer) error
}

// Registry хранит метрики приложения.
type Registry struct {
	mu         sync.RWMutex
	collectors []collector
}

// New создаёт пустой набор метрик.
func New() *Registry { return &Registry{} }

// Counter — счётчик, который только растёт.
type Counter struct {
	desc   description
	labels []string

	mu     sync.Mutex
	values map[string]*atomic.Int64
}

// description — общая часть метрики: имя, пояснение и тип.
type description struct {
	metricName string
	help       string
	kind       string
}

// NewCounter заводит счётчик. Метки перечисляются здесь, а значения к ним —
// при каждом увеличении.
func (r *Registry) NewCounter(name, help string, labels ...string) *Counter {
	c := &Counter{
		desc:   description{metricName: name, help: help, kind: "counter"},
		labels: labels,
		values: map[string]*atomic.Int64{},
	}
	r.add(c)
	return c
}

// Inc увеличивает счётчик на единицу.
//
// Значения меток идут в том же порядке, в каком метки объявлены. Лишние
// и недостающие молча отбрасываются: метрика — не тот повод, из-за которого
// стоит ронять обработку апдейта.
func (c *Counter) Inc(values ...string) { c.Add(1, values...) }

// Add увеличивает счётчик на заданную величину.
func (c *Counter) Add(delta int64, values ...string) {
	if len(values) != len(c.labels) {
		return
	}
	c.value(values).Add(delta)
}

func (c *Counter) value(values []string) *atomic.Int64 {
	key := strings.Join(values, "\x00")

	c.mu.Lock()
	defer c.mu.Unlock()

	if v, ok := c.values[key]; ok {
		return v
	}
	v := &atomic.Int64{}
	c.values[key] = v
	return v
}

func (c *Counter) name() string { return c.desc.metricName }

func (c *Counter) writeTo(w io.Writer) error {
	c.mu.Lock()
	keys := make([]string, 0, len(c.values))
	for key := range c.values {
		keys = append(keys, key)
	}
	snapshot := make(map[string]int64, len(c.values))
	for key, v := range c.values {
		snapshot[key] = v.Load()
	}
	c.mu.Unlock()

	sort.Strings(keys)
	if err := writeHeader(w, &c.desc); err != nil {
		return err
	}
	for _, key := range keys {
		labels := ""
		if len(c.labels) > 0 {
			labels = renderLabels(c.labels, strings.Split(key, "\x00"))
		}
		if _, err := fmt.Fprintf(w, "%s%s %d\n", c.desc.metricName, labels, snapshot[key]); err != nil {
			return err
		}
	}
	return nil
}

// Gauge — величина, которая ходит вверх и вниз: занятые соединения пула,
// длина очереди.
type Gauge struct {
	desc description
	// read вызывается в момент отдачи метрик: датчик спрашивает значение
	// у источника, а не хранит копию, которая успеет устареть.
	read func() float64
}

// NewGauge заводит датчик, читающий значение на месте.
func (r *Registry) NewGauge(name, help string, read func() float64) *Gauge {
	g := &Gauge{desc: description{metricName: name, help: help, kind: "gauge"}, read: read}
	r.add(g)
	return g
}

func (g *Gauge) name() string { return g.desc.metricName }

func (g *Gauge) writeTo(w io.Writer) error {
	if err := writeHeader(w, &g.desc); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "%s %s\n", g.desc.metricName, formatFloat(g.read()))
	return err
}

// Histogram — распределение длительностей по корзинам.
type Histogram struct {
	desc    description
	buckets []float64

	mu     sync.Mutex
	counts []int64
	sum    float64
	total  int64
}

// NewHistogram заводит гистограмму. Пустой набор границ означает DefaultBuckets.
func (r *Registry) NewHistogram(name, help string, buckets []float64) *Histogram {
	if len(buckets) == 0 {
		buckets = DefaultBuckets
	}
	h := &Histogram{
		desc:    description{metricName: name, help: help, kind: "histogram"},
		buckets: buckets,
		counts:  make([]int64, len(buckets)),
	}
	r.add(h)
	return h
}

// Observe записывает измерение в секундах.
func (h *Histogram) Observe(seconds float64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for i, edge := range h.buckets {
		if seconds <= edge {
			h.counts[i]++
		}
	}
	h.sum += seconds
	h.total++
}

func (h *Histogram) name() string { return h.desc.metricName }

func (h *Histogram) writeTo(w io.Writer) error {
	h.mu.Lock()
	counts := make([]int64, len(h.counts))
	copy(counts, h.counts)
	sum, total := h.sum, h.total
	h.mu.Unlock()

	if err := writeHeader(w, &h.desc); err != nil {
		return err
	}
	// Корзины накопительные: в le="0.01" входит всё, что попало и в 0.005.
	// Считаем их такими сразу при записи наблюдения, поэтому здесь
	// достаточно вывести как есть.
	for i, edge := range h.buckets {
		if _, err := fmt.Fprintf(w, "%s_bucket{le=%q} %d\n",
			h.desc.metricName, formatFloat(edge), counts[i]); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "%s_bucket{le=\"+Inf\"} %d\n", h.desc.metricName, total); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "%s_sum %s\n", h.desc.metricName, formatFloat(sum)); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "%s_count %d\n", h.desc.metricName, total)
	return err
}

// Expose отдаёт все метрики в текстовом формате Prometheus.
//
// Порядок по имени: одинаковый вывод при одинаковом состоянии проще читать
// глазами и сравнивать в тесте.
func (r *Registry) Expose(w io.Writer) error {
	r.mu.RLock()
	collectors := make([]collector, len(r.collectors))
	copy(collectors, r.collectors)
	r.mu.RUnlock()

	sort.Slice(collectors, func(i, j int) bool {
		return collectors[i].name() < collectors[j].name()
	})
	for _, c := range collectors {
		if err := c.writeTo(w); err != nil {
			return err
		}
	}
	return nil
}

func (r *Registry) add(c collector) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.collectors = append(r.collectors, c)
}

func writeHeader(w io.Writer, d *description) error {
	if _, err := fmt.Fprintf(w, "# HELP %s %s\n", d.metricName, d.help); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "# TYPE %s %s\n", d.metricName, d.kind)
	return err
}

// renderLabels собирает часть строки с метками.
func renderLabels(names, values []string) string {
	// Кавычки ставятся вручную, а не через %q: тот экранирует сам,
	// и вместе с нашим escape получилось бы двойное экранирование.
	pairs := make([]string, 0, len(names))
	for i, name := range names {
		pairs = append(pairs, name+`="`+escape(values[i])+`"`)
	}
	return "{" + strings.Join(pairs, ",") + "}"
}

// escape готовит значение метки к выводу: формат не терпит в них кавычек,
// обратных слешей и переводов строки.
func escape(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return replacer.Replace(value)
}

// formatFloat печатает число без лишних нулей: 0.005, а не 0.005000.
func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}
