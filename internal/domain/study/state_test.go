package study_test

import (
	"errors"
	"strings"
	"testing"

	"lexi-bot/internal/domain/study"
)

func TestStateRoundTrip(t *testing.T) {
	t.Parallel()

	// Фаза хранится в базе строкой, поэтому разбор обязан быть обратным
	// к String() для каждого значения.
	for _, s := range study.States() {
		name := s.String()
		back, err := study.ParseState(name)
		if err != nil {
			t.Fatalf("ParseState(%q) вернул ошибку: %v", name, err)
		}
		if back != s {
			t.Errorf("ParseState(%q) = %v, ожидалось %v", name, back, s)
		}
	}
}

func TestStateNames(t *testing.T) {
	t.Parallel()

	want := map[study.State]string{
		study.StateNew:        "new",
		study.StateLearning:   "learning",
		study.StateReview:     "review",
		study.StateRelearning: "relearning",
		study.StateSuspended:  "suspended",
	}
	for s, name := range want {
		if s.String() != name {
			t.Errorf("String() = %q, ожидалось %q", s.String(), name)
		}
	}
}

func TestStateZeroValueIsNew(t *testing.T) {
	t.Parallel()

	// Нулевое значение — новая карточка: свежесозданный CardState корректен
	// без единого присваивания фазы.
	var zero study.State
	if zero != study.StateNew {
		t.Errorf("нулевое значение = %v, ожидалось new", zero)
	}
	if !zero.IsValid() {
		t.Error("нулевое значение должно быть допустимой фазой")
	}
}

func TestParseStateNormalizes(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"relearning", "RELEARNING", "  Relearning "} {
		got, err := study.ParseState(in)
		if err != nil {
			t.Fatalf("ParseState(%q) вернул ошибку: %v", in, err)
		}
		if got != study.StateRelearning {
			t.Errorf("ParseState(%q) = %v, ожидалось relearning", in, got)
		}
	}
}

func TestParseStateErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want error
	}{
		{"", study.ErrRequired},
		{"  ", study.ErrRequired},
		{"0", study.ErrInvalid},
		{"buried", study.ErrInvalid},
		{"state(9)", study.ErrInvalid},
	}
	for _, tt := range tests {
		if _, err := study.ParseState(tt.in); !errors.Is(err, tt.want) {
			t.Errorf("ParseState(%q) = %v, ожидалась ошибка %v", tt.in, err, tt.want)
		}
	}
}

func TestStateIsValid(t *testing.T) {
	t.Parallel()

	for _, s := range study.States() {
		if !s.IsValid() {
			t.Errorf("фаза %v должна быть допустимой", s)
		}
	}
	for _, s := range []study.State{9, 255} {
		if s.IsValid() {
			t.Errorf("фаза %d не должна быть допустимой", uint8(s))
		}
		if !strings.HasPrefix(s.String(), "state(") {
			t.Errorf("String() = %q, ожидалась диагностическая запись", s.String())
		}
	}
}

func TestStateIsLearning(t *testing.T) {
	t.Parallel()

	for _, s := range []study.State{study.StateLearning, study.StateRelearning} {
		if !s.IsLearning() {
			t.Errorf("фаза %v относится к обучению", s)
		}
	}
	for _, s := range []study.State{study.StateNew, study.StateReview, study.StateSuspended} {
		if s.IsLearning() {
			t.Errorf("фаза %v не относится к обучению", s)
		}
	}
}

func TestModeRoundTrip(t *testing.T) {
	t.Parallel()

	for _, m := range study.Modes() {
		back, err := study.ParseMode(m.String())
		if err != nil {
			t.Fatalf("ParseMode(%q) вернул ошибку: %v", m, err)
		}
		if back != m {
			t.Errorf("ParseMode(%q) = %q, ожидалось %q", m, back, m)
		}
		if !m.IsValid() {
			t.Errorf("режим %q должен быть допустимым", m)
		}
	}
}

func TestParseModeErrors(t *testing.T) {
	t.Parallel()

	if got, err := study.ParseMode("  TYPING "); err != nil || got != study.ModeTyping {
		t.Errorf("ParseMode() = %q, %v; ожидался режим typing", got, err)
	}
	if _, err := study.ParseMode(""); !errors.Is(err, study.ErrRequired) {
		t.Error("пустая строка должна давать ErrRequired")
	}
	if _, err := study.ParseMode("dictation"); !errors.Is(err, study.ErrInvalid) {
		t.Error("неизвестный режим должен давать ErrInvalid")
	}
}

func TestModesIsCopy(t *testing.T) {
	t.Parallel()

	modes := study.Modes()
	modes[0] = study.Mode("dictation")
	if study.Modes()[0] == study.Mode("dictation") {
		t.Error("Modes() отдаёт изменяемый внутренний список")
	}
}
