package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// ReviewRepo пишет журнал повторений и считает по нему сводки.
type ReviewRepo struct {
	base
}

// NewReviewRepo создаёт репозиторий журнала.
func NewReviewRepo(pool *pgxpool.Pool) *ReviewRepo {
	return &ReviewRepo{base: base{pool: pool}}
}

var _ port.ReviewRepo = (*ReviewRepo)(nil)

// Add добавляет запись в журнал.
//
// Обычный ответ пользователя идёт через CardRepo.Apply, где журнал пишется
// вместе с карточкой одной транзакцией. Этот метод — для случаев, когда
// состояние карточки не меняется.
func (r *ReviewRepo) Add(ctx context.Context, userID user.ID, review *study.Review) error {
	if err := review.Validate(); err != nil {
		return err
	}
	return insertReview(ctx, r.db(ctx), userID, review)
}

// Stats считает ответы пользователя начиная с момента since.
func (r *ReviewRepo) Stats(ctx context.Context, userID user.ID, since time.Time) (port.ReviewStats, error) {
	const op = "посчитать сводку по журналу"
	const query = `
		SELECT count(*), count(*) FILTER (WHERE is_correct)
		FROM reviews
		WHERE user_id = $1 AND rated_at >= $2`

	var stats port.ReviewStats
	err := r.db(ctx).QueryRow(ctx, query, int64(userID), since).Scan(&stats.Total, &stats.Correct)
	if err != nil {
		return port.ReviewStats{}, wrap(op, err)
	}
	return stats, nil
}

// ActiveDays возвращает дни, в которые пользователь отвечал, от свежего
// к старому.
//
// День считается в таймзоне пользователя: ответ в половине первого ночи
// по Сеулу — это вчерашний вечер по UTC, и без приведения зоны серия
// занятий рвалась бы на ровном месте. Приводит зону сама база: тащить
// в Go все ответы за месяц ради группировки по датам незачем.
func (r *ReviewRepo) ActiveDays(ctx context.Context, userID user.ID, tz user.Timezone, since time.Time) ([]time.Time, error) {
	const op = "получить дни занятий"
	const query = `
		SELECT DISTINCT (rated_at AT TIME ZONE $2)::DATE AS day
		FROM reviews
		WHERE user_id = $1 AND rated_at >= $3
		ORDER BY day DESC`

	rows, err := r.db(ctx).Query(ctx, query, int64(userID), tz.String(), since)
	if err != nil {
		return nil, wrap(op, err)
	}
	defer rows.Close()

	var out []time.Time
	for rows.Next() {
		var day time.Time
		if err := rows.Scan(&day); err != nil {
			return nil, wrap(op, err)
		}
		out = append(out, day)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap(op, err)
	}
	return out, nil
}

// insertReview добавляет запись в журнал повторений.
//
// Журнал только пополняется — правок и удалений у него нет, поэтому нет
// и ON CONFLICT: повторная вставка того же ответа означала бы ошибку
// в сценарии, и молча её проглатывать нельзя.
//
// user_id заполняется здесь, а не берётся из карточки: это денормализация
// ради статистики, и её источник — курс, который сценарий уже знает.
func insertReview(ctx context.Context, tx queryer, userID user.ID, review *study.Review) error {
	const query = `
		INSERT INTO reviews (
			card_id, user_id, rated_at, rating, mode, answer_raw, is_correct,
			duration_ms, prev_interval, new_interval, prev_ef, new_ef)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`

	_, err := tx.Exec(ctx, query,
		int64(review.CardID), int64(userID), review.RatedAt,
		review.Rating.String(), review.Mode.String(), review.AnswerRaw, review.IsCorrect,
		review.Duration.Milliseconds(), review.PrevInterval, review.NewInterval,
		review.PrevEase, review.NewEase)
	return wrap("записать повторение", err)
}
