package logger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"lexi-bot/internal/infra/logger"
)

func TestNewJSONFormat(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := logger.New(&buf, slog.LevelInfo, logger.FormatJSON)
	log.Info("привет", slog.String("key", "value"))

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("вывод не является JSON: %v (%q)", err, buf.String())
	}
	if record["msg"] != "привет" {
		t.Errorf("msg = %v", record["msg"])
	}
	if record["key"] != "value" {
		t.Errorf("key = %v", record["key"])
	}
}

func TestNewTextFormat(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := logger.New(&buf, slog.LevelInfo, logger.FormatText)
	log.Info("привет")

	if json.Valid(bytes.TrimSpace(buf.Bytes())) {
		t.Error("ожидался текстовый формат, получен JSON")
	}
	if !strings.Contains(buf.String(), "привет") {
		t.Errorf("сообщение не попало в вывод: %q", buf.String())
	}
}

func TestLevelFiltering(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := logger.New(&buf, slog.LevelWarn, logger.FormatJSON)

	log.Debug("отладка")
	log.Info("информация")
	if buf.Len() != 0 {
		t.Errorf("записи ниже порога попали в вывод: %q", buf.String())
	}

	log.Warn("предупреждение")
	if buf.Len() == 0 {
		t.Error("запись на уровне порога не попала в вывод")
	}
}

func TestFromContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		ctx    func() context.Context
		want   map[string]any
		absent []string
	}{
		{
			name:   "пустой контекст не добавляет атрибутов",
			ctx:    context.Background,
			want:   map[string]any{},
			absent: []string{"user_id", "update_id"},
		},
		{
			name: "только пользователь",
			ctx: func() context.Context {
				return logger.WithUserID(context.Background(), 42)
			},
			want:   map[string]any{"user_id": float64(42)},
			absent: []string{"update_id"},
		},
		{
			name: "пользователь и апдейт",
			ctx: func() context.Context {
				ctx := logger.WithUserID(context.Background(), 42)
				return logger.WithUpdateID(ctx, 777)
			},
			want: map[string]any{"user_id": float64(42), "update_id": float64(777)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			base := logger.New(&buf, slog.LevelInfo, logger.FormatJSON)
			logger.FromContext(tt.ctx(), base).Info("событие")

			var record map[string]any
			if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
				t.Fatalf("вывод не является JSON: %v", err)
			}
			for key, want := range tt.want {
				if record[key] != want {
					t.Errorf("%s = %v, ожидалось %v", key, record[key], want)
				}
			}
			for _, key := range tt.absent {
				if _, ok := record[key]; ok {
					t.Errorf("неожиданный атрибут %s в записи", key)
				}
			}
		})
	}
}
