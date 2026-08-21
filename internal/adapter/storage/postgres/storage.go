// Package postgres реализует порты-репозитории поверх PostgreSQL.
//
// Здесь и только здесь живёт SQL. Сценарии работают с интерфейсами из
// usecase/port, поэтому ни один тип pgx не поднимается выше этого пакета,
// а запросы остаются видимыми — без ORM, которая прячет их от глаз.
//
// Каждый репозиторий получает исполнителя запросов через db(ctx): если
// вызов идёт внутри TxManager.InTx, это транзакция, иначе — пул. Благодаря
// этому один и тот же метод работает и сам по себе, и как часть более
// крупной транзакции, и знать об этом ему не нужно.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"lexi-bot/internal/usecase/port"
)

// Коды ошибок PostgreSQL, которые для приложения означают не аварию,
// а ожидаемый ответ.
const (
	codeUniqueViolation     = "23505"
	codeForeignKeyViolation = "23503"
	codeCheckViolation      = "23514"
)

// queryer — то, что умеет выполнять запросы: и пул, и транзакция.
type queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// base — общая часть репозиториев.
type base struct {
	pool *pgxpool.Pool
}

// db возвращает исполнителя запросов для текущего контекста: транзакцию,
// если она открыта, иначе пул.
func (b base) db(ctx context.Context) queryer {
	if tx := txFromContext(ctx); tx != nil {
		return tx
	}
	return b.pool
}

// inTx выполняет fn в транзакции: в уже открытой, если она есть в контексте,
// иначе в собственной. Нужен там, где одного запроса мало, а сценарий может
// и не открывать транзакцию сам — например, при добавлении слов в колоду,
// после которого пересчитывается её размер.
func (b base) inTx(ctx context.Context, fn func(q queryer) error) error {
	if tx := txFromContext(ctx); tx != nil {
		return fn(tx)
	}
	return pgx.BeginFunc(ctx, b.pool, func(tx pgx.Tx) error { return fn(tx) })
}

// wrap переводит ошибку драйвера в ошибку приложения, добавляя к ней
// описание операции: «сохранить настройки: запись не найдена» читается
// в логе куда лучше, чем голое no rows in result set.
func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", op, port.ErrNotFound)
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case codeUniqueViolation:
			return fmt.Errorf("%s: %w (%s)", op, port.ErrConflict, pgErr.ConstraintName)
		case codeForeignKeyViolation:
			return fmt.Errorf("%s: %w (%s)", op, port.ErrNotFound, pgErr.ConstraintName)
		case codeCheckViolation:
			return fmt.Errorf("%s: %w (%s)", op, port.ErrInvalidData, pgErr.ConstraintName)
		}
	}
	return fmt.Errorf("%s: %w", op, err)
}

// requireRows превращает «запрос никого не задел» в ErrNotFound: UPDATE
// по несуществующему идентификатору — это не успех.
func requireRows(op string, tag pgconn.CommandTag, err error) error {
	if err != nil {
		return wrap(op, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%s: %w", op, port.ErrNotFound)
	}
	return nil
}
