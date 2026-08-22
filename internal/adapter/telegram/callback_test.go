package telegram_test

import (
	"math/rand"
	"strings"
	"testing"

	"lexi-bot/internal/adapter/telegram"
)

func TestCallbackRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   telegram.Callback
		want string
	}{
		{"только действие", telegram.Callback{Action: "learn"}, "learn"},
		{"действие и идентификатор", telegram.Callback{Action: "deck", ID: 42}, "deck:42"},
		{"все три части", telegram.Callback{Action: "rate", ID: 42, Param: "good"}, "rate:42:good"},
		{"параметр без идентификатора", telegram.Callback{Action: "lang", Param: "ru"}, "lang:0:ru"},
		{"отрицательный идентификатор", telegram.Callback{Action: "chat", ID: -100500}, "chat:-100500"},
		{"двоеточия внутри параметра", telegram.Callback{Action: "set", ID: 1, Param: "time:21:30"}, "set:1:time:21:30"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			encoded, err := tt.in.Encode()
			if err != nil {
				t.Fatalf("Encode() вернул ошибку: %v", err)
			}
			if encoded != tt.want {
				t.Errorf("Encode() = %q, ожидалось %q", encoded, tt.want)
			}

			back, err := telegram.DecodeCallback(encoded)
			if err != nil {
				t.Fatalf("DecodeCallback(%q) вернул ошибку: %v", encoded, err)
			}
			if back != tt.in {
				t.Errorf("после разбора %+v, ожидалось %+v", back, tt.in)
			}
		})
	}
}

func TestCallbackLengthLimitCountsBytes(t *testing.T) {
	t.Parallel()

	// Предел Telegram — 64 байта, а не символа. Кириллица стоит по два
	// байта на букву, и наивная проверка по длине строки в рунах пропустила
	// бы вдвое больше, чем влезает.
	cyrillic := telegram.Callback{Action: "set", Param: strings.Repeat("я", 31)}
	if _, err := cyrillic.Encode(); err == nil {
		t.Error("62 байта параметра плюс служебные части должны не влезать")
	}

	latin := telegram.Callback{Action: "set", Param: strings.Repeat("a", 31)}
	encoded, err := latin.Encode()
	if err != nil {
		t.Fatalf("та же длина латиницей должна влезать: %v", err)
	}
	if len(encoded) > telegram.MaxCallbackDataLen {
		t.Errorf("длина %d байт превысила предел", len(encoded))
	}
}

func TestCallbackEncodeRejectsBrokenInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   telegram.Callback
	}{
		{"без действия", telegram.Callback{Param: "good"}},
		{"разделитель в действии", telegram.Callback{Action: "rate:good"}},
		{"слишком длинное", telegram.Callback{Action: "rate", ID: 1, Param: strings.Repeat("x", 64)}},
		{"битый UTF-8 в параметре", telegram.Callback{Action: "rate", Param: string([]byte{0xff, 0xfe})}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got, err := tt.in.Encode(); err == nil {
				t.Errorf("Encode() = %q, ожидалась ошибка", got)
			}
		})
	}
}

func TestDecodeCallbackRejectsGarbage(t *testing.T) {
	t.Parallel()

	// Данные приходят из внешнего мира: кнопка могла остаться от прошлой
	// версии бота или быть подделана.
	tests := []string{
		"",
		":42:good",
		"rate:не число:good",
		strings.Repeat("x", telegram.MaxCallbackDataLen+1),
	}

	for _, data := range tests {
		if got, err := telegram.DecodeCallback(data); err == nil {
			t.Errorf("DecodeCallback(%q) = %+v, ожидалась ошибка", data, got)
		}
	}
}

func TestDecodeCallbackTolerantToEmptyParts(t *testing.T) {
	t.Parallel()

	// Кнопки, собранные вручную или прошлой версией, могут содержать
	// пустые части. Разбор не должен на этом спотыкаться.
	got, err := telegram.DecodeCallback("rate::good")
	if err != nil {
		t.Fatalf("DecodeCallback() вернул ошибку: %v", err)
	}
	if got.Action != "rate" || got.ID != 0 || got.Param != "good" {
		t.Errorf("разобрано %+v", got)
	}
}

func TestCallbackRoundTripProperty(t *testing.T) {
	t.Parallel()

	// Свойство: всё, что закодировалось, разбирается обратно в то же самое.
	// Источник случайности с фиксированным зерном — чтобы упавший тест
	// можно было повторить.
	random := rand.New(rand.NewSource(20260822))
	alphabet := []rune("abcdefghijklmnopqrstuvwxyz0123456789_-:.,абвгд ")

	text := func(maxLen int) string {
		var b strings.Builder
		for i := 0; i < random.Intn(maxLen+1); i++ {
			b.WriteRune(alphabet[random.Intn(len(alphabet))])
		}
		return b.String()
	}

	encoded, skipped := 0, 0
	for i := 0; i < 2000; i++ {
		want := telegram.Callback{
			Action: strings.ReplaceAll(text(8), ":", ""),
			ID:     random.Int63n(2_000_001) - 1_000_000,
			Param:  text(30),
		}

		data, err := want.Encode()
		if err != nil {
			// Слишком длинные и пустые действия отвергаются — это тоже
			// часть контракта, но обратимость проверяется на остальных.
			skipped++
			continue
		}
		encoded++

		back, err := telegram.DecodeCallback(data)
		if err != nil {
			t.Fatalf("DecodeCallback(%q) вернул ошибку: %v (исходное %+v)", data, err, want)
		}
		if back != want {
			t.Fatalf("%+v → %q → %+v", want, data, back)
		}
	}

	if encoded < 1000 {
		t.Fatalf("закодировалось только %d значений из 2000 (отвергнуто %d): тест почти ничего не проверил",
			encoded, skipped)
	}
}
