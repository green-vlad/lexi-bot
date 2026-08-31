// Package settings меняет настройки обучения.
//
// Сценарий тонкий намеренно: все правила — границы значений, канонизация
// набора режимов, непустота — живут в домене (user.Settings), и здесь их
// нет. Задача пакета в другом: прочитать настройки, применить одну правку
// и сохранить их целиком.
package settings

import (
	"context"
	"errors"
	"fmt"

	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// Deps — зависимости настроек.
type Deps struct {
	Settings port.SettingsRepo
}

// Service — сценарий настроек.
type Service struct {
	deps Deps
}

// New создаёт сценарий.
func New(deps Deps) (*Service, error) {
	if deps.Settings == nil {
		return nil, errors.New("настройкам нужен SettingsRepo")
	}
	return &Service{deps: deps}, nil
}

// Get возвращает настройки пользователя.
func (s *Service) Get(ctx context.Context, userID user.ID) (user.Settings, error) {
	current, err := s.deps.Settings.Get(ctx, userID)
	if err != nil {
		return user.Settings{}, fmt.Errorf("прочитать настройки: %w", err)
	}
	return current, nil
}

// change — одна правка настроек.
//
// Функция, а не набор методов на каждое поле: чтение, применение и запись
// у всех правок одинаковы, и отличается только середина.
type change func(user.Settings) (user.Settings, error)

// SetNewPerDay меняет дневную норму новых слов.
//
// Сегодняшний день правка не задевает: норма на сутки зафиксирована в момент,
// когда человек начал первое слово, и новое значение вступит в силу назавтра.
func (s *Service) SetNewPerDay(ctx context.Context, userID user.ID, n int) (user.Settings, error) {
	return s.apply(ctx, userID, func(current user.Settings) (user.Settings, error) {
		return current.WithNewPerDay(n)
	})
}

// SetMaxReviewsPerDay меняет потолок повторений в день.
func (s *Service) SetMaxReviewsPerDay(ctx context.Context, userID user.ID, n int) (user.Settings, error) {
	return s.apply(ctx, userID, func(current user.Settings) (user.Settings, error) {
		return current.WithMaxReviewsPerDay(n)
	})
}

// SetQuizModes меняет набор включённых режимов проверки.
func (s *Service) SetQuizModes(ctx context.Context, userID user.ID, modes []study.Mode) (user.Settings, error) {
	return s.apply(ctx, userID, func(current user.Settings) (user.Settings, error) {
		return current.WithQuizModes(modes)
	})
}

// ToggleMode включает или выключает один режим проверки.
//
// Последний включённый выключить нельзя: набор без режимов означает карточку,
// которую нечем показать, и домен такие настройки не принимает. Второе
// значение — false, если правка отклонена именно поэтому.
func (s *Service) ToggleMode(ctx context.Context, userID user.ID, mode study.Mode) (user.Settings, bool, error) {
	current, err := s.Get(ctx, userID)
	if err != nil {
		return user.Settings{}, false, err
	}

	modes := make([]study.Mode, 0, len(current.QuizModes)+1)
	for _, enabled := range current.QuizModes {
		if enabled != mode {
			modes = append(modes, enabled)
		}
	}
	if len(modes) == len(current.QuizModes) {
		modes = append(modes, mode)
	}
	if len(modes) == 0 {
		return current, false, nil
	}

	updated, err := s.SetQuizModes(ctx, userID, modes)
	if err != nil {
		return user.Settings{}, false, err
	}
	return updated, true, nil
}

// SetTimezone меняет таймзону, по которой считается граница суток.
func (s *Service) SetTimezone(ctx context.Context, userID user.ID, tz user.Timezone) (user.Settings, error) {
	return s.apply(ctx, userID, func(current user.Settings) (user.Settings, error) {
		return current.WithTimezone(tz)
	})
}

// SetReminderAt задаёт время напоминания. Незаданное время выключает его.
func (s *Service) SetReminderAt(ctx context.Context, userID user.ID, at user.TimeOfDay) (user.Settings, error) {
	return s.apply(ctx, userID, func(current user.Settings) (user.Settings, error) {
		return current.WithReminderAt(at)
	})
}

// ToggleDirection переключает направление проверки.
func (s *Service) ToggleDirection(ctx context.Context, userID user.ID) (user.Settings, error) {
	return s.apply(ctx, userID, func(current user.Settings) (user.Settings, error) {
		current.ReverseDirection = !current.ReverseDirection
		return current, current.Validate()
	})
}

// apply читает настройки, применяет правку и сохраняет результат.
//
// Испорченная правка не сохраняется: настройки — значение, и неудачная
// проверка возвращает ошибку, не тронув ни копию, ни базу.
func (s *Service) apply(ctx context.Context, userID user.ID, edit change) (user.Settings, error) {
	current, err := s.Get(ctx, userID)
	if err != nil {
		return user.Settings{}, err
	}

	updated, err := edit(current)
	if err != nil {
		return user.Settings{}, err
	}
	if err := s.deps.Settings.Save(ctx, userID, updated); err != nil {
		return user.Settings{}, fmt.Errorf("сохранить настройки: %w", err)
	}
	return updated, nil
}
