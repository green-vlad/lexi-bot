package postgres

import (
	"context"

	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
)

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
