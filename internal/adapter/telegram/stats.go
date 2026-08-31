package telegram

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"

	"lexi-bot/internal/usecase/port"
	"lexi-bot/internal/usecase/stats"
)

// Stats — команда /stats.
type Stats struct {
	service   *stats.Service
	messenger port.Messenger
}

// NewStats создаёт хендлер статистики.
func NewStats(service *stats.Service, messenger port.Messenger) (*Stats, error) {
	switch {
	case service == nil:
		return nil, errors.New("статистике нужен сценарий")
	case messenger == nil:
		return nil, errors.New("статистике нужен мессенджер")
	}
	return &Stats{service: service, messenger: messenger}, nil
}

// Register привязывает команду к роутеру.
func (s *Stats) Register(router *Router) {
	router.Command("stats", port.UpdateHandlerFunc(s.show))
}

// show отвечает сводкой.
func (s *Stats) show(ctx context.Context, u *port.Update) error {
	known, ok := UserFrom(ctx)
	if !ok {
		return errors.New("/stats без пользователя")
	}
	localizer, ok := LocalizerFrom(ctx)
	if !ok {
		return errors.New("нет локализатора: middleware локализации не подключён")
	}

	summary, err := s.service.Of(ctx, known.ID)
	if err != nil {
		return err
	}
	if !summary.HasCourses {
		return Reply(s.messenger, "stats.no_course").Handle(ctx, u)
	}

	text, err := statsText(localizer, &summary)
	if err != nil {
		return err
	}
	_, err = s.messenger.SendMessage(ctx, port.OutgoingMessage{ChatID: u.Chat, Text: text})
	return err
}

// statsText собирает сводку.
//
// Строки, за которыми ничего нет, опускаются: «серия 0 дней» и «точность 0%»
// у того, кто ещё не отвечал, — не факты, а упрёки.
func statsText(localizer port.Localizer, summary *stats.Summary) (string, error) {
	lines := []string{mustText(localizer, "stats.title"), ""}

	progress, err := localizer.T("stats.progress", port.Args{
		"Learned":   summary.Learned,
		"Learning":  summary.Learning,
		"Remaining": summary.NewRemaining,
	})
	if err != nil {
		return "", err
	}
	lines = append(lines, progress)

	if summary.Known > 0 {
		lines = append(lines, plural(localizer, "stats.known", summary.Known))
	}
	if summary.Streak > 0 {
		lines = append(lines, plural(localizer, "stats.streak", summary.Streak))
	}

	if accuracy := accuracyLine(localizer, summary); accuracy != "" {
		lines = append(lines, "", accuracy)
	}
	if forecast := forecastLine(localizer, summary.Forecast); forecast != "" {
		lines = append(lines, "", forecast)
	}
	return strings.Join(lines, "\n"), nil
}

// accuracyLine показывает точность за неделю и за месяц.
//
// Месяц показывается только когда он говорит не то же, что неделя: две
// одинаковые строки подряд ничего не добавляют.
func accuracyLine(localizer port.Localizer, summary *stats.Summary) string {
	if !summary.Week.Known() && !summary.Month.Known() {
		return ""
	}

	if !summary.Week.Known() {
		text, err := localizer.T("stats.accuracy_month", port.Args{"Percent": percent(summary.Month)})
		if err != nil {
			return ""
		}
		return text
	}

	week, month := percent(summary.Week), percent(summary.Month)
	if !summary.Month.Known() || week == month {
		text, err := localizer.T("stats.accuracy_week", port.Args{"Percent": week})
		if err != nil {
			return ""
		}
		return text
	}

	text, err := localizer.T("stats.accuracy", port.Args{"Week": week, "Month": month})
	if err != nil {
		return ""
	}
	return text
}

// forecastLine рисует нагрузку на ближайшие дни.
//
// Пустой прогноз не показывается вовсе: строка из одних нулей сообщает
// человеку ровно то же, что её отсутствие, только длиннее.
func forecastLine(localizer port.Localizer, forecast []int) string {
	total := 0
	for _, count := range forecast {
		total += count
	}
	if total == 0 {
		return ""
	}

	parts := make([]string, 0, len(forecast))
	for _, count := range forecast {
		parts = append(parts, strconv.Itoa(count))
	}

	text, err := localizer.T("stats.forecast", port.Args{
		"Days":  strings.Join(parts, " · "),
		"Total": total,
	})
	if err != nil {
		return ""
	}
	return text
}

// percent округляет долю к ближайшему целому проценту: 83.4% — это 83%,
// а не 83.4% и уж точно не 84%.
func percent(a stats.Accuracy) int {
	return int(math.Round(a.Share() * 100))
}
