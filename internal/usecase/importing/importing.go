// Package importing грузит слова пользователя из CSV.
//
// Импорт частичный: битая строка отменяет себя, а не весь файл. Человек
// собирал его руками в редакторе таблиц, и требовать безупречности от
// пятисот строк значило бы возвращать ему файл целиком из-за одной опечатки.
package importing

import (
	"context"
	"errors"
	"fmt"
	"io"

	"lexi-bot/internal/adapter/importcsv"
	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/internal/usecase/vocab"
)

// WordAdder добавляет одно слово в личный словарь.
//
// Интерфейс, а не *vocab.Service, по двум причинам. Проверять импорт можно,
// не поднимая весь словарь с его колодами и курсами. И видно, что импорту
// от словаря нужно ровно одно действие, а не весь его набор.
type WordAdder interface {
	Add(ctx context.Context, userID user.ID, word *vocab.Word) (vocab.Added, error)
}

// Deps — зависимости импорта.
type Deps struct {
	// Vocab добавляет слова по одному. Пачкой было бы быстрее, но правила
	// дедупликации живут в словаре, и второй их экземпляр разошёлся бы
	// с первым на первой же правке.
	Vocab WordAdder
	Jobs  port.ImportRepo
}

// Service — сценарий импорта.
type Service struct {
	deps Deps
}

// New создаёт сценарий.
func New(deps Deps) (*Service, error) {
	switch {
	case deps.Vocab == nil:
		return nil, errors.New("импорту нужен сценарий личного словаря")
	case deps.Jobs == nil:
		return nil, errors.New("импорту нужен ImportRepo")
	}
	return &Service{deps: deps}, nil
}

// Report — итог импорта.
type Report struct {
	Job port.ImportJob
	// Imported — сколько слов добавлено.
	Imported int
	// Duplicates — сколько строк пропущено потому, что слово уже есть.
	// Это не ошибка: человек мог прислать файл второй раз.
	Duplicates int
	// Errors — отвергнутые строки с номерами и причинами.
	Errors []port.ImportError
}

// Failed сообщает, что импорт не удался целиком.
func (r *Report) Failed() bool { return r.Job.Status == port.ImportFailed }

// Import разбирает файл и добавляет из него слова.
//
// Задание пишется в любом случае — и на удачном импорте, и на провальном:
// человек должен иметь возможность вернуться и посмотреть, что случилось,
// а сводка в чате теряется под следующими сообщениями.
func (s *Service) Import(ctx context.Context, userID user.ID, fileName string, r io.Reader) (Report, error) {
	job, err := s.deps.Jobs.Create(ctx, &port.ImportJob{
		UserID: userID, FileName: fileName, Status: port.ImportRunning,
	})
	if err != nil {
		return Report{}, fmt.Errorf("завести задание импорта: %w", err)
	}

	parsed, parseErr := importcsv.Parse(r)
	if parseErr != nil {
		// Файл испорчен целиком: читать нечего, и добавлять нечего.
		job.Status = port.ImportFailed
		if err := s.deps.Jobs.Update(ctx, &job); err != nil {
			return Report{}, fmt.Errorf("сохранить задание импорта: %w", err)
		}
		return Report{Job: job}, parseErr
	}

	report := Report{Errors: parsed.Errors}
	for i := range parsed.Rows {
		row := &parsed.Rows[i]

		added, err := s.deps.Vocab.Add(ctx, userID, &vocab.Word{
			Term:         row.Term,
			Translations: row.Translations,
			Reading:      row.Reading,
			Example:      row.Example,
			POS:          row.POS,
		})
		switch {
		case errors.Is(err, vocab.ErrNoCourse):
			// Учить нечего: языки брать неоткуда, и остальные строки
			// упрутся в то же самое. Останавливаемся.
			job.Status = port.ImportFailed
			if updateErr := s.deps.Jobs.Update(ctx, &job); updateErr != nil {
				return Report{}, fmt.Errorf("сохранить задание импорта: %w", updateErr)
			}
			return Report{Job: job}, err
		case err != nil && isRowProblem(err):
			// Строка не прошла проверку домена — это ошибка строки,
			// а не повод бросить файл.
			report.Errors = append(report.Errors, port.ImportError{
				Line: row.Line, Reason: err.Error(),
			})
			continue
		case err != nil:
			// Сломалось хранилище: продолжать бессмысленно, следующая
			// строка упрётся в то же.
			job.Status = port.ImportFailed
			if updateErr := s.deps.Jobs.Update(ctx, &job); updateErr != nil {
				return Report{}, fmt.Errorf("сохранить задание импорта: %w", updateErr)
			}
			return Report{Job: job}, err
		}

		if added.Outcome == vocab.OutcomeAdded {
			report.Imported++
		} else {
			report.Duplicates++
		}
	}

	job.Status = port.ImportDone
	job.RowsTotal = parsed.Total
	job.RowsImported = report.Imported
	job.RowsFailed = len(report.Errors)
	job.Errors = report.Errors
	if err := s.deps.Jobs.Update(ctx, &job); err != nil {
		return Report{}, fmt.Errorf("сохранить задание импорта: %w", err)
	}

	report.Job = job
	return report, nil
}

// isRowProblem отличает придирку к строке от поломки бота.
//
// Различие важное: на первой мы пишем номер строки в отчёт и идём дальше,
// на второй — останавливаемся, потому что следующая строка упрётся в то же.
func isRowProblem(err error) bool {
	return errors.Is(err, lexicon.ErrRequired) ||
		errors.Is(err, lexicon.ErrTooLong) ||
		errors.Is(err, lexicon.ErrInvalid)
}
