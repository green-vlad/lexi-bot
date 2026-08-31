package port

import (
	"context"
	"time"

	"lexi-bot/internal/domain/user"
)

// ImportStatus — состояние задания импорта.
type ImportStatus string

// Состояния задания импорта.
const (
	ImportPending ImportStatus = "pending"
	ImportRunning ImportStatus = "running"
	ImportDone    ImportStatus = "done"
	ImportFailed  ImportStatus = "failed"
)

// ImportJobID — идентификатор задания импорта.
type ImportJobID int64

// ImportError — одна отвергнутая строка файла.
type ImportError struct {
	// Line — номер строки в файле, считая заголовок: пользователь будет
	// искать ошибку глазами именно по нему.
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

// ImportJob — задание импорта CSV.
type ImportJob struct {
	ID       ImportJobID
	UserID   user.ID
	FileName string
	Status   ImportStatus
	// RowsTotal, RowsImported и RowsFailed складываются в сводку
	// «загружено N, пропущено M».
	RowsTotal    int
	RowsImported int
	RowsFailed   int
	// Errors — отвергнутые строки. Импорт частичный: одна битая строка
	// не отменяет остальные, поэтому список ошибок и результат существуют
	// одновременно.
	Errors    []ImportError
	CreatedAt time.Time
}

// ImportRepo хранит задания импорта.
type ImportRepo interface {
	// Create заводит задание и возвращает его с идентификатором.
	//
	// Указателем не для того, чтобы задание меняли — реализация его
	// не трогает, — а чтобы не копировать отчёт об ошибках на каждый вызов.
	Create(ctx context.Context, job *ImportJob) (ImportJob, error)

	// Update сохраняет ход и итог импорта.
	Update(ctx context.Context, job *ImportJob) error

	// ByID возвращает задание.
	ByID(ctx context.Context, id ImportJobID) (ImportJob, error)
}
