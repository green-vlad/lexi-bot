// Package account распоряжается учётной записью целиком: приостановкой
// занятий и удалением данных.
//
// Удаление здесь настоящее, а не пометка. Мягкое годится для того, кто
// заблокировал бота: он может вернуться, и терять его прогресс незачем.
// Человек, попросивший удалить его данные, просил именно этого.
package account

import (
	"context"
	"errors"
	"fmt"

	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// Deps — зависимости учётной записи.
type Deps struct {
	Users port.UserRepo
}

// Service — сценарий учётной записи.
type Service struct {
	deps Deps
}

// New создаёт сценарий.
func New(deps Deps) (*Service, error) {
	if deps.Users == nil {
		return nil, errors.New("учётной записи нужен UserRepo")
	}
	return &Service{deps: deps}, nil
}

// Delete удаляет пользователя и всё, что на него ссылается.
//
// Перечислять таблицы здесь незачем: каскады описаны в схеме, и второй
// список рано или поздно разошёлся бы с первым. Настройки, курсы, карточки,
// журнал, личные слова, задания импорта и очередь отправки уходят вместе
// со строкой пользователя.
func (s *Service) Delete(ctx context.Context, userID user.ID) error {
	if err := s.deps.Users.Purge(ctx, userID); err != nil {
		return fmt.Errorf("удалить пользователя: %w", err)
	}
	return nil
}
