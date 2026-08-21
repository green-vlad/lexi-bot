package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// SessionRepo хранит состояние диалогов (FSM).
type SessionRepo struct {
	base
}

// NewSessionRepo создаёт репозиторий состояний диалога.
func NewSessionRepo(pool *pgxpool.Pool) *SessionRepo {
	return &SessionRepo{base: base{pool: pool}}
}

var _ port.SessionRepo = (*SessionRepo)(nil)

// emptyPayload — то, что записывается вместо пустого шага диалога: колонка
// объявлена NOT NULL, а «шаг без данных» — нормальное состояние.
var emptyPayload = json.RawMessage(`{}`)

// Get возвращает состояние диалога.
func (r *SessionRepo) Get(ctx context.Context, userID user.ID) (port.Session, error) {
	const op = "прочитать состояние диалога"
	const query = "SELECT state, payload, updated_at FROM user_sessions WHERE user_id = $1"

	session := port.Session{UserID: userID}
	err := r.db(ctx).QueryRow(ctx, query, int64(userID)).
		Scan(&session.State, &session.Payload, &session.UpdatedAt)
	if err != nil {
		return port.Session{}, wrap(op, err)
	}
	return session, nil
}

// Save сохраняет состояние, перезаписывая предыдущее: диалог у пользователя
// один, и два шага одновременно — это не состояние, а ошибка.
//
// Нулевой UpdatedAt означает «сейчас», и время проставляет база. Непустое
// значение используется как есть: сценарии работают с инжектируемыми часами,
// и подменённое время должно доходить до строки, иначе тесты про протухшие
// диалоги проверяли бы не то.
func (r *SessionRepo) Save(ctx context.Context, s port.Session) error {
	const op = "сохранить состояние диалога"

	if s.State == "" {
		return fmt.Errorf("%s: %w (состояние диалога пустое)", op, port.ErrInvalidData)
	}

	payload := s.Payload
	if len(payload) == 0 {
		payload = emptyPayload
	}
	// Проверяем здесь, а не полагаемся на базу: ошибка разбора от Postgres
	// сообщает про синтаксис в неведомой строке, а не про то, что сценарий
	// собрал битый payload.
	if !json.Valid(payload) {
		return fmt.Errorf("%s: %w (payload не разбирается как JSON)", op, port.ErrInvalidData)
	}

	const query = `
		INSERT INTO user_sessions (user_id, state, payload, updated_at)
		VALUES ($1, $2, $3, COALESCE($4, now()))
		ON CONFLICT (user_id) DO UPDATE
		SET state      = EXCLUDED.state,
		    payload    = EXCLUDED.payload,
		    updated_at = EXCLUDED.updated_at`

	var updatedAt *time.Time
	if !s.UpdatedAt.IsZero() {
		updatedAt = &s.UpdatedAt
	}

	_, err := r.db(ctx).Exec(ctx, query, int64(s.UserID), s.State, []byte(payload), updatedAt)
	return wrap(op, err)
}

// Delete сбрасывает диалог.
//
// Отсутствие диалога ошибкой не считается: /cancel без начатого диалога —
// это «уже нечего отменять», а не авария.
func (r *SessionRepo) Delete(ctx context.Context, userID user.ID) error {
	const op = "сбросить состояние диалога"
	const query = "DELETE FROM user_sessions WHERE user_id = $1"

	_, err := r.db(ctx).Exec(ctx, query, int64(userID))
	return wrap(op, err)
}

// DeleteStale убирает диалоги, брошенные на полуслове, и возвращает число
// удалённых.
//
// Без этого пользователь, начавший /add месяц назад и не дописавший слово,
// навсегда остаётся в шаге «введите перевод»: любое его сообщение уходит
// в диалог, о котором он давно забыл.
func (r *SessionRepo) DeleteStale(ctx context.Context, olderThan time.Time) (int64, error) {
	const op = "убрать протухшие диалоги"
	const query = "DELETE FROM user_sessions WHERE updated_at < $1"

	tag, err := r.db(ctx).Exec(ctx, query, olderThan)
	if err != nil {
		return 0, wrap(op, err)
	}
	return tag.RowsAffected(), nil
}
