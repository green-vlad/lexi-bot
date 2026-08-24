package port

import (
	"context"
	"time"

	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
)

// CourseRepo хранит курсы: какую колоду и с каким переводом учит пользователь.
type CourseRepo interface {
	// Ensure создаёт курс или возвращает существующий: одну и ту же колоду
	// с тем же языком перевода дважды не заводят.
	Ensure(ctx context.Context, c study.Course) (study.Course, error)

	// ByID возвращает курс по идентификатору.
	ByID(ctx context.Context, id study.CourseID) (study.Course, error)

	// ByUser возвращает все курсы пользователя, включая приостановленные:
	// список курсов показывает и их тоже.
	ByUser(ctx context.Context, userID user.ID) ([]study.Course, error)

	// SetStatus переключает курс между активным, паузой и архивом.
	SetStatus(ctx context.Context, id study.CourseID, status study.CourseStatus) error
}

// DueQuery — запрос очереди повторений.
type DueQuery struct {
	CourseID study.CourseID
	// Now — момент, на который отбираются карточки: due_at <= Now.
	Now time.Time
	// Limit — сколько карточек взять, с учётом дневного лимита повторений.
	Limit int
}

// IntroduceQuery — запрос на ввод новых слов в курс.
type IntroduceQuery struct {
	CourseID study.CourseID
	// Now — момент введения: с него карточка становится доступной.
	Now time.Time
	// Day — календарные сутки пользователя, к которым относится дневной
	// лимит. Именно сутки пользователя, а не UTC.
	Day time.Time
	// Limit — дневной лимит новых слов целиком, а не его остаток.
	//
	// Остаток репозиторий считает сам, под блокировкой строки счётчика:
	// иначе два одновременных нажатия «учить» прочитали бы один и тот же
	// остаток и ввели по полному лимиту каждое.
	Limit int
	// Batch — сколько слов ввести за один раз. Ноль означает «весь остаток».
	//
	// Учебная сессия вводит слова по одному, ровно когда собирается их
	// показать: иначе человек, бросивший занятие после первой карточки,
	// потратил бы весь дневной лимит на слова, которых не видел.
	Batch int
}

// ReviewOutcome — результат ответа: всё, что нужно записать одной транзакцией.
type ReviewOutcome struct {
	CardID study.CardID
	// State — новое состояние карточки, посчитанное планировщиком.
	State study.CardState
	// Review — запись в журнал повторений.
	Review study.Review
	// UserID заполняет денормализованную колонку reviews.user_id,
	// на которой стоит вся статистика.
	UserID user.ID
	// Day — сутки пользователя для дневного счётчика повторений.
	Day time.Time
}

// CardRepo хранит карточки — состояние интервального повторения.
type CardRepo interface {
	// Due возвращает карточки, которым подошёл срок, по возрастанию due_at.
	Due(ctx context.Context, q DueQuery) ([]study.Card, error)

	// IntroduceNew берёт из колоды слова, для которых карточек ещё нет,
	// заводит их и увеличивает дневной счётчик новых — всё в одной
	// транзакции. Иначе два одновременных нажатия «учить» пробили бы
	// дневной лимит.
	IntroduceNew(ctx context.Context, q IntroduceQuery) ([]study.Card, error)

	// Apply записывает результат ответа: новое состояние карточки, строку
	// журнала и дневной счётчик — одной транзакцией.
	//
	// Указатель здесь не приглашение менять структуру — реализация её
	// не трогает, — а способ не копировать четверть килобайта на каждый
	// ответ пользователя.
	Apply(ctx context.Context, outcome *ReviewOutcome) error

	// ByID возвращает карточку по идентификатору.
	ByID(ctx context.Context, id study.CardID) (study.Card, error)

	// CountsByState считает карточки курса по фазам — это основа статистики
	// «выучено / на повторении / новых осталось».
	CountsByState(ctx context.Context, courseID study.CourseID) (map[study.State]int, error)
}

// ReviewStats — сводка по журналу за период.
type ReviewStats struct {
	Total   int
	Correct int
}

// Accuracy — доля верных ответов от нуля до единицы. У пустой сводки ноль:
// показывать «100% точности» тому, кто не ответил ни разу, было бы обманом.
func (s ReviewStats) Accuracy() float64 {
	if s.Total == 0 {
		return 0
	}
	return float64(s.Correct) / float64(s.Total)
}

// ReviewRepo пишет журнал повторений и считает по нему сводки.
//
// Журнал только пополняется: правок и удалений в нём нет, и методов для них
// здесь тоже нет.
type ReviewRepo interface {
	// Add добавляет запись. Обычный путь ответа идёт через CardRepo.Apply,
	// где журнал пишется вместе с карточкой; этот метод — для случаев,
	// когда состояние карточки не меняется.
	//
	// Указатель — чтобы не копировать запись журнала на каждый ответ;
	// реализация её не меняет.
	Add(ctx context.Context, userID user.ID, review *study.Review) error

	// Stats считает ответы пользователя начиная с момента since.
	Stats(ctx context.Context, userID user.ID, since time.Time) (ReviewStats, error)

	// ActiveDays возвращает календарные дни, в которые пользователь отвечал,
	// от свежего к старому. Дни считаются в его таймзоне: серия занятий
	// не должна прерываться из-за того, что вечерний ответ пришёлся
	// на следующие сутки UTC.
	ActiveDays(ctx context.Context, userID user.ID, tz user.Timezone, since time.Time) ([]time.Time, error)
}

// DailyCounter — дневные счётчики курса.
type DailyCounter struct {
	// Day — календарная дата в таймзоне пользователя.
	Day           time.Time
	NewIntroduced int
	ReviewsDone   int
}

// CounterRepo хранит дневные счётчики.
//
// Считать их запросом к cards и reviews теоретически можно, но отдельная
// строка превращает проверку дневного лимита в одно дешёвое чтение и, что
// важнее, не врёт при смене таймзоны: прошлые дни остаются такими, какими
// они были прожиты.
type CounterRepo interface {
	// Get возвращает счётчики за сутки; их отсутствие — это нули, а не ошибка.
	Get(ctx context.Context, courseID study.CourseID, day time.Time) (DailyCounter, error)

	// AddReview увеличивает счётчик повторений за сутки.
	AddReview(ctx context.Context, courseID study.CourseID, day time.Time) error
}
