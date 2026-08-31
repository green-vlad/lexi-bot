package port

import (
	"context"
	"time"

	"lexi-bot/internal/domain/user"
)

// Виды уведомлений.
const (
	// NotificationReminder — напоминание о занятии в заданное время.
	NotificationReminder = "reminder"
	// NotificationBroadcast — сообщение от администратора всем сразу.
	NotificationBroadcast = "broadcast"
)

// NotificationID — идентификатор строки очереди отправки.
type NotificationID int64

// Notification — одно сообщение, ждущее отправки.
//
// Очередь в базе, а не в памяти: перезапуск бота посреди рассылки не должен
// ни терять напоминания, ни слать их по второму разу.
type Notification struct {
	ID     NotificationID
	UserID user.ID
	Kind   string
	// ScheduledFor — момент, на который назначено сообщение. Он же ключ
	// от повторов: два тика планировщика, попавшие в одно окно, посчитают
	// один и тот же момент, и второй не создаст ничего нового.
	ScheduledFor time.Time
	// SentAt — когда сообщение ушло. Нулевое значение означает «ещё нет».
	SentAt time.Time
}

// Sent сообщает, что сообщение уже отправлено.
func (n *Notification) Sent() bool { return !n.SentAt.IsZero() }

// OutboxRepo хранит очередь отправки.
type OutboxRepo interface {
	// Schedule ставит сообщения в очередь, пропуская те, что уже стоят.
	//
	// Возвращает, сколько строк добавилось: по этому числу видно, сработал
	// тик или всё уже было запланировано.
	Schedule(ctx context.Context, notifications []Notification) (int, error)

	// Pending возвращает неотправленные сообщения, чей срок наступил,
	// от самых старых.
	Pending(ctx context.Context, now time.Time, limit int) ([]Notification, error)

	// MarkSent отмечает сообщения отправленными.
	MarkSent(ctx context.Context, ids []NotificationID, at time.Time) error
}

// UserReminder — кому и когда напоминать.
type UserReminder struct {
	UserID   user.ID
	Timezone user.Timezone
	At       user.TimeOfDay
}
