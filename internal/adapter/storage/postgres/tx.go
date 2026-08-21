package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// txKey — ключ транзакции в контексте. Собственный тип не даёт ему
// столкнуться с ключами других пакетов.
type txKey struct{}

func txFromContext(ctx context.Context) pgx.Tx {
	tx, _ := ctx.Value(txKey{}).(pgx.Tx)
	return tx
}

// TxManager выполняет функции в транзакции.
type TxManager struct {
	pool *pgxpool.Pool
}

// NewTxManager создаёт менеджер транзакций поверх пула.
func NewTxManager(pool *pgxpool.Pool) *TxManager {
	return &TxManager{pool: pool}
}

// InTx выполняет fn в транзакции: при ошибке не изменяется ничего.
//
// Если транзакция в контексте уже есть, вторая не открывается — fn просто
// выполняется внутри текущей. Вложенные транзакции через SAVEPOINT здесь
// были бы вредны: сценарий, вызывающий другой сценарий, ожидает, что либо
// применится всё, либо ничего, а частичный откат вложенного куска дал бы
// наполовину применённое изменение.
func (m *TxManager) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if txFromContext(ctx) != nil {
		return fn(ctx)
	}

	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("открыть транзакцию: %w", err)
	}

	// Откат после успешного коммита — не ошибка, а no-op, поэтому его можно
	// смело держать в defer: он страхует от паники внутри fn.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if err := fn(context.WithValue(ctx, txKey{}, tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("зафиксировать транзакцию: %w", err)
	}
	return nil
}
