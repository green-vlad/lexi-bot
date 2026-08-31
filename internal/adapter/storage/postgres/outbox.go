package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// scheduleLockKey — ключ межпроцессной блокировки планировщика.
//
// Она не единственная защита от дублей и даже не главная: уникальный индекс
// по (user_id, kind, scheduled_for) не даст создать второе напоминание
// на тот же момент при любой гонке. Блокировка нужна затем, чтобы два
// инстанса не делали одну и ту же работу впустую.
const scheduleLockKey int64 = 8_140_233

// OutboxRepo хранит очередь отправки.
type OutboxRepo struct {
	base
}

// NewOutboxRepo создаёт репозиторий очереди отправки.
func NewOutboxRepo(pool *pgxpool.Pool) *OutboxRepo {
	return &OutboxRepo{base: base{pool: pool}}
}

var _ port.OutboxRepo = (*OutboxRepo)(nil)

// Schedule ставит сообщения в очередь, пропуская те, что уже стоят.
//
// Блокировка транзакционная: она снимается вместе с коммитом, и отдельного
// освобождения не требует — а значит, не может остаться висеть после паники
// или обрыва соединения.
func (r *OutboxRepo) Schedule(ctx context.Context, notifications []port.Notification) (int, error) {
	const op = "поставить уведомления в очередь"

	if len(notifications) == 0 {
		return 0, nil
	}

	users := make([]int64, len(notifications))
	kinds := make([]string, len(notifications))
	moments := make([]time.Time, len(notifications))
	for i, n := range notifications {
		users[i] = int64(n.UserID)
		kinds[i] = n.Kind
		moments[i] = n.ScheduledFor
	}

	var added int
	err := r.inTx(ctx, func(tx queryer) error {
		var locked bool
		if err := tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1)", scheduleLockKey).Scan(&locked); err != nil {
			return wrap(op, err)
		}
		if !locked {
			// Тик уже выполняет другой инстанс. Ждать нечего: он поставит
			// в очередь ровно то же самое.
			return nil
		}

		const insert = `
			INSERT INTO outbox_notifications (user_id, kind, scheduled_for)
			SELECT * FROM unnest($1::BIGINT[], $2::TEXT[], $3::TIMESTAMPTZ[])
			ON CONFLICT (user_id, kind, scheduled_for) DO NOTHING`

		tag, err := tx.Exec(ctx, insert, users, kinds, moments)
		if err != nil {
			return wrap(op, err)
		}
		added = int(tag.RowsAffected())
		return nil
	})
	if err != nil {
		return 0, err
	}
	return added, nil
}

// Pending возвращает неотправленные сообщения, чей срок наступил.
func (r *OutboxRepo) Pending(ctx context.Context, now time.Time, limit int) ([]port.Notification, error) {
	const op = "получить очередь отправки"

	if limit <= 0 {
		return nil, nil
	}

	const query = `
		SELECT id, user_id, kind, scheduled_for
		FROM outbox_notifications
		WHERE sent_at IS NULL AND scheduled_for <= $1
		ORDER BY scheduled_for, id
		LIMIT $2`

	rows, err := r.db(ctx).Query(ctx, query, now, limit)
	if err != nil {
		return nil, wrap(op, err)
	}
	defer rows.Close()

	var out []port.Notification
	for rows.Next() {
		var (
			n      port.Notification
			id     int64
			userID int64
		)
		if err := rows.Scan(&id, &userID, &n.Kind, &n.ScheduledFor); err != nil {
			return nil, wrap(op, err)
		}
		n.ID = port.NotificationID(id)
		n.UserID = user.ID(userID)
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap(op, err)
	}
	return out, nil
}

// MarkSent отмечает сообщения отправленными.
//
// Условие sent_at IS NULL защищает от повторной отметки: два воркера,
// подхватившие одну строку, не должны переписывать чужой момент отправки.
func (r *OutboxRepo) MarkSent(ctx context.Context, ids []port.NotificationID, at time.Time) error {
	const op = "отметить отправленные уведомления"

	if len(ids) == 0 {
		return nil
	}

	raw := make([]int64, len(ids))
	for i, id := range ids {
		raw[i] = int64(id)
	}

	const query = `
		UPDATE outbox_notifications
		SET sent_at = $2
		WHERE id = ANY($1::BIGINT[]) AND sent_at IS NULL`

	if _, err := r.db(ctx).Exec(ctx, query, raw, at); err != nil {
		return wrap(op, err)
	}
	return nil
}
