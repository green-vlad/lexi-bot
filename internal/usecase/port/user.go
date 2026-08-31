package port

import (
	"context"
	"encoding/json"
	"time"

	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
)

// UserRepo хранит учётные записи.
type UserRepo interface {
	// Ensure заводит пользователя или возвращает существующего по tg_user_id.
	// Второе значение — признак того, что запись создана: по нему сценарий
	// онбординга отличает первый /start от повторного и не сбрасывает прогресс.
	Ensure(ctx context.Context, u *user.User) (saved user.User, created bool, err error)

	// ByTelegramID возвращает пользователя по идентификатору Telegram.
	ByTelegramID(ctx context.Context, tgID user.TelegramID) (user.User, error)

	// ByID возвращает пользователя по внутреннему идентификатору.
	ByID(ctx context.Context, id user.ID) (user.User, error)

	// SetUILang меняет язык интерфейса.
	SetUILang(ctx context.Context, id user.ID, lang user.UILang) error

	// SetCurrentCourse запоминает, какой курс человек учит сейчас.
	// Нулевой курс означает «забыть выбор»: занятие возьмёт любой активный.
	SetCurrentCourse(ctx context.Context, id user.ID, courseID study.CourseID) error

	// SoftDelete помечает запись удалённой, сохраняя журнал повторений.
	// Так же деактивируется пользователь, заблокировавший бота (T-047).
	SoftDelete(ctx context.Context, id user.ID, at time.Time) error

	// Purge удаляет пользователя и все его данные без следа — это /delete_me,
	// и здесь мягкого удаления недостаточно.
	Purge(ctx context.Context, id user.ID) error
}

// SettingsRepo хранит настройки обучения.
type SettingsRepo interface {
	// Get возвращает настройки пользователя.
	Get(ctx context.Context, userID user.ID) (user.Settings, error)

	// Save сохраняет настройки целиком: они маленькие и меняются редко,
	// а частичные обновления породили бы метод на каждое поле.
	Save(ctx context.Context, userID user.ID, s user.Settings) error

	// Reminding возвращает тех, кому есть смысл напоминать: напоминание
	// включено, запись не удалена и хотя бы один курс активен.
	//
	// Человеку, поставившему все курсы на паузу, напоминание пришло бы
	// о занятии, которого нет, — и выглядело бы упрёком.
	Reminding(ctx context.Context) ([]UserReminder, error)
}

// Session — состояние диалога с пользователем (FSM).
//
// Payload оставлен сырым JSON: у каждого шага диалога он свой, и порт
// не должен знать, что именно кладёт туда сценарий.
type Session struct {
	UserID    user.ID
	State     string
	Payload   json.RawMessage
	UpdatedAt time.Time
}

// SessionRepo хранит состояние диалогов.
//
// Состояние лежит в базе, а не в памяти процесса: диалог должен пережить
// перезапуск бота, а при сотне пользователей цена этого нулевая.
type SessionRepo interface {
	// Get возвращает состояние диалога; ErrNotFound означает, что диалога нет.
	Get(ctx context.Context, userID user.ID) (Session, error)

	// Save сохраняет состояние, перезаписывая предыдущее.
	Save(ctx context.Context, s Session) error

	// Delete сбрасывает диалог — это /cancel и завершение сценария.
	Delete(ctx context.Context, userID user.ID) error

	// DeleteStale убирает состояния, брошенные на полуслове, и возвращает
	// число удалённых.
	DeleteStale(ctx context.Context, olderThan time.Time) (int64, error)
}
