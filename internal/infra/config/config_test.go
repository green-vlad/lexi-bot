package config_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"lexi-bot/internal/infra/config"
)

const (
	validToken = "123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw"
	validDSN   = "postgres://lexi:secret@localhost:5432/lexi?sslmode=disable"
)

// env превращает карту в config.Lookup, поэтому тесты не трогают окружение процесса
// и могут выполняться параллельно.
func env(pairs map[string]string) config.Lookup {
	return func(key string) (string, bool) {
		v, ok := pairs[key]
		return v, ok
	}
}

func minimalEnv() map[string]string {
	return map[string]string{
		"BOT_TOKEN":    validToken,
		"DATABASE_URL": validDSN,
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(env(minimalEnv()))
	if err != nil {
		t.Fatalf("Load() вернул ошибку: %v", err)
	}

	if cfg.Env != config.EnvDev {
		t.Errorf("Env = %q, ожидалось %q", cfg.Env, config.EnvDev)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, ожидался info", cfg.LogLevel)
	}
	if cfg.HTTPAddr != "127.0.0.1:8080" {
		t.Errorf("HTTPAddr = %q, ожидался петлевой интерфейс", cfg.HTTPAddr)
	}
	if cfg.DefaultTimezone != time.UTC {
		t.Errorf("DefaultTimezone = %v, ожидался UTC", cfg.DefaultTimezone)
	}
	if cfg.AdminChatID != 0 {
		t.Errorf("AdminChatID = %d, ожидался 0 (алерты выключены)", cfg.AdminChatID)
	}
	if cfg.PollTimeout != 30*time.Second {
		t.Errorf("PollTimeout = %v, ожидалось 30s", cfg.PollTimeout)
	}
}

func TestLoadFull(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(env(map[string]string{
		"APP_ENV":          "prod",
		"BOT_TOKEN":        validToken,
		"DATABASE_URL":     validDSN,
		"LOG_LEVEL":        "debug",
		"HTTP_ADDR":        "0.0.0.0:9090",
		"DEFAULT_TIMEZONE": "Europe/Moscow",
		"ADMIN_CHAT_ID":    "-1001234567890",
	}))
	if err != nil {
		t.Fatalf("Load() вернул ошибку: %v", err)
	}

	if cfg.Env != config.EnvProd {
		t.Errorf("Env = %q, ожидалось prod", cfg.Env)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, ожидался debug", cfg.LogLevel)
	}
	if cfg.HTTPAddr != "0.0.0.0:9090" {
		t.Errorf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.DefaultTimezone.String() != "Europe/Moscow" {
		t.Errorf("DefaultTimezone = %v", cfg.DefaultTimezone)
	}
	if cfg.AdminChatID != -1001234567890 {
		t.Errorf("AdminChatID = %d", cfg.AdminChatID)
	}
}

func TestLoadValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(map[string]string)
		wantKey string
	}{
		{
			name:    "нет токена",
			mutate:  func(m map[string]string) { delete(m, "BOT_TOKEN") },
			wantKey: "BOT_TOKEN",
		},
		{
			name:    "пустой токен",
			mutate:  func(m map[string]string) { m["BOT_TOKEN"] = "   " },
			wantKey: "BOT_TOKEN",
		},
		{
			name:    "токен без двоеточия",
			mutate:  func(m map[string]string) { m["BOT_TOKEN"] = "простоСтрока" },
			wantKey: "BOT_TOKEN",
		},
		{
			name:    "нечисловой id в токене",
			mutate:  func(m map[string]string) { m["BOT_TOKEN"] = "abc:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw" },
			wantKey: "BOT_TOKEN",
		},
		{
			name:    "слишком короткий секрет в токене",
			mutate:  func(m map[string]string) { m["BOT_TOKEN"] = "123456789:short" },
			wantKey: "BOT_TOKEN",
		},
		{
			name:    "нет строки подключения",
			mutate:  func(m map[string]string) { delete(m, "DATABASE_URL") },
			wantKey: "DATABASE_URL",
		},
		{
			name:    "чужая схема в строке подключения",
			mutate:  func(m map[string]string) { m["DATABASE_URL"] = "mysql://localhost:3306/lexi" },
			wantKey: "DATABASE_URL",
		},
		{
			name:    "строка подключения без хоста",
			mutate:  func(m map[string]string) { m["DATABASE_URL"] = "postgres:///lexi" },
			wantKey: "DATABASE_URL",
		},
		{
			name:    "неизвестный уровень логирования",
			mutate:  func(m map[string]string) { m["LOG_LEVEL"] = "verbose" },
			wantKey: "LOG_LEVEL",
		},
		{
			name:    "неизвестная таймзона",
			mutate:  func(m map[string]string) { m["DEFAULT_TIMEZONE"] = "Europe/Атлантида" },
			wantKey: "DEFAULT_TIMEZONE",
		},
		{
			name:    "нечисловой admin chat id",
			mutate:  func(m map[string]string) { m["ADMIN_CHAT_ID"] = "@admin" },
			wantKey: "ADMIN_CHAT_ID",
		},
		{
			name:    "неизвестный режим запуска",
			mutate:  func(m map[string]string) { m["APP_ENV"] = "staging" },
			wantKey: "APP_ENV",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vars := minimalEnv()
			tt.mutate(vars)

			_, err := config.Load(env(vars))
			if err == nil {
				t.Fatal("ожидалась ошибка, получено nil")
			}
			if !strings.Contains(err.Error(), tt.wantKey) {
				t.Errorf("ошибка не называет переменную %s:\n%v", tt.wantKey, err)
			}
		})
	}
}

// Ошибки должны собираться все сразу: при первом развёртывании неприятно
// выяснять их по одной, перезапуская процесс.
func TestLoadReportsAllProblemsAtOnce(t *testing.T) {
	t.Parallel()

	_, err := config.Load(env(map[string]string{
		"LOG_LEVEL": "verbose",
	}))
	if err == nil {
		t.Fatal("ожидалась ошибка, получено nil")
	}

	for _, key := range []string{"BOT_TOKEN", "DATABASE_URL", "LOG_LEVEL"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("в ошибке нет упоминания %s:\n%v", key, err)
		}
	}
}

func TestSecretsAreNotExposed(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(env(minimalEnv()))
	if err != nil {
		t.Fatalf("Load() вернул ошибку: %v", err)
	}

	rendered := cfg.String() + cfg.LogValue().String()
	if strings.Contains(rendered, validToken) {
		t.Error("токен попал в текстовое представление конфигурации")
	}
	if strings.Contains(rendered, "secret") {
		t.Error("пароль от базы попал в текстовое представление конфигурации")
	}
	if !strings.Contains(rendered, config.Redacted) {
		t.Error("ожидалась подстановка маркера скрытого значения")
	}
}

func TestRedactDSN(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{
			name: "пароль вырезан",
			dsn:  "postgres://lexi:secret@db:5432/lexi",
			want: "postgres://lexi:" + config.Redacted + "@db:5432/lexi",
		},
		{
			name: "без пароля строка не меняется",
			dsn:  "postgres://lexi@db:5432/lexi",
			want: "postgres://lexi@db:5432/lexi",
		},
		{
			name: "без пользователя строка не меняется",
			dsn:  "postgres://db:5432/lexi",
			want: "postgres://db:5432/lexi",
		},
		{
			name: "пустая строка",
			dsn:  "",
			want: "",
		},
		{
			name: "неразбираемая строка скрывается целиком",
			dsn:  "postgres://user:pass@[::1:5432/lexi",
			want: config.Redacted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := config.RedactDSN(tt.dsn); got != tt.want {
				t.Errorf("RedactDSN(%q) = %q, ожидалось %q", tt.dsn, got, tt.want)
			}
		})
	}
}
