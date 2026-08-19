// Package config читает конфигурацию приложения из переменных окружения.
//
// Все значения проверяются один раз при старте: приложение либо получает валидный
// Config, либо не запускается вовсе. Ошибки собираются вместе, чтобы при первом
// развёртывании не выяснять их по одной.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Lookup возвращает значение переменной окружения и признак того, что она задана.
// Сигнатура совпадает с os.LookupEnv, поэтому тесты подставляют собственное
// окружение, не трогая переменные процесса.
type Lookup func(key string) (string, bool)

// Env различает режимы запуска: в dev логи человекочитаемые, в prod — JSON.
type Env string

// Поддерживаемые режимы запуска.
const (
	EnvDev  Env = "dev"
	EnvProd Env = "prod"
)

// Redacted подставляется вместо секретов при выводе конфигурации в лог.
const Redacted = "***"

// Значения по умолчанию. HTTPAddr слушает только петлевой интерфейс: /healthz
// и /metrics наружу не публикуются, к ним ходят с самого хоста.
const (
	defaultHTTPAddr    = "127.0.0.1:8080"
	defaultLogLevel    = slog.LevelInfo
	defaultTimezone    = "UTC"
	defaultPollTimeout = 30 * time.Second
	defaultEnv         = EnvDev
)

// Config — полная конфигурация приложения.
type Config struct {
	Env         Env
	BotToken    string
	DatabaseURL string
	LogLevel    slog.Level
	HTTPAddr    string
	// DefaultTimezone используется для пользователей, которые ещё не выбрали свою.
	DefaultTimezone *time.Location
	// AdminChatID — чат для алертов. Ноль означает, что алерты выключены.
	AdminChatID int64
	// PollTimeout — таймаут длинного опроса getUpdates.
	PollTimeout time.Duration
}

// Load собирает конфигурацию из окружения и проверяет её целиком.
// Возвращаемая ошибка содержит все обнаруженные проблемы, а не только первую.
func Load(lookup Lookup) (Config, error) {
	var errs []error
	problem := func(key string, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", key, fmt.Sprintf(format, args...)))
	}

	cfg := Config{
		HTTPAddr:    optional(lookup, "HTTP_ADDR", defaultHTTPAddr),
		PollTimeout: defaultPollTimeout,
	}

	// Env
	switch env := Env(optional(lookup, "APP_ENV", string(defaultEnv))); env {
	case EnvDev, EnvProd:
		cfg.Env = env
	default:
		problem("APP_ENV", "ожидалось %q или %q, получено %q", EnvDev, EnvProd, env)
	}

	// BotToken
	token := strings.TrimSpace(optional(lookup, "BOT_TOKEN", ""))
	switch {
	case token == "":
		problem("BOT_TOKEN", "обязательная переменная не задана, токен выдаёт @BotFather")
	case !looksLikeBotToken(token):
		problem("BOT_TOKEN", "не похоже на токен Telegram, ожидался формат <id>:<секрет>")
	default:
		cfg.BotToken = token
	}

	// DatabaseURL
	dsn := strings.TrimSpace(optional(lookup, "DATABASE_URL", ""))
	switch err := validateDSN(dsn); {
	case dsn == "":
		problem("DATABASE_URL", "обязательная переменная не задана")
	case err != nil:
		problem("DATABASE_URL", "%v", err)
	default:
		cfg.DatabaseURL = dsn
	}

	// LogLevel
	cfg.LogLevel = defaultLogLevel
	if raw, ok := lookup("LOG_LEVEL"); ok && strings.TrimSpace(raw) != "" {
		var level slog.Level
		if err := level.UnmarshalText([]byte(strings.TrimSpace(raw))); err != nil {
			problem("LOG_LEVEL", "ожидалось debug, info, warn или error, получено %q", raw)
		} else {
			cfg.LogLevel = level
		}
	}

	// Timezone
	tzName := optional(lookup, "DEFAULT_TIMEZONE", defaultTimezone)
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		problem("DEFAULT_TIMEZONE", "неизвестная зона %q, ожидалось имя IANA вида Europe/Moscow", tzName)
	} else {
		cfg.DefaultTimezone = loc
	}

	// AdminChatID — необязательный.
	if raw, ok := lookup("ADMIN_CHAT_ID"); ok && strings.TrimSpace(raw) != "" {
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			problem("ADMIN_CHAT_ID", "ожидалось целое число, получено %q", raw)
		} else {
			cfg.AdminChatID = id
		}
	}

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("конфигурация некорректна:\n%w", errors.Join(errs...))
	}
	return cfg, nil
}

// LogValue отдаёт slog безопасное представление конфигурации: токен скрыт целиком,
// а из строки подключения вырезан пароль. Метод существует ровно для того, чтобы
// секрет нельзя было залогировать случайно, передав Config целиком.
//
// Получатель обязан быть значением, а не указателем: иначе slog не вызовет этот
// метод для Config, переданного по значению, и секреты утекут в лог.
//
//nolint:gocritic // hugeParam здесь неизбежен, см. комментарий выше.
func (c Config) LogValue() slog.Value {
	tz := defaultTimezone
	if c.DefaultTimezone != nil {
		tz = c.DefaultTimezone.String()
	}
	return slog.GroupValue(
		slog.String("env", string(c.Env)),
		slog.String("bot_token", Redacted),
		slog.String("database_url", RedactDSN(c.DatabaseURL)),
		slog.String("log_level", c.LogLevel.String()),
		slog.String("http_addr", c.HTTPAddr),
		slog.String("default_timezone", tz),
		slog.Int64("admin_chat_id", c.AdminChatID),
		slog.Duration("poll_timeout", c.PollTimeout),
	)
}

// String скрывает секреты так же, как LogValue: fmt.Sprintf("%v", cfg) безопасен.
//
//nolint:gocritic // hugeParam: получатель обязан быть значением, как и у LogValue.
func (c Config) String() string {
	return c.LogValue().String()
}

// RedactDSN заменяет пароль в строке подключения. Непарсящуюся строку возвращает
// скрытой целиком: раз структура неизвестна, безопаснее не показывать ничего.
func RedactDSN(dsn string) string {
	if dsn == "" {
		return ""
	}
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil {
		if err != nil {
			return Redacted
		}
		return dsn
	}
	if _, hasPassword := u.User.Password(); !hasPassword {
		return dsn
	}
	// Подменяем часть с учётными данными в исходной строке, а не пересобираем URL
	// через u.String(): тот процентно кодирует маркер и уродует остальную строку.
	return strings.Replace(dsn, u.User.String(), url.User(u.User.Username()).String()+":"+Redacted, 1)
}

func optional(lookup Lookup, key, fallback string) string {
	if v, ok := lookup(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}

// looksLikeBotToken проверяет форму токена <числовой id>:<секрет>. Точная валидация
// невозможна, но эта проверка ловит самую частую ошибку — подставленное не то значение.
func looksLikeBotToken(token string) bool {
	id, secret, found := strings.Cut(token, ":")
	if !found || id == "" || len(secret) < 20 {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func validateDSN(dsn string) error {
	u, err := url.Parse(dsn)
	if err != nil {
		return fmt.Errorf("строка подключения не разбирается: %w", err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return fmt.Errorf("ожидалась схема postgres://, получена %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("не указан хост")
	}
	return nil
}
