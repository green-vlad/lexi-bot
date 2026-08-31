package telegram

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"lexi-bot/internal/adapter/importcsv"
	"lexi-bot/internal/usecase/importing"
	"lexi-bot/internal/usecase/port"
	"lexi-bot/internal/usecase/vocab"
)

// errorReportName — имя файла с отвергнутыми строками.
const errorReportName = "ошибки-импорта.csv"

// maxReportRows — сколько строк отчёта уезжает в файл.
//
// Предел есть потому, что отчёт на пять тысяч строк — это не отчёт,
// а тот же самый файл, только с колонкой «почему». Если ошибок столько,
// дело не в опечатках, а в формате, и первой сотни хватит, чтобы это понять.
const maxReportRows = 100

// Importing — приём CSV со словами пользователя.
//
// Диалога здесь нет: файл можно прислать когда угодно, а /import только
// рассказывает, какой файл подойдёт. Заводить состояние ради одного
// сообщения значило бы требовать от человека сначала объявить о намерении,
// а потом его исполнить.
type Importing struct {
	service   *importing.Service
	messenger port.Messenger
}

// NewImporting создаёт хендлер импорта.
func NewImporting(service *importing.Service, messenger port.Messenger) (*Importing, error) {
	switch {
	case service == nil:
		return nil, errors.New("импорту нужен сценарий")
	case messenger == nil:
		return nil, errors.New("импорту нужен мессенджер")
	}
	return &Importing{service: service, messenger: messenger}, nil
}

// Register привязывает команду и приём файлов к роутеру.
func (i *Importing) Register(router *Router) {
	router.Command("import", Reply(i.messenger, "import.how"))
	router.Document(port.UpdateHandlerFunc(i.receive))
}

// receive принимает присланный файл.
func (i *Importing) receive(ctx context.Context, u *port.Update) error {
	known, ok := UserFrom(ctx)
	if !ok {
		return errors.New("файл без пользователя")
	}
	doc := u.Document

	if !looksLikeCSV(doc) {
		return Reply(i.messenger, "import.not_csv").Handle(ctx, u)
	}
	// Размер проверяется до скачивания: качать два мегабайта, чтобы потом
	// сказать «слишком много», — расточительство и по трафику, и по времени.
	if doc.Size > importcsv.MaxFileSize {
		return Reply(i.messenger, "import.too_large").Handle(ctx, u)
	}

	content, err := i.messenger.DownloadFile(ctx, doc.FileID, importcsv.MaxFileSize)
	if err != nil {
		return err
	}

	report, err := i.service.Import(ctx, known.ID, doc.FileName, bytes.NewReader(content))
	switch {
	case errors.Is(err, vocab.ErrNoCourse):
		return Reply(i.messenger, "import.no_course").Handle(ctx, u)
	case err != nil && isFileProblem(err):
		// Файл нечитаем целиком — человеку нужно знать, чем именно.
		return i.reject(ctx, u, err)
	case err != nil:
		return err
	}

	return i.summarize(ctx, u, &report)
}

// isFileProblem отличает придирку к файлу от поломки бота.
func isFileProblem(err error) bool {
	for _, known := range []error{
		importcsv.ErrEmpty, importcsv.ErrNoHeader, importcsv.ErrNotUTF8,
		importcsv.ErrTooLarge, importcsv.ErrTooManyRows,
	} {
		if errors.Is(err, known) {
			return true
		}
	}
	return false
}

// reject объясняет, почему файл не пригодился.
func (i *Importing) reject(ctx context.Context, u *port.Update, cause error) error {
	localizer, err := i.localizer(ctx)
	if err != nil {
		return err
	}

	text, err := localizer.T("import.rejected", port.Args{"Reason": cause.Error()})
	if err != nil {
		return err
	}
	_, err = i.messenger.SendMessage(ctx, port.OutgoingMessage{ChatID: u.Chat, Text: text})
	return err
}

// summarize отвечает сводкой и, если было что отвергнуть, файлом с ошибками.
func (i *Importing) summarize(ctx context.Context, u *port.Update, report *importing.Report) error {
	localizer, err := i.localizer(ctx)
	if err != nil {
		return err
	}

	lines := []string{plural(localizer, "import.imported", report.Imported)}
	if report.Duplicates > 0 {
		// Повтор не ошибка: человек мог прислать файл второй раз.
		lines = append(lines, plural(localizer, "import.duplicates", report.Duplicates))
	}
	if len(report.Errors) > 0 {
		lines = append(lines, plural(localizer, "import.failed", len(report.Errors)))
	}

	text := strings.Join(lines, "\n")
	if _, err := i.messenger.SendMessage(ctx, port.OutgoingMessage{ChatID: u.Chat, Text: text}); err != nil {
		return err
	}

	if len(report.Errors) == 0 {
		return nil
	}

	content, err := errorReport(localizer, report.Errors)
	if err != nil {
		return err
	}
	return i.messenger.SendDocument(ctx, port.Document{
		ChatID:   u.Chat,
		FileName: errorReportName,
		Caption:  mustText(localizer, "import.report_caption"),
		Content:  content,
	})
}

// errorReport собирает CSV с отвергнутыми строками.
//
// Файлом, а не сообщением: ошибок бывает несколько сотен, и Telegram
// такое сообщение просто не пропустит. К тому же файл открывается тем же
// редактором таблиц, в котором человек правит словарь.
func errorReport(localizer port.Localizer, errs []port.ImportError) ([]byte, error) {
	var buf bytes.Buffer
	// Метка порядка байтов — чтобы редакторы таблиц открыли отчёт в UTF-8,
	// а не в кодировке системы: иначе вместо причин будет абракадабра.
	buf.WriteString("\uFEFF")

	w := csv.NewWriter(&buf)
	header := []string{
		mustText(localizer, "import.report_line"),
		mustText(localizer, "import.report_reason"),
	}
	if err := w.Write(header); err != nil {
		return nil, fmt.Errorf("записать отчёт: %w", err)
	}

	for idx, e := range errs {
		if idx >= maxReportRows {
			break
		}
		if err := w.Write([]string{strconv.Itoa(e.Line), e.Reason}); err != nil {
			return nil, fmt.Errorf("записать отчёт: %w", err)
		}
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return nil, fmt.Errorf("записать отчёт: %w", err)
	}
	return buf.Bytes(), nil
}

// looksLikeCSV решает, стоит ли вообще качать файл.
//
// Проверяются и тип, и расширение: Telegram отдаёт text/csv не всегда —
// у файла из некоторых редакторов тип приезжает application/octet-stream,
// и отказывать по нему значило бы отвергать нормальные словари.
func looksLikeCSV(doc *port.IncomingDocument) bool {
	if strings.HasPrefix(doc.MIMEType, "text/csv") {
		return true
	}
	return strings.HasSuffix(strings.ToLower(doc.FileName), ".csv")
}

func (i *Importing) localizer(ctx context.Context) (port.Localizer, error) {
	localizer, ok := LocalizerFrom(ctx)
	if !ok {
		return nil, errors.New("нет локализатора: middleware локализации не подключён")
	}
	return localizer, nil
}
