//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"lexi-bot/internal/adapter/storage/postgres"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/test/pgtest"
)

func TestImportJobRoundTrip(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewImportRepo(pool)

	f := newCourse(t, pool, 1)

	job, err := repo.Create(ctx, &port.ImportJob{
		UserID: f.user.ID, FileName: "words.csv", Status: port.ImportRunning,
	})
	if err != nil {
		t.Fatalf("Create() вернул ошибку: %v", err)
	}
	if job.ID == 0 {
		t.Fatal("задание заведено без идентификатора")
	}
	if job.CreatedAt.IsZero() {
		t.Error("момент создания не проставлен")
	}
	// Пустой отчёт записывается как «[]», а не как null: колонка объявлена
	// NOT NULL, и null сломал бы вставку на первом безошибочном импорте.
	if len(job.Errors) != 0 {
		t.Errorf("отчёт = %+v, ожидался пустой", job.Errors)
	}

	job.Status = port.ImportDone
	job.RowsTotal, job.RowsImported, job.RowsFailed = 5, 3, 2
	job.Errors = []port.ImportError{
		{Line: 3, Reason: "пустое слово"},
		{Line: 7, Reason: "перевод длиннее 300 символов"},
	}
	if err := repo.Update(ctx, &job); err != nil {
		t.Fatalf("Update() вернул ошибку: %v", err)
	}

	got, err := repo.ByID(ctx, job.ID)
	if err != nil {
		t.Fatalf("ByID() вернул ошибку: %v", err)
	}
	if got.Status != port.ImportDone || got.FileName != "words.csv" || got.UserID != f.user.ID {
		t.Errorf("задание = %+v", got)
	}
	if got.RowsTotal != 5 || got.RowsImported != 3 || got.RowsFailed != 2 {
		t.Errorf("счётчики = %d/%d/%d, ожидались 5/3/2",
			got.RowsTotal, got.RowsImported, got.RowsFailed)
	}
	// Отчёт переживает поездку в JSONB и обратно вместе с номерами строк:
	// по ним человек и будет искать ошибки в своём файле.
	if len(got.Errors) != 2 {
		t.Fatalf("отчёт = %+v, ожидались две строки", got.Errors)
	}
	if got.Errors[0].Line != 3 || got.Errors[0].Reason != "пустое слово" {
		t.Errorf("первая ошибка = %+v", got.Errors[0])
	}
	if got.Errors[1].Line != 7 {
		t.Errorf("вторая ошибка = %+v", got.Errors[1])
	}
}

func TestImportJobNotFound(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	repo := postgres.NewImportRepo(pool)

	if _, err := repo.ByID(ctx, 99999); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("ByID() = %v, ожидалась ErrNotFound", err)
	}

	// Обновление несуществующего задания — тоже «не найдено», а не тихий
	// успех: молчаливая потеря записи хуже ошибки.
	err := repo.Update(ctx, &port.ImportJob{ID: 99999, Status: port.ImportDone})
	if !errors.Is(err, port.ErrNotFound) {
		t.Errorf("Update() = %v, ожидалась ErrNotFound", err)
	}
}
