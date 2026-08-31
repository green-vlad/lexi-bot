package importing_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"lexi-bot/internal/adapter/importcsv"
	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/importing"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/internal/usecase/vocab"
)

const owner = user.ID(42)

// fakeAdder ведёт себя как личный словарь: помнит добавленное, второй раз
// то же слово не заводит и отвергает то, что не проходит проверку.
type fakeAdder struct {
	added    []string
	failOn   map[string]error
	inCourse map[string]bool
}

func (f *fakeAdder) Add(_ context.Context, _ user.ID, word *vocab.Word) (vocab.Added, error) {
	if err, ok := f.failOn[word.Term]; ok {
		return vocab.Added{}, err
	}
	if f.inCourse[word.Term] {
		return vocab.Added{Outcome: vocab.OutcomeInCourse}, nil
	}
	for _, term := range f.added {
		if term == word.Term {
			return vocab.Added{Outcome: vocab.OutcomeAlreadyPersonal}, nil
		}
	}
	f.added = append(f.added, word.Term)
	return vocab.Added{Outcome: vocab.OutcomeAdded}, nil
}

// fakeJobs хранит задания импорта так же, как база: Create присваивает
// идентификатор, Update меняет уже существующую запись.
type fakeJobs struct {
	jobs     map[port.ImportJobID]port.ImportJob
	nextID   port.ImportJobID
	failWith error
}

func newFakeJobs() *fakeJobs {
	return &fakeJobs{jobs: map[port.ImportJobID]port.ImportJob{}}
}

func (f *fakeJobs) Create(_ context.Context, job *port.ImportJob) (port.ImportJob, error) {
	if f.failWith != nil {
		return port.ImportJob{}, f.failWith
	}
	f.nextID++
	job.ID = f.nextID
	f.jobs[job.ID] = *job
	return *job, nil
}

func (f *fakeJobs) Update(_ context.Context, job *port.ImportJob) error {
	if f.failWith != nil {
		return f.failWith
	}
	if _, ok := f.jobs[job.ID]; !ok {
		return port.ErrNotFound
	}
	f.jobs[job.ID] = *job
	return nil
}

func (f *fakeJobs) ByID(_ context.Context, id port.ImportJobID) (port.ImportJob, error) {
	if job, ok := f.jobs[id]; ok {
		return job, nil
	}
	return port.ImportJob{}, port.ErrNotFound
}

type fixture struct {
	service *importing.Service
	adder   *fakeAdder
	jobs    *fakeJobs
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	f := &fixture{
		adder: &fakeAdder{failOn: map[string]error{}, inCourse: map[string]bool{}},
		jobs:  newFakeJobs(),
	}

	service, err := importing.New(importing.Deps{Vocab: f.adder, Jobs: f.jobs})
	if err != nil {
		t.Fatalf("importing.New() вернул ошибку: %v", err)
	}
	f.service = service
	return f
}

func (f *fixture) run(t *testing.T, text string) importing.Report {
	t.Helper()

	report, err := f.service.Import(context.Background(), owner, "words.csv", strings.NewReader(text))
	if err != nil {
		t.Fatalf("Import() вернул ошибку: %v", err)
	}
	return report
}

func TestImportKeepsGoodRowsAmongBad(t *testing.T) {
	t.Parallel()

	report := newFixture(t).run(t, "term,translation\n"+
		"사람,человек\n"+ // строка 2 — годная
		",забыли слово\n"+ // строка 3 — битая
		"물,вода\n"+ // строка 4 — годная
		"불,\n"+ // строка 5 — битая
		"책,книга\n") // строка 6 — годная

	if report.Imported != 3 {
		t.Errorf("загружено %d, ожидалось три", report.Imported)
	}
	if len(report.Errors) != 2 {
		t.Fatalf("ошибок %d, ожидалось две: %+v", len(report.Errors), report.Errors)
	}
	// В отчёте номера строк файла и причины, а не просто «что-то не так».
	if report.Errors[0].Line != 3 || report.Errors[1].Line != 5 {
		t.Errorf("номера строк = %d и %d, ожидались 3 и 5", report.Errors[0].Line, report.Errors[1].Line)
	}
	for _, e := range report.Errors {
		if e.Reason == "" {
			t.Errorf("строка %d без причины", e.Line)
		}
	}
}

func TestImportWritesJob(t *testing.T) {
	t.Parallel()

	fx := newFixture(t)
	report := fx.run(t, "term,translation\n사람,человек\n,забыли\n물,вода\n")

	saved, err := fx.jobs.ByID(context.Background(), report.Job.ID)
	if err != nil {
		t.Fatalf("задание не сохранилось: %v", err)
	}
	if saved.Status != port.ImportDone {
		t.Errorf("статус = %q, ожидалось done", saved.Status)
	}
	if saved.FileName != "words.csv" || saved.UserID != owner {
		t.Errorf("задание = %+v", saved)
	}
	if saved.RowsTotal != 3 || saved.RowsImported != 2 || saved.RowsFailed != 1 {
		t.Errorf("счётчики = %d/%d/%d, ожидались 3/2/1",
			saved.RowsTotal, saved.RowsImported, saved.RowsFailed)
	}
	// Отчёт уезжает в задание целиком: сводка в чате теряется под
	// следующими сообщениями, а задание остаётся.
	if len(saved.Errors) != 1 || saved.Errors[0].Line != 3 {
		t.Errorf("отчёт задания = %+v", saved.Errors)
	}
}

func TestImportCountsDuplicatesApart(t *testing.T) {
	t.Parallel()

	fx := newFixture(t)
	fx.adder.inCourse["물"] = true

	report := fx.run(t, "term,translation\n사람,человек\n사람,человек\n물,вода\n")

	if report.Imported != 1 {
		t.Errorf("загружено %d, ожидалось одно", report.Imported)
	}
	// Повтор и слово из учебной колоды — не ошибки: человек мог прислать
	// файл второй раз или переписать в него часть учебника.
	if report.Duplicates != 2 {
		t.Errorf("повторов %d, ожидалось два", report.Duplicates)
	}
	if len(report.Errors) != 0 {
		t.Errorf("ошибки = %+v, повтор ошибкой не является", report.Errors)
	}
	if report.Job.RowsFailed != 0 {
		t.Errorf("в задании отказов %d, ожидался ноль", report.Job.RowsFailed)
	}
}

func TestImportReportsRowRejectedByVocabulary(t *testing.T) {
	t.Parallel()

	fx := newFixture(t)
	// Словарь может отвергнуть строку по своим правилам — например,
	// если перевод не проходит проверку домена.
	fx.adder.failOn["물"] = fmt.Errorf("перевод: %w", lexicon.ErrTooLong)

	report := fx.run(t, "term,translation\n사람,человек\n물,вода\n책,книга\n")

	if report.Imported != 2 {
		t.Errorf("загружено %d, ожидалось два", report.Imported)
	}
	if len(report.Errors) != 1 || report.Errors[0].Line != 3 {
		t.Fatalf("ошибки = %+v", report.Errors)
	}
	if !strings.Contains(report.Errors[0].Reason, "перевод") {
		t.Errorf("причина = %q", report.Errors[0].Reason)
	}
}

func TestImportStopsOnBrokenStorage(t *testing.T) {
	t.Parallel()

	fx := newFixture(t)
	broken := errors.New("база недоступна")
	fx.adder.failOn["사람"] = broken

	// Поломка хранилища — не придирка к строке: следующая упрётся в то же,
	// и продолжать бессмысленно.
	report, err := fx.service.Import(context.Background(), owner, "words.csv",
		strings.NewReader("term,translation\n사람,человек\n물,вода\n"))
	if !errors.Is(err, broken) {
		t.Fatalf("ошибка = %v, ожидалась поломка хранилища", err)
	}
	if !report.Failed() {
		t.Errorf("статус = %q, ожидалось failed", report.Job.Status)
	}
	if len(fx.adder.added) != 0 {
		t.Errorf("добавлено %v, ожидалось ничего", fx.adder.added)
	}
}

func TestImportStopsWithoutCourse(t *testing.T) {
	t.Parallel()

	fx := newFixture(t)
	fx.adder.failOn["사람"] = vocab.ErrNoCourse

	report, err := fx.service.Import(context.Background(), owner, "words.csv",
		strings.NewReader("term,translation\n사람,человек\n물,вода\n"))
	if !errors.Is(err, vocab.ErrNoCourse) {
		t.Fatalf("ошибка = %v, ожидалась ErrNoCourse", err)
	}
	if !report.Failed() {
		t.Errorf("статус = %q, ожидалось failed", report.Job.Status)
	}
}

func TestImportFailsOnBrokenFile(t *testing.T) {
	t.Parallel()

	fx := newFixture(t)

	report, err := fx.service.Import(context.Background(), owner, "words.csv",
		strings.NewReader("перевод,пример\nчеловек,пример\n"))
	if !errors.Is(err, importcsv.ErrNoHeader) {
		t.Fatalf("ошибка = %v, ожидалась ErrNoHeader", err)
	}
	// Задание всё равно записано: человек должен иметь возможность
	// вернуться и посмотреть, что случилось.
	if !report.Failed() {
		t.Errorf("статус = %q, ожидалось failed", report.Job.Status)
	}
	saved, err := fx.jobs.ByID(context.Background(), report.Job.ID)
	if err != nil {
		t.Fatalf("задание не сохранилось: %v", err)
	}
	if saved.Status != port.ImportFailed {
		t.Errorf("сохранённый статус = %q", saved.Status)
	}
}

func TestImportReportsRepoFailure(t *testing.T) {
	t.Parallel()

	fx := newFixture(t)
	fx.jobs.failWith = errors.New("база недоступна")

	_, err := fx.service.Import(context.Background(), owner, "words.csv",
		strings.NewReader("term,translation\n사람,человек\n"))
	if err == nil {
		t.Error("недоступная база должна быть ошибкой")
	}
}

func TestNewNeedsDependencies(t *testing.T) {
	t.Parallel()

	if _, err := importing.New(importing.Deps{}); err == nil {
		t.Error("сценарий без зависимостей должен быть ошибкой")
	}
}
