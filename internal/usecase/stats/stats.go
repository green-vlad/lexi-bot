// Package stats собирает сводку о том, как идёт учёба.
//
// Считается она по всем курсам человека сразу: «сколько я знаю» — вопрос
// про него, а не про колоду. Курсы на паузе и в архиве в счёт не идут —
// они убраны из занятий сознательно, и подмешивать их прогресс значило бы
// показывать чужие цифры.
package stats

import (
	"context"
	"errors"
	"fmt"
	"time"

	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// ForecastDays — на сколько дней вперёд считается нагрузка.
//
// Неделя: за меньший срок не видно, разгрузится ли завтрашний завал,
// за больший прогноз всё равно врёт — он не знает, как человек ответит
// на ближайшие карточки.
const ForecastDays = 7

// Периоды, за которые считается точность.
const (
	WeekDays  = 7
	MonthDays = 30
)

// Deps — зависимости сводки.
type Deps struct {
	Users    port.UserRepo
	Courses  port.CourseRepo
	Decks    port.DeckRepo
	Cards    port.CardRepo
	Reviews  port.ReviewRepo
	Settings port.SettingsRepo
	Clock    port.Clock
}

// Service — сценарий статистики.
type Service struct {
	deps Deps
}

// New создаёт сценарий. Зависимости передаются указателем, как и в остальных
// конструкторах: структура крупная, а копировать её незачем.
func New(deps *Deps) (*Service, error) {
	if deps == nil {
		return nil, errors.New("статистике нужны зависимости")
	}

	switch {
	case deps.Users == nil:
		return nil, errors.New("статистике нужен UserRepo")
	case deps.Courses == nil:
		return nil, errors.New("статистике нужен CourseRepo")
	case deps.Decks == nil:
		return nil, errors.New("статистике нужен DeckRepo")
	case deps.Cards == nil:
		return nil, errors.New("статистике нужен CardRepo")
	case deps.Reviews == nil:
		return nil, errors.New("статистике нужен ReviewRepo")
	case deps.Settings == nil:
		return nil, errors.New("статистике нужен SettingsRepo")
	case deps.Clock == nil:
		return nil, errors.New("статистике нужны часы")
	}
	return &Service{deps: *deps}, nil
}

// Accuracy — доля верных ответов за период.
type Accuracy struct {
	Total   int
	Correct int
}

// Known сообщает, есть ли по чему считать долю. Ноль ответов и ноль
// процентов — разные вещи, и путать их в сводке нельзя.
func (a Accuracy) Known() bool { return a.Total > 0 }

// Share возвращает долю верных от нуля до единицы.
func (a Accuracy) Share() float64 {
	if a.Total == 0 {
		return 0
	}
	return float64(a.Correct) / float64(a.Total)
}

// Summary — сводка по учёбе.
type Summary struct {
	// Learned — слов на повторении с растущим интервалом.
	Learned int
	// Learning — слов на шагах обучения, включая забытые.
	Learning int
	// Known — слов, отмеченных «уже знаю» на знакомстве.
	Known int
	// NewRemaining — слов в колодах, которых человек ещё не начинал.
	NewRemaining int
	// Streak — сколько дней подряд человек занимался, считая сегодня.
	Streak int
	Week   Accuracy
	Month  Accuracy
	// Forecast — сколько карточек подойдёт в каждый из ближайших дней,
	// начиная с сегодняшнего.
	Forecast []int
	// HasCourses — false, если человек ещё ничего не учит: тогда сводка
	// пуста не потому, что он ленив.
	HasCourses bool
}

// Total — сколько слов человек так или иначе прошёл.
func (s *Summary) Total() int { return s.Learned + s.Learning + s.Known }

// Of собирает сводку по всем активным курсам пользователя.
func (s *Service) Of(ctx context.Context, userID user.ID) (Summary, error) {
	courses, err := s.deps.Courses.ByUser(ctx, userID)
	if err != nil {
		return Summary{}, fmt.Errorf("получить курсы: %w", err)
	}

	settings, err := s.deps.Settings.Get(ctx, userID)
	if err != nil {
		return Summary{}, fmt.Errorf("прочитать настройки: %w", err)
	}

	now := s.deps.Clock.Now()
	summary := Summary{Forecast: make([]int, ForecastDays)}
	edges := dayEdges(&settings, now)

	for i := range courses {
		if !courses[i].IsActive() {
			continue
		}
		summary.HasCourses = true

		if err := s.addCourse(ctx, &summary, &courses[i], edges); err != nil {
			return Summary{}, err
		}
	}

	if summary.Week, err = s.accuracy(ctx, userID, now, WeekDays); err != nil {
		return Summary{}, err
	}
	if summary.Month, err = s.accuracy(ctx, userID, now, MonthDays); err != nil {
		return Summary{}, err
	}
	if summary.Streak, err = s.streak(ctx, userID, &settings, now); err != nil {
		return Summary{}, err
	}
	return summary, nil
}

// addCourse добавляет в сводку числа одного курса.
func (s *Service) addCourse(ctx context.Context, summary *Summary, course *study.Course, edges []time.Time) error {
	counts, err := s.deps.Cards.CountsByState(ctx, course.ID)
	if err != nil {
		return fmt.Errorf("посчитать карточки курса %d: %w", course.ID, err)
	}

	deck, err := s.deps.Decks.ByID(ctx, course.DeckID)
	if err != nil {
		return fmt.Errorf("найти колоду курса %d: %w", course.ID, err)
	}

	summary.Learned += counts[study.StateReview]
	summary.Learning += counts[study.StateLearning] + counts[study.StateRelearning]
	summary.Known += counts[study.StateKnown]

	// Осталось то, что человек ещё не начинал. Отложенные слова — карточки
	// в фазе new — сюда входят: они вернутся в знакомство и своего часа ждут.
	started := counts[study.StateLearning] + counts[study.StateReview] +
		counts[study.StateRelearning] + counts[study.StateKnown]
	if remaining := deck.Size - started; remaining > 0 {
		summary.NewRemaining += remaining
	}

	due, err := s.deps.Cards.DueBefore(ctx, course.ID, edges[len(edges)-1])
	if err != nil {
		return fmt.Errorf("получить сроки курса %d: %w", course.ID, err)
	}
	addForecast(summary.Forecast, due, edges)
	return nil
}

// dayEdges возвращает границы суток пользователя: начало сегодняшнего дня
// и следующие ForecastDays границ.
//
// Границы календарные, а не «сегодня плюс сутки»: в день перевода часов
// сутки короче или длиннее, и прогноз, посчитанный сложением часов, съехал
// бы на день.
func dayEdges(settings *user.Settings, now time.Time) []time.Time {
	edges := make([]time.Time, 0, ForecastDays+1)
	edge := settings.DayStart(now)
	edges = append(edges, edge)
	for i := 0; i < ForecastDays; i++ {
		edge = settings.NextDayStart(edge)
		edges = append(edges, edge)
	}
	return edges
}

// addForecast раскладывает сроки по дням.
//
// Всё, что просрочено, попадает в первый день: эти карточки ждут прямо
// сейчас, и показывать их отдельной строкой «в прошлом» незачем.
func addForecast(forecast []int, due, edges []time.Time) {
	for _, at := range due {
		day := 0
		for i := 1; i < len(edges); i++ {
			if !at.Before(edges[i]) {
				day = i
			}
		}
		if day < len(forecast) {
			forecast[day]++
		}
	}
}

// accuracy считает долю верных ответов за последние days суток.
func (s *Service) accuracy(ctx context.Context, userID user.ID, now time.Time, days int) (Accuracy, error) {
	stats, err := s.deps.Reviews.Stats(ctx, port.StatsQuery{
		UserID: userID,
		Since:  now.AddDate(0, 0, -days),
	})
	if err != nil {
		return Accuracy{}, fmt.Errorf("посчитать точность за %d дней: %w", days, err)
	}
	return Accuracy{Total: stats.Total, Correct: stats.Correct}, nil
}

// streak считает, сколько дней подряд человек занимался.
//
// Сегодняшний день серию не обрывает: человек мог ещё не сесть за занятие,
// и говорить ему «серия прервалась» в полдень было бы неправдой. Отсчёт
// поэтому начинается со вчерашнего дня, а сегодняшний добавляется, если
// ответы за него уже есть.
func (s *Service) streak(ctx context.Context, userID user.ID, settings *user.Settings, now time.Time) (int, error) {
	// Месяца с запасом хватает: серия длиннее показывается тем же числом,
	// а тянуть весь журнал ради неё незачем.
	since := now.AddDate(0, 0, -MonthDays)

	days, err := s.deps.Reviews.ActiveDays(ctx, userID, settings.Timezone, since)
	if err != nil {
		return 0, fmt.Errorf("получить дни занятий: %w", err)
	}
	if len(days) == 0 {
		return 0, nil
	}

	today := settings.DayStart(now)
	expected := today
	if !days[0].Equal(today) {
		// Сегодня человек ещё не занимался — серия считается по вчерашний.
		expected = previousDay(settings, today)
		if !days[0].Equal(expected) {
			return 0, nil
		}
	}

	streak := 0
	for _, day := range days {
		if !day.Equal(expected) {
			break
		}
		streak++
		expected = previousDay(settings, expected)
	}
	return streak, nil
}

// previousDay возвращает начало предыдущих суток пользователя.
func previousDay(settings *user.Settings, day time.Time) time.Time {
	return settings.DayStart(day.AddDate(0, 0, -1))
}
