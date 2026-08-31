package telegram_test

import (
	"context"
	"strings"
	"testing"

	"lexi-bot/internal/adapter/importcsv"
	"lexi-bot/internal/adapter/telegram"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/importing"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/internal/usecase/vocab"
)

type importFixture struct {
	router    *telegram.Router
	messenger *fakeMessenger
	decks     *stubDecks
	lexemes   *stubLexemes
	courses   *stubCourses
	users     *fakeUsers
	jobs      *stubImportJobs
}

// stubImportJobs хранит задания импорта так же, как база.
type stubImportJobs struct {
	jobs   map[port.ImportJobID]port.ImportJob
	nextID port.ImportJobID
}

func (s *stubImportJobs) Create(_ context.Context, job *port.ImportJob) (port.ImportJob, error) {
	s.nextID++
	job.ID = s.nextID
	s.jobs[job.ID] = *job
	return *job, nil
}

func (s *stubImportJobs) Update(_ context.Context, job *port.ImportJob) error {
	if _, ok := s.jobs[job.ID]; !ok {
		return port.ErrNotFound
	}
	s.jobs[job.ID] = *job
	return nil
}

func (s *stubImportJobs) ByID(_ context.Context, id port.ImportJobID) (port.ImportJob, error) {
	if job, ok := s.jobs[id]; ok {
		return job, nil
	}
	return port.ImportJob{}, port.ErrNotFound
}

func newImportFixture(t *testing.T) *importFixture {
	t.Helper()

	f := &importFixture{
		messenger: &fakeMessenger{files: map[string][]byte{}},
		decks:     newStubDecks(),
		lexemes:   newStubLexemes(),
		courses:   newStubCourses(),
		users:     newFakeUsers(),
		jobs:      &stubImportJobs{jobs: map[port.ImportJobID]port.ImportJob{}},
	}

	owner := mustUser(t, 555, user.UILangRU)
	saved, _, err := f.users.Ensure(context.Background(), &owner)
	if err != nil {
		t.Fatalf("Ensure() вернул ошибку: %v", err)
	}

	course, err := f.courses.Ensure(context.Background(), study.Course{
		UserID: int64(saved.ID), DeckID: 1, TranslationLang: langRU, Status: study.CourseActive,
	})
	if err != nil {
		t.Fatalf("Ensure() вернул ошибку: %v", err)
	}
	if err := f.users.SetCurrentCourse(context.Background(), saved.ID, course.ID); err != nil {
		t.Fatalf("SetCurrentCourse() вернул ошибку: %v", err)
	}

	vocabService, err := vocab.New(vocab.Deps{
		Users: f.users, Decks: f.decks, Lexemes: f.lexemes, Courses: f.courses,
	})
	if err != nil {
		t.Fatalf("vocab.New() вернул ошибку: %v", err)
	}

	service, err := importing.New(importing.Deps{Vocab: vocabService, Jobs: f.jobs})
	if err != nil {
		t.Fatalf("importing.New() вернул ошибку: %v", err)
	}

	handler, err := telegram.NewImporting(service, f.messenger)
	if err != nil {
		t.Fatalf("NewImporting() вернул ошибку: %v", err)
	}

	f.router = telegram.NewRouter()
	f.router.Use(
		telegram.Identify(f.users, quietLogger()),
		telegram.Localize(testCatalog(t)),
	)
	handler.Register(f.router)
	return f
}

// upload присылает боту файл.
func (f *importFixture) upload(t *testing.T, doc *port.IncomingDocument, content string) {
	t.Helper()

	if content != "" {
		f.messenger.files[doc.FileID] = []byte(content)
	}
	update := &port.Update{
		ID:       3,
		Chat:     777,
		Sender:   port.Sender{TelegramID: 555, Username: "durov", LanguageCode: "ru"},
		Document: doc,
	}
	if err := f.router.Handle(context.Background(), update); err != nil {
		t.Fatalf("Handle() вернул ошибку: %v", err)
	}
}

func csvDoc(size int64) *port.IncomingDocument {
	return &port.IncomingDocument{
		FileID: "file-1", FileName: "words.csv", MIMEType: "text/csv", Size: size,
	}
}

func TestImportLoadsWords(t *testing.T) {
	t.Parallel()

	f := newImportFixture(t)
	content := "term,translation\n냉장고,холодильник\n옷장,шкаф\n"

	f.upload(t, csvDoc(int64(len(content))), content)

	text := f.messenger.last(t).Text
	if !strings.Contains(text, "Загружено 2 слова") {
		t.Errorf("сводка = %q", text)
	}
	// Ошибок не было — файла с отчётом быть не должно.
	if len(f.messenger.docs) != 0 {
		t.Errorf("отправлен отчёт, хотя ошибок нет: %+v", f.messenger.docs)
	}

	if _, err := f.lexemes.ByTerm(context.Background(), langKO, "냉장고", 1); err != nil {
		t.Errorf("слово не сохранилось: %v", err)
	}
	// Файл качается с объявленным пределом: читать в память то, что пришло
	// из внешнего мира, без предела нельзя.
	if len(f.messenger.downloaded) != 1 || f.messenger.downloaded[0] != importcsv.MaxFileSize {
		t.Errorf("предел скачивания = %v", f.messenger.downloaded)
	}
}

func TestImportSendsErrorReport(t *testing.T) {
	t.Parallel()

	f := newImportFixture(t)
	content := "term,translation\n냉장고,холодильник\n,забыли слово\n옷장,\n"

	f.upload(t, csvDoc(int64(len(content))), content)

	text := f.messenger.last(t).Text
	if !strings.Contains(text, "Загружено 1 слово") {
		t.Errorf("сводка = %q", text)
	}
	if !strings.Contains(text, "2 строки разобрать не удалось") {
		t.Errorf("сводка = %q, ожидалось число отвергнутых", text)
	}

	// Отчёт уезжает файлом: ошибок бывают сотни, и таким сообщением
	// Telegram подавился бы.
	doc := f.messenger.lastDoc(t)
	if !strings.HasSuffix(doc.FileName, ".csv") {
		t.Errorf("имя отчёта = %q", doc.FileName)
	}
	report := string(doc.Content)
	if !strings.HasPrefix(report, "\uFEFF") {
		t.Error("в отчёте нет метки порядка байтов: редактор таблиц откроет его не в UTF-8")
	}
	// В отчёте номера строк файла и причины.
	for _, want := range []string{"строка", "причина", "3", "4"} {
		if !strings.Contains(report, want) {
			t.Errorf("отчёт = %q, в нём нет %q", report, want)
		}
	}
}

func TestImportCountsWordsAlreadyKnown(t *testing.T) {
	t.Parallel()

	f := newImportFixture(t)
	content := "term,translation\n냉장고,холодильник\n냉장고,холодильник\n"

	f.upload(t, csvDoc(int64(len(content))), content)

	text := f.messenger.last(t).Text
	// Повтор не ошибка: человек мог прислать файл второй раз.
	if !strings.Contains(text, "уже был") {
		t.Errorf("сводка = %q, ожидалось про повтор", text)
	}
	if strings.Contains(text, "разобрать не удалось") {
		t.Errorf("сводка = %q: повтор назван ошибкой", text)
	}
}

func TestImportRejectsTooLargeFileWithoutDownloading(t *testing.T) {
	t.Parallel()

	f := newImportFixture(t)

	// Размер известен из апдейта: качать два мегабайта, чтобы потом сказать
	// «слишком много», — расточительство.
	f.upload(t, csvDoc(importcsv.MaxFileSize+1), "")

	if got := f.messenger.last(t).Text; !strings.Contains(got, "двух мегабайт") {
		t.Errorf("ответ = %q", got)
	}
	if len(f.messenger.downloaded) != 0 {
		t.Error("слишком большой файл всё равно качали")
	}
}

func TestImportRejectsNotCSV(t *testing.T) {
	t.Parallel()

	f := newImportFixture(t)

	f.upload(t, &port.IncomingDocument{
		FileID: "file-2", FileName: "конспект.pdf", MIMEType: "application/pdf", Size: 100,
	}, "")

	if got := f.messenger.last(t).Text; !strings.Contains(got, "только CSV") {
		t.Errorf("ответ = %q", got)
	}
	if len(f.messenger.downloaded) != 0 {
		t.Error("не-CSV всё равно качали")
	}
}

func TestImportAcceptsCSVByExtension(t *testing.T) {
	t.Parallel()

	f := newImportFixture(t)
	content := "term,translation\n냉장고,холодильник\n"

	// Некоторые редакторы отдают файл как поток байтов: отказывать по типу
	// значило бы отвергать нормальные словари.
	f.upload(t, &port.IncomingDocument{
		FileID: "file-3", FileName: "Мои слова.CSV",
		MIMEType: "application/octet-stream", Size: int64(len(content)),
	}, content)

	if got := f.messenger.last(t).Text; !strings.Contains(got, "Загружено") {
		t.Errorf("ответ = %q", got)
	}
}

func TestImportExplainsBrokenFile(t *testing.T) {
	t.Parallel()

	f := newImportFixture(t)
	content := "перевод,пример\nчеловек,пример\n"

	f.upload(t, csvDoc(int64(len(content))), content)

	got := f.messenger.last(t).Text
	if !strings.Contains(got, "Не получилось прочитать файл") {
		t.Errorf("ответ = %q", got)
	}
	// И подсказка, куда смотреть.
	if !strings.Contains(got, "/import") {
		t.Errorf("ответ = %q, ожидалась подсказка про /import", got)
	}
}

func TestImportWithoutCourse(t *testing.T) {
	t.Parallel()

	f := newImportFixture(t)
	f.courses.byID = map[study.CourseID]study.Course{}
	content := "term,translation\n냉장고,холодильник\n"

	f.upload(t, csvDoc(int64(len(content))), content)

	if got := f.messenger.last(t).Text; !strings.Contains(got, "/start") {
		t.Errorf("ответ = %q, ожидалась подсказка про /start", got)
	}
}

func TestImportCommandExplainsFormat(t *testing.T) {
	t.Parallel()

	f := newImportFixture(t)

	if err := f.router.Handle(context.Background(), message("/import")); err != nil {
		t.Fatalf("Handle() вернул ошибку: %v", err)
	}

	got := f.messenger.last(t).Text
	for _, want := range []string{"term,translation", "UTF-8", "5000"} {
		if !strings.Contains(got, want) {
			t.Errorf("справка = %q, в ней нет %q", got, want)
		}
	}
}

func TestImportingNeedsDependencies(t *testing.T) {
	t.Parallel()

	if _, err := telegram.NewImporting(nil, nil); err == nil {
		t.Error("хендлер без зависимостей должен быть ошибкой")
	}
}
