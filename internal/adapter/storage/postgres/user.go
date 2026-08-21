package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// UserRepo хранит учётные записи в PostgreSQL.
type UserRepo struct {
	base
}

// NewUserRepo создаёт репозиторий пользователей.
func NewUserRepo(pool *pgxpool.Pool) *UserRepo {
	return &UserRepo{base: base{pool: pool}}
}

var _ port.UserRepo = (*UserRepo)(nil)

// userColumns перечислены в одном месте: список повторяется в трёх запросах,
// и разъехавшись, он даст ошибку сканирования, а не понятную ошибку сборки.
const userColumns = "id, tg_user_id, tg_username, ui_lang, deleted_at"

// Ensure заводит пользователя или возвращает существующего.
//
// Повторный /start не трогает язык интерфейса и не сбрасывает прогресс:
// обновляется только имя в Telegram, которое пользователь мог сменить.
// Заодно снимается мягкое удаление — человек, вернувшийся после блокировки
// бота, продолжает с того же места.
func (r *UserRepo) Ensure(ctx context.Context, u user.User) (user.User, bool, error) {
	const op = "создать или найти пользователя"

	if err := u.Validate(); err != nil {
		return user.User{}, false, err
	}

	// xmax = 0 у строки, вставленной этим же запросом: у обновлённой там
	// стоит номер транзакции. Так один запрос отвечает и «кто это»,
	// и «впервые ли мы его видим».
	const query = `
		INSERT INTO users (tg_user_id, tg_username, ui_lang)
		VALUES ($1, $2, $3)
		ON CONFLICT (tg_user_id) DO UPDATE
		SET tg_username = EXCLUDED.tg_username,
		    deleted_at  = NULL
		RETURNING ` + userColumns + `, (xmax = 0) AS created`

	var (
		saved   user.User
		created bool
	)
	row := r.db(ctx).QueryRow(ctx, query, int64(u.TelegramID), u.Username, string(u.UILang))
	if err := scanUserWith(row, &saved, &created); err != nil {
		return user.User{}, false, wrap(op, err)
	}
	return saved, created, nil
}

// ByTelegramID возвращает пользователя по идентификатору Telegram.
//
// Мягко удалённые возвращаются тоже: отличить «нет такого» от «был и ушёл»
// должен вызывающий, у него для этого есть User.IsActive.
func (r *UserRepo) ByTelegramID(ctx context.Context, tgID user.TelegramID) (user.User, error) {
	const op = "найти пользователя по идентификатору Telegram"
	const query = "SELECT " + userColumns + " FROM users WHERE tg_user_id = $1"

	var u user.User
	row := r.db(ctx).QueryRow(ctx, query, int64(tgID))
	if err := scanUser(row, &u); err != nil {
		return user.User{}, wrap(op, err)
	}
	return u, nil
}

// ByID возвращает пользователя по внутреннему идентификатору.
func (r *UserRepo) ByID(ctx context.Context, id user.ID) (user.User, error) {
	const op = "найти пользователя"
	const query = "SELECT " + userColumns + " FROM users WHERE id = $1"

	var u user.User
	row := r.db(ctx).QueryRow(ctx, query, int64(id))
	if err := scanUser(row, &u); err != nil {
		return user.User{}, wrap(op, err)
	}
	return u, nil
}

// SetUILang меняет язык интерфейса.
func (r *UserRepo) SetUILang(ctx context.Context, id user.ID, lang user.UILang) error {
	const op = "сменить язык интерфейса"
	const query = "UPDATE users SET ui_lang = $2 WHERE id = $1"

	tag, err := r.db(ctx).Exec(ctx, query, int64(id), string(lang))
	return requireRows(op, tag, err)
}

// SoftDelete помечает запись удалённой, сохраняя журнал повторений.
func (r *UserRepo) SoftDelete(ctx context.Context, id user.ID, at time.Time) error {
	const op = "мягко удалить пользователя"
	const query = "UPDATE users SET deleted_at = $2 WHERE id = $1"

	tag, err := r.db(ctx).Exec(ctx, query, int64(id), at)
	return requireRows(op, tag, err)
}

// Purge удаляет пользователя без следа. Всё, что на него ссылается —
// настройки, курсы, карточки, журнал, личные слова, — уходит каскадом,
// описанным в схеме: перечислять таблицы здесь значило бы завести второй
// список, который однажды разойдётся с первым.
func (r *UserRepo) Purge(ctx context.Context, id user.ID) error {
	const op = "удалить пользователя"
	const query = "DELETE FROM users WHERE id = $1"

	tag, err := r.db(ctx).Exec(ctx, query, int64(id))
	return requireRows(op, tag, err)
}

// row — общий знаменатель pgx.Row и строки из pgx.Rows.
type row interface {
	Scan(dest ...any) error
}

func scanUser(r row, u *user.User) error {
	return scanUserWith(r, u, nil)
}

// scanUserWith читает пользователя, а при непустом created — ещё и признак
// того, что строка только что создана.
func scanUserWith(r row, u *user.User, created *bool) error {
	var (
		id        int64
		tgID      int64
		lang      string
		deletedAt *time.Time
	)

	dest := []any{&id, &tgID, &u.Username, &lang, &deletedAt}
	if created != nil {
		dest = append(dest, created)
	}
	if err := r.Scan(dest...); err != nil {
		return err
	}

	u.ID = user.ID(id)
	u.TelegramID = user.TelegramID(tgID)
	u.UILang = user.UILang(lang)
	if deletedAt != nil {
		u.DeletedAt = *deletedAt
	} else {
		u.DeletedAt = time.Time{}
	}
	return nil
}
