package port

import (
	"context"
	"time"

	"lexi-bot/internal/domain/lexicon"
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

// NewWordQuery — запрос очереди знакомства.
type NewWordQuery struct {
	CourseID study.CourseID
	// Now — момент, на который отбираются слова: отложенные возвращаются,
	// когда их due_at уже прошёл.
	Now time.Time
	// Limit — сколько слов взять. Меню спрашивает столько, сколько человеку
	// осталось начать сегодня, экран знакомства — одно.
	Limit int
}

// StartLearningQuery — «запомнил»: слово уходит на шаги обучения.
type StartLearningQuery struct {
	CourseID study.CourseID
	LexemeID lexicon.LexemeID
	// State — первое состояние карточки, посчитанное планировщиком.
	State study.CardState
	// Now — момент знакомства: он же introduced_at.
	Now time.Time
	// Day — сутки пользователя для дневного счётчика новых слов.
	Day time.Time
	// Limit — дневной лимит новых слов целиком, а не его остаток.
	// Остаток репозиторий считает сам, под блокировкой строки счётчика.
	Limit int
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
	// ExpectedLastReviewedAt — версия карточки, на которую отвечает
	// пользователь: момент её прошлого ответа, каким он был, когда карточку
	// показывали. Если в базе он уже другой, значит на эту же карточку
	// успели ответить, и второй ответ применять нельзя — обычно это двойное
	// нажатие кнопки. Нулевое значение означает «карточку ещё не отвечали».
	ExpectedLastReviewedAt time.Time
}

// CardRepo хранит карточки — состояние интервального повторения.
type CardRepo interface {
	// Due возвращает карточки, которым подошёл срок, по возрастанию due_at.
	Due(ctx context.Context, q DueQuery) ([]study.Card, error)

	// CountDue считает карточки, которым подошёл срок. Нужен меню занятия:
	// оно обещает число, а тянуть ради него сами карточки незачем.
	CountDue(ctx context.Context, courseID study.CourseID, now time.Time) (int, error)

	// NewWords возвращает слова колоды, которые пора показать в знакомстве,
	// по возрастанию позиции в колоде.
	//
	// Это слова, для которых карточки ещё нет, и слова, отложенные кнопкой
	// «пропустить», у которых срок возврата уже прошёл.
	NewWords(ctx context.Context, q NewWordQuery) ([]lexicon.LexemeID, error)

	// StartLearning заводит карточку на шагах обучения и увеличивает
	// дневной счётчик новых слов — в одной транзакции, под блокировкой
	// строки счётчика. Иначе два одновременных «запомнил» прочитали бы
	// один и тот же остаток лимита и пробили бы его вдвоём.
	//
	// Второе значение — false, если лимит уже выбран: карточка не заведена,
	// и человеку нужно сказать, что на сегодня хватит.
	//
	// Запрос передаётся указателем не для того, чтобы его меняли —
	// реализация его не трогает, — а чтобы не копировать состояние
	// карточки целиком на каждое нажатие.
	StartLearning(ctx context.Context, q *StartLearningQuery) (study.Card, bool, error)

	// MarkKnown помечает слово выученным: оно не предлагается в знакомстве
	// и не попадает в повторения. Дневной лимит новых слов не тратится —
	// человек ничего не начинал учить.
	MarkKnown(ctx context.Context, courseID study.CourseID, lexemeID lexicon.LexemeID, now time.Time) error

	// PostponeNew убирает слово из знакомства до момента until — обычно
	// это начало следующих суток пользователя.
	PostponeNew(ctx context.Context, courseID study.CourseID, lexemeID lexicon.LexemeID, now, until time.Time) error

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

	// NextDue возвращает ближайший срок повторения в курсе. Второе значение —
	// false, если повторять больше нечего: у сводки в конце занятия это
	// разные сообщения.
	NextDue(ctx context.Context, courseID study.CourseID) (time.Time, bool, error)
}

// StatsQuery — за какие ответы считать сводку.
type StatsQuery struct {
	UserID user.ID
	// CourseID сужает подсчёт до одного курса. Ноль означает «по всем»:
	// сводка занятия говорит про курс, а статистика в /stats — про человека.
	CourseID study.CourseID
	Since    time.Time
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

	// Stats считает ответы за период.
	Stats(ctx context.Context, q StatsQuery) (ReviewStats, error)

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
	// NewLimit — норма новых слов, зафиксированная на эти сутки в момент,
	// когда человек впервые в этот день начал учить слово. Ноль означает
	// «не зафиксирована»: норму берут из настроек.
	//
	// Нужна затем, чтобы правка нормы не действовала задним числом:
	// поднявший её в середине дня не получает лишних слов сегодня,
	// опустивший — не слышит «на сегодня всё» о том, что уже сделал.
	NewLimit int
}

// LimitFor возвращает дневную норму новых слов: зафиксированную на эти
// сутки, а при её отсутствии — текущую из настроек.
func (c DailyCounter) LimitFor(configured int) int {
	if c.NewLimit > 0 {
		return c.NewLimit
	}
	return configured
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
