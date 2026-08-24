package session

import (
	"context"
	"fmt"
	"time"

	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// Summary — итог занятия.
//
// Всё в ней про сегодня, а не про прошедшее занятие: занятий за день может
// быть несколько, и человеку важнее «сколько сделано сегодня», чем «сколько
// за последние двадцать минут». К тому же «занятие» нигде не начинается
// и не кончается — есть только карточки и дневные лимиты.
type Summary struct {
	// Reviewed — сколько карточек отвечено за сегодня.
	Reviewed int
	// New — сколько из них новых слов.
	New int
	// Answered — сколько ответов нашлось в журнале за сегодня. Точность
	// считается по ним, и при нуле её показывать нельзя: «0% верных»
	// рядом с «повторено 3 карточки» — не статистика, а недоразумение.
	Answered int
	// Accuracy — доля верных ответов от нуля до единицы.
	Accuracy float64
	// NextReview — когда ждать следующую карточку. Есть не всегда:
	// у только что начатого курса повторять ещё нечего.
	NextReview time.Time
	HasNext    bool
}

// Summary собирает итог занятия по курсу.
func (s *Service) Summary(ctx context.Context, courseID study.CourseID) (Summary, error) {
	course, err := s.deps.Courses.ByID(ctx, courseID)
	if err != nil {
		return Summary{}, fmt.Errorf("найти курс: %w", err)
	}

	settings, err := s.deps.Settings.Get(ctx, user.ID(course.UserID))
	if err != nil {
		return Summary{}, fmt.Errorf("прочитать настройки: %w", err)
	}

	now := s.deps.Clock.Now()
	day := settings.DayStart(now)

	counter, err := s.deps.Counters.Get(ctx, courseID, day)
	if err != nil {
		return Summary{}, fmt.Errorf("прочитать дневные счётчики: %w", err)
	}

	summary := Summary{Reviewed: counter.ReviewsDone, New: counter.NewIntroduced}

	if s.deps.Reviews != nil {
		// Точность считается по журналу, а не по счётчикам: счётчик знает
		// только сколько ответов было, но не какими они были.
		stats, err := s.deps.Reviews.Stats(ctx, port.StatsQuery{
			UserID:   user.ID(course.UserID),
			CourseID: courseID,
			Since:    day,
		})
		if err != nil {
			return Summary{}, fmt.Errorf("посчитать точность: %w", err)
		}
		summary.Answered = stats.Total
		summary.Accuracy = stats.Accuracy()
	}

	next, ok, err := s.deps.Cards.NextDue(ctx, courseID)
	if err != nil {
		return Summary{}, fmt.Errorf("найти ближайшее повторение: %w", err)
	}
	summary.NextReview = next
	summary.HasNext = ok

	return summary, nil
}
