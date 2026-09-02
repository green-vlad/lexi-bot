// Package health поднимает служебный HTTP-сервер: проверку живости
// и метрики.
//
// На петлевом интерфейсе: наружу отдавать нечего, а внутренние счётчики
// в открытом доступе — приглашение посчитать, сколько у бота людей.
package health

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Таймауты служебного сервера. Он отвечает мгновенно, и держать соединение
// дольше нескольких секунд незачем.
const (
	readTimeout     = 5 * time.Second
	writeTimeout    = 10 * time.Second
	shutdownTimeout = 5 * time.Second
)

// StaleAfter — через сколько молчания опрос Telegram считается зависшим.
//
// Заметно больше периода опроса: длинный запрос сам по себе висит
// до тридцати секунд, и объявлять бота больным на тридцать первой значило бы
// поднимать тревогу на ровном месте.
const StaleAfter = 3 * time.Minute

// Probe — проверка, от которой зависит живость. Ошибка означает, что бот
// работать не может.
type Probe func(ctx context.Context) error

// Config — из чего собирается служебный сервер.
type Config struct {
	Addr string
	// Ping проверяет базу. Без неё бот бесполезен: ни карточку показать,
	// ни ответ записать.
	Ping Probe
	// Metrics пишет метрики в текстовом формате.
	Metrics func(w io.Writer) error
	Clock   func() time.Time
	Logger  *slog.Logger
}

// Server — служебный HTTP-сервер.
type Server struct {
	http    *http.Server
	log     *slog.Logger
	clock   func() time.Time
	ping    Probe
	metrics func(w io.Writer) error

	mu       sync.RWMutex
	lastPoll time.Time
}

// New создаёт сервер.
func New(cfg Config) (*Server, error) {
	switch {
	case cfg.Addr == "":
		return nil, errors.New("служебному серверу нужен адрес")
	case cfg.Ping == nil:
		return nil, errors.New("служебному серверу нужна проверка базы")
	case cfg.Clock == nil:
		return nil, errors.New("служебному серверу нужны часы")
	case cfg.Logger == nil:
		return nil, errors.New("служебному серверу нужен логгер")
	}

	s := &Server{
		log: cfg.Logger, clock: cfg.Clock,
		ping: cfg.Ping, metrics: cfg.Metrics,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	if cfg.Metrics != nil {
		mux.HandleFunc("GET /metrics", s.serveMetrics)
	}

	s.http = &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: readTimeout,
		WriteTimeout:      writeTimeout,
	}
	return s, nil
}

// PollSucceeded отмечает удачный цикл опроса Telegram.
//
// Момент нужен затем, чтобы отличить живой процесс от повисшего: бот может
// отвечать на /healthz и при этом давно не получать апдейтов — например,
// если у него отобрали токен или его вытеснил второй инстанс.
func (s *Server) PollSucceeded() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastPoll = s.clock()
}

// Start поднимает сервер и возвращается сразу.
//
// Ошибка запуска не роняет бота: занятый порт метрик — досадно, но это
// не повод оставить людей без занятий.
func (s *Server) Start(ctx context.Context) {
	var config net.ListenConfig
	listener, err := config.Listen(ctx, "tcp", s.http.Addr)
	if err != nil {
		s.log.Error("служебный сервер не поднялся",
			slog.String("addr", s.http.Addr), slog.Any("error", err))
		return
	}

	go func() {
		if err := s.http.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("служебный сервер остановился", slog.Any("error", err))
		}
	}()
	s.log.Info("служебный сервер слушает", slog.String("addr", s.http.Addr))
}

// Stop останавливает сервер, дав начатым запросам договорить.
func (s *Server) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := s.http.Shutdown(ctx); err != nil {
		s.log.Warn("служебный сервер закрылся с ошибкой", slog.Any("error", err))
	}
}

// healthz отвечает на проверку живости.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	problems := s.check(r.Context())

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if len(problems) > 0 {
		// 503, а не 500: бот не сломан, он временно не может работать,
		// и внешний пинг должен отличать одно от другого.
		w.WriteHeader(http.StatusServiceUnavailable)
		s.write(w, strings.Join(problems, "\n"))
		return
	}
	s.write(w, "ok")
}

// write отдаёт тело ответа. Оборванное соединение здесь — не повод для
// шума: проверяющий ушёл, и рассказывать об этом некому.
func (s *Server) write(w io.Writer, text string) {
	if _, err := io.WriteString(w, text+"\n"); err != nil {
		s.log.Debug("ответ проверки живости не дописан", slog.Any("error", err))
	}
}

// check собирает все жалобы разом.
//
// Все, а не первую: если легли и база, и опрос, человеку у пульта полезнее
// увидеть обе строки, чем узнавать о второй после починки первой.
func (s *Server) check(ctx context.Context) []string {
	var problems []string

	if err := s.ping(ctx); err != nil {
		problems = append(problems, "база недоступна: "+err.Error())
	}

	s.mu.RLock()
	last := s.lastPoll
	s.mu.RUnlock()

	switch {
	case last.IsZero():
		// Бот только что поднялся и ещё не сделал ни одного цикла.
		// Это не болезнь, а ожидание первого апдейта.
	case s.clock().Sub(last) > StaleAfter:
		problems = append(problems,
			fmt.Sprintf("опрос Telegram молчит с %s", last.Format(time.RFC3339)))
	}
	return problems
}

// serveMetrics отдаёт метрики.
func (s *Server) serveMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if err := s.metrics(w); err != nil {
		s.log.Error("не удалось отдать метрики", slog.Any("error", err))
	}
}
