package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/usecase/port"
)

// CounterRepo хранит дневные счётчики курса.
type CounterRepo struct {
	base
}

// NewCounterRepo создаёт репозиторий дневных счётчиков.
func NewCounterRepo(pool *pgxpool.Pool) *CounterRepo {
	return &CounterRepo{base: base{pool: pool}}
}

var _ port.CounterRepo = (*CounterRepo)(nil)

// Get возвращает счётчики за сутки.
//
// Отсутствие строки — это нули, а не ошибка: в день, когда пользователь
// ещё не занимался, счётчиков просто нет, и заводить их ради чтения было бы
// записью на каждый показ меню.
func (r *CounterRepo) Get(ctx context.Context, courseID study.CourseID, day time.Time) (port.DailyCounter, error) {
	const op = "прочитать дневные счётчики"
	const query = `
		SELECT new_introduced, reviews_done
		FROM daily_counters
		WHERE user_course_id = $1 AND day = $2`

	counter := port.DailyCounter{Day: day}
	err := r.db(ctx).QueryRow(ctx, query, int64(courseID), day).
		Scan(&counter.NewIntroduced, &counter.ReviewsDone)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.DailyCounter{Day: day}, nil
	}
	if err != nil {
		return port.DailyCounter{}, wrap(op, err)
	}
	return counter, nil
}

// AddReview увеличивает счётчик повторений за сутки, заводя строку при
// первом ответе дня.
//
// Инкремент делается в базе (reviews_done + 1), а не в Go: прочитать,
// прибавить и записать значило бы потерять ответ при двух одновременных
// нажатиях.
func (r *CounterRepo) AddReview(ctx context.Context, courseID study.CourseID, day time.Time) error {
	const op = "увеличить счётчик повторений"
	const query = `
		INSERT INTO daily_counters (user_course_id, day, reviews_done)
		VALUES ($1, $2, 1)
		ON CONFLICT (user_course_id, day) DO UPDATE
		SET reviews_done = daily_counters.reviews_done + 1`

	_, err := r.db(ctx).Exec(ctx, query, int64(courseID), day)
	return wrap(op, err)
}
