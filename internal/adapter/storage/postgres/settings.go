package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// SettingsRepo хранит настройки обучения.
type SettingsRepo struct {
	base
}

// NewSettingsRepo создаёт репозиторий настроек.
func NewSettingsRepo(pool *pgxpool.Pool) *SettingsRepo {
	return &SettingsRepo{base: base{pool: pool}}
}

var _ port.SettingsRepo = (*SettingsRepo)(nil)

// Get возвращает настройки пользователя.
//
// Время напоминания читается сразу строкой «ЧЧ:ММ»: колонка типа TIME
// в Go превращается в неудобный тип с микросекундами от полуночи, а нам
// нужны ровно часы и минуты, которые домен и так умеет разбирать.
func (r *SettingsRepo) Get(ctx context.Context, userID user.ID) (user.Settings, error) {
	const op = "прочитать настройки"
	const query = `
		SELECT new_per_day,
		       max_reviews_per_day,
		       to_char(reminder_at, 'HH24:MI'),
		       timezone,
		       quiz_modes,
		       reverse_direction
		FROM user_settings
		WHERE user_id = $1`

	var (
		s        user.Settings
		reminder *string
		tzName   string
		modes    []string
	)

	err := r.db(ctx).QueryRow(ctx, query, int64(userID)).Scan(
		&s.NewPerDay, &s.MaxReviewsPerDay, &reminder, &tzName, &modes, &s.ReverseDirection)
	if err != nil {
		return user.Settings{}, wrap(op, err)
	}

	tz, err := user.ParseTimezone(tzName)
	if err != nil {
		return user.Settings{}, wrap(op, err)
	}
	s.Timezone = tz

	if reminder != nil {
		at, err := user.ParseTimeOfDay(*reminder)
		if err != nil {
			return user.Settings{}, wrap(op, err)
		}
		s.ReminderAt = at
	}

	s.QuizModes = make([]study.Mode, 0, len(modes))
	for _, raw := range modes {
		mode, err := study.ParseMode(raw)
		if err != nil {
			return user.Settings{}, wrap(op, err)
		}
		s.QuizModes = append(s.QuizModes, mode)
	}

	if err := s.Validate(); err != nil {
		return user.Settings{}, wrap(op, err)
	}
	return s, nil
}

// Save сохраняет настройки целиком.
//
// Настройки маленькие и меняются редко, поэтому пишутся одной строкой:
// частичные обновления породили бы метод на каждое поле и вопрос «а что
// сейчас в остальных» при каждом вызове.
func (r *SettingsRepo) Save(ctx context.Context, userID user.ID, s user.Settings) error {
	const op = "сохранить настройки"

	if err := s.Validate(); err != nil {
		return err
	}

	const query = `
		INSERT INTO user_settings (
			user_id, new_per_day, max_reviews_per_day, reminder_at,
			timezone, quiz_modes, reverse_direction)
		VALUES ($1, $2, $3, $4::TIME, $5, $6, $7)
		ON CONFLICT (user_id) DO UPDATE
		SET new_per_day         = EXCLUDED.new_per_day,
		    max_reviews_per_day = EXCLUDED.max_reviews_per_day,
		    reminder_at         = EXCLUDED.reminder_at,
		    timezone            = EXCLUDED.timezone,
		    quiz_modes          = EXCLUDED.quiz_modes,
		    reverse_direction   = EXCLUDED.reverse_direction`

	var reminder *string
	if s.ReminderAt.IsSet() {
		at := s.ReminderAt.String()
		reminder = &at
	}

	modes := make([]string, 0, len(s.QuizModes))
	for _, mode := range s.QuizModes {
		modes = append(modes, mode.String())
	}

	_, err := r.db(ctx).Exec(ctx, query,
		int64(userID), s.NewPerDay, s.MaxReviewsPerDay, reminder,
		s.Timezone.String(), modes, s.ReverseDirection)
	return wrap(op, err)
}
