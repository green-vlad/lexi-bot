package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// ImportRepo хранит задания импорта CSV.
type ImportRepo struct {
	base
}

// NewImportRepo создаёт репозиторий заданий импорта.
func NewImportRepo(pool *pgxpool.Pool) *ImportRepo {
	return &ImportRepo{base: base{pool: pool}}
}

var _ port.ImportRepo = (*ImportRepo)(nil)

const importColumns = `id, user_id, file_name, status, rows_total, rows_imported,
	rows_failed, error_report, created_at`

// Create заводит задание импорта.
func (r *ImportRepo) Create(ctx context.Context, job *port.ImportJob) (port.ImportJob, error) {
	const op = "завести задание импорта"
	const query = `
		INSERT INTO import_jobs (user_id, file_name, status, rows_total,
		                         rows_imported, rows_failed, error_report)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING ` + importColumns

	report, err := marshalReport(job.Errors)
	if err != nil {
		return port.ImportJob{}, wrap(op, err)
	}

	row := r.db(ctx).QueryRow(ctx, query, int64(job.UserID), job.FileName,
		string(job.Status), job.RowsTotal, job.RowsImported, job.RowsFailed, report)

	var saved port.ImportJob
	if err := scanImportJob(row, &saved); err != nil {
		return port.ImportJob{}, wrap(op, err)
	}
	return saved, nil
}

// Update сохраняет ход и итог импорта.
func (r *ImportRepo) Update(ctx context.Context, job *port.ImportJob) error {
	const op = "обновить задание импорта"
	const query = `
		UPDATE import_jobs
		SET status = $2, rows_total = $3, rows_imported = $4,
		    rows_failed = $5, error_report = $6
		WHERE id = $1`

	report, err := marshalReport(job.Errors)
	if err != nil {
		return wrap(op, err)
	}

	tag, err := r.db(ctx).Exec(ctx, query, int64(job.ID), string(job.Status),
		job.RowsTotal, job.RowsImported, job.RowsFailed, report)
	if err != nil {
		return wrap(op, err)
	}
	if tag.RowsAffected() == 0 {
		return port.ErrNotFound
	}
	return nil
}

// ByID возвращает задание импорта.
func (r *ImportRepo) ByID(ctx context.Context, id port.ImportJobID) (port.ImportJob, error) {
	const op = "найти задание импорта"
	const query = "SELECT " + importColumns + " FROM import_jobs WHERE id = $1"

	var job port.ImportJob
	err := scanImportJob(r.db(ctx).QueryRow(ctx, query, int64(id)), &job)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ImportJob{}, port.ErrNotFound
	}
	if err != nil {
		return port.ImportJob{}, wrap(op, err)
	}
	return job, nil
}

// marshalReport складывает отвергнутые строки в JSON.
//
// Пустой список записывается как «[]», а не как null: колонка объявлена
// NOT NULL, и null сломал бы вставку на первом же безошибочном импорте.
func marshalReport(errs []port.ImportError) ([]byte, error) {
	if len(errs) == 0 {
		return []byte("[]"), nil
	}
	return json.Marshal(errs)
}

func scanImportJob(r row, job *port.ImportJob) error {
	var (
		id     int64
		userID int64
		status string
		report []byte
	)
	err := r.Scan(&id, &userID, &job.FileName, &status, &job.RowsTotal,
		&job.RowsImported, &job.RowsFailed, &report, &job.CreatedAt)
	if err != nil {
		return err
	}

	job.ID = port.ImportJobID(id)
	job.UserID = user.ID(userID)
	job.Status = port.ImportStatus(status)

	if len(report) > 0 {
		if err := json.Unmarshal(report, &job.Errors); err != nil {
			return err
		}
	}
	return nil
}
