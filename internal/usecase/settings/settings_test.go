package settings_test

import (
	"context"
	"errors"
	"testing"

	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/settings"
)

const owner = user.ID(42)

// fakeSettings хранит настройки так же, как база: Save переписывает их
// целиком, Get отдаёт сохранённое.
type fakeSettings struct {
	settings user.Settings
	saves    int
	failWith error
}

func (f *fakeSettings) Get(context.Context, user.ID) (user.Settings, error) {
	if f.failWith != nil {
		return user.Settings{}, f.failWith
	}
	return f.settings, nil
}

func (f *fakeSettings) Save(_ context.Context, _ user.ID, s user.Settings) error {
	if f.failWith != nil {
		return f.failWith
	}
	f.saves++
	f.settings = s
	return nil
}

type fixture struct {
	service *settings.Service
	repo    *fakeSettings
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	f := &fixture{repo: &fakeSettings{
		settings: user.DefaultSettings(user.MustParseTimezone("Asia/Seoul")),
	}}

	service, err := settings.New(settings.Deps{Settings: f.repo})
	if err != nil {
		t.Fatalf("settings.New() вернул ошибку: %v", err)
	}
	f.service = service
	return f
}

func TestSetNumericLimits(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	updated, err := f.service.SetNewPerDay(context.Background(), owner, 20)
	if err != nil {
		t.Fatalf("SetNewPerDay() вернул ошибку: %v", err)
	}
	if updated.NewPerDay != 20 || f.repo.settings.NewPerDay != 20 {
		t.Errorf("норма = %d/%d, ожидалось 20", updated.NewPerDay, f.repo.settings.NewPerDay)
	}

	updated, err = f.service.SetMaxReviewsPerDay(context.Background(), owner, 50)
	if err != nil {
		t.Fatalf("SetMaxReviewsPerDay() вернул ошибку: %v", err)
	}
	if updated.MaxReviewsPerDay != 50 {
		t.Errorf("потолок = %d, ожидалось 50", updated.MaxReviewsPerDay)
	}
	// Остальные настройки правка не задела.
	if updated.NewPerDay != 20 {
		t.Errorf("норма = %d: правка потолка задела соседнюю настройку", updated.NewPerDay)
	}
}

func TestLimitsRespectBounds(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		call func(*settings.Service) error
	}{
		{"норма ниже предела", func(s *settings.Service) error {
			_, err := s.SetNewPerDay(context.Background(), owner, user.MinNewPerDay-1)
			return err
		}},
		{"норма выше предела", func(s *settings.Service) error {
			_, err := s.SetNewPerDay(context.Background(), owner, user.MaxNewPerDay+1)
			return err
		}},
		{"потолок ниже предела", func(s *settings.Service) error {
			_, err := s.SetMaxReviewsPerDay(context.Background(), owner, user.MinReviewsPerDay-1)
			return err
		}},
		{"потолок выше предела", func(s *settings.Service) error {
			_, err := s.SetMaxReviewsPerDay(context.Background(), owner, user.MaxReviewsPerDay+1)
			return err
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			before := f.repo.settings

			if err := tt.call(f.service); !errors.Is(err, user.ErrOutOfRange) {
				t.Errorf("ошибка = %v, ожидалась ErrOutOfRange", err)
			}
			// Отклонённая правка не сохраняется: настройки — значение,
			// и неудачная проверка не трогает ни копию, ни базу.
			if f.repo.saves != 0 {
				t.Error("отклонённая правка всё равно сохранилась")
			}
			if f.repo.settings.NewPerDay != before.NewPerDay ||
				f.repo.settings.MaxReviewsPerDay != before.MaxReviewsPerDay {
				t.Errorf("настройки = %+v, ожидались прежние", f.repo.settings)
			}
		})
	}
}

func TestToggleModeKeepsAtLeastOne(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	// Выключаем ввод текстом — остаётся выбор из вариантов.
	updated, ok, err := f.service.ToggleMode(context.Background(), owner, study.ModeTyping)
	if err != nil || !ok {
		t.Fatalf("ToggleMode() = %t, %v", ok, err)
	}
	if updated.ModeEnabled(study.ModeTyping) || !updated.ModeEnabled(study.ModeChoice) {
		t.Errorf("режимы = %v", updated.QuizModes)
	}

	// А последний включённый выключить нельзя: карточку станет нечем
	// показать, и домен такие настройки не принимает.
	saves := f.repo.saves
	updated, ok, err = f.service.ToggleMode(context.Background(), owner, study.ModeChoice)
	if err != nil {
		t.Fatalf("ToggleMode() вернул ошибку: %v", err)
	}
	if ok {
		t.Error("последний режим выключился")
	}
	if !updated.ModeEnabled(study.ModeChoice) {
		t.Errorf("режимы = %v, ожидался прежний набор", updated.QuizModes)
	}
	if f.repo.saves != saves {
		t.Error("отклонённая правка всё равно сохранилась")
	}

	// Повторное нажатие включает режим обратно.
	updated, ok, err = f.service.ToggleMode(context.Background(), owner, study.ModeTyping)
	if err != nil || !ok {
		t.Fatalf("ToggleMode() = %t, %v", ok, err)
	}
	if !updated.ModeEnabled(study.ModeTyping) {
		t.Errorf("режимы = %v, ожидалось возвращение ввода текстом", updated.QuizModes)
	}
}

func TestSetQuizModesRejectsEmptyAndUnknown(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	if _, err := f.service.SetQuizModes(context.Background(), owner, nil); !errors.Is(err, user.ErrRequired) {
		t.Errorf("пустой набор = %v, ожидалась ErrRequired", err)
	}
	if _, err := f.service.SetQuizModes(context.Background(), owner, []study.Mode{study.Mode("dictation")}); !errors.Is(err, user.ErrInvalid) {
		t.Errorf("неизвестный режим = %v, ожидалась ErrInvalid", err)
	}
	if f.repo.saves != 0 {
		t.Error("отклонённая правка всё равно сохранилась")
	}
}

func TestToggleDirection(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	updated, err := f.service.ToggleDirection(context.Background(), owner)
	if err != nil {
		t.Fatalf("ToggleDirection() вернул ошибку: %v", err)
	}
	if !updated.ReverseDirection || updated.Direction() != study.DirectionProduce {
		t.Errorf("направление = %v", updated.Direction())
	}

	updated, err = f.service.ToggleDirection(context.Background(), owner)
	if err != nil {
		t.Fatalf("ToggleDirection() вернул ошибку: %v", err)
	}
	if updated.ReverseDirection || updated.Direction() != study.DirectionRecognize {
		t.Errorf("направление = %v, ожидался возврат", updated.Direction())
	}
}

func TestSetReminderAt(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	updated, err := f.service.SetReminderAt(context.Background(), owner, user.MustParseTimeOfDay("21:30"))
	if err != nil {
		t.Fatalf("SetReminderAt() вернул ошибку: %v", err)
	}
	if !updated.RemindersEnabled() || updated.ReminderAt.String() != "21:30" {
		t.Errorf("напоминание = %q", updated.ReminderAt)
	}

	// Незаданное время выключает напоминания.
	updated, err = f.service.SetReminderAt(context.Background(), owner, user.TimeOfDay{})
	if err != nil {
		t.Fatalf("SetReminderAt() вернул ошибку: %v", err)
	}
	if updated.RemindersEnabled() {
		t.Error("напоминания не выключились")
	}
}

func TestSetTimezone(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	updated, err := f.service.SetTimezone(context.Background(), owner, user.MustParseTimezone("Europe/Moscow"))
	if err != nil {
		t.Fatalf("SetTimezone() вернул ошибку: %v", err)
	}
	if updated.Timezone.String() != "Europe/Moscow" {
		t.Errorf("таймзона = %q", updated.Timezone)
	}

	// Пустая таймзона недопустима: по ней считается граница суток.
	if _, err := f.service.SetTimezone(context.Background(), owner, user.Timezone{}); !errors.Is(err, user.ErrRequired) {
		t.Errorf("пустая таймзона = %v, ожидалась ErrRequired", err)
	}
}

func TestReportsRepoFailure(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.repo.failWith = errors.New("база недоступна")

	if _, err := f.service.Get(context.Background(), owner); err == nil {
		t.Error("недоступная база должна быть ошибкой")
	}
	if _, err := f.service.SetNewPerDay(context.Background(), owner, 10); err == nil {
		t.Error("недоступная база должна быть ошибкой")
	}
}

func TestNewNeedsDependencies(t *testing.T) {
	t.Parallel()

	if _, err := settings.New(settings.Deps{}); err == nil {
		t.Error("сценарий без зависимостей должен быть ошибкой")
	}
}
