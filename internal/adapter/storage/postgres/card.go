package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/usecase/port"
)

// CardRepo хранит карточки — состояние интервального повторения.
type CardRepo struct {
	base
}

// NewCardRepo создаёт репозиторий карточек.
func NewCardRepo(pool *pgxpool.Pool) *CardRepo {
	return &CardRepo{base: base{pool: pool}}
}

var _ port.CardRepo = (*CardRepo)(nil)

const cardColumns = `id, user_course_id, lexeme_id, state, due_at, interval_days,
	ease_factor, repetitions, lapses, learn_step, introduced_at, last_reviewed_at`

// dueCardsQuery вынесен из метода, чтобы тест плана проверял тот же самый
// запрос, а не его копию, которая однажды разойдётся с оригиналом.
//
// Доупорядочивание по id делает выдачу устойчивой: у карточек, введённых
// одной пачкой, due_at совпадает до микросекунды.
const dueCardsQuery = "SELECT " + cardColumns + ` FROM cards
	WHERE user_course_id = $1 AND state <> 'suspended' AND due_at <= $2
	ORDER BY due_at, id
	LIMIT $3`

// Due возвращает карточки, которым подошёл срок, по возрастанию due_at.
//
// Запрос написан под индекс cards_due_idx: те же колонки в том же порядке
// и то же условие на состояние. Отложенные карточки в индекс не входят,
// поэтому и в выдачу не попадают.
func (r *CardRepo) Due(ctx context.Context, q port.DueQuery) ([]study.Card, error) {
	const op = "получить карточки к повторению"

	if q.Limit <= 0 {
		return nil, nil
	}

	rows, err := r.db(ctx).Query(ctx, dueCardsQuery, int64(q.CourseID), q.Now, q.Limit)
	if err != nil {
		return nil, wrap(op, err)
	}
	cards, err := collectCards(rows)
	if err != nil {
		return nil, wrap(op, err)
	}
	return cards, nil
}

// IntroduceNew вводит в курс новые слова и увеличивает дневной счётчик.
//
// Всё происходит в одной транзакции, и начинается она с блокировки строки
// счётчика. Без этого два одновременных нажатия «учить» прочитали бы один
// и тот же остаток лимита и ввели по полному лимиту каждое — пользователь
// получил бы двойную порцию новых слов и вдвое большую нагрузку через день.
func (r *CardRepo) IntroduceNew(ctx context.Context, q port.IntroduceQuery) ([]study.Card, error) {
	const op = "ввести новые слова"

	if q.Limit <= 0 {
		return nil, nil
	}

	var cards []study.Card
	err := r.inTx(ctx, func(tx queryer) error {
		introduced, err := lockCounter(ctx, tx, q.CourseID, q.Day)
		if err != nil {
			return err
		}

		remaining := q.Limit - introduced
		if remaining <= 0 {
			return nil
		}

		// Слова берутся из колоды курса по возрастанию позиции — у встроенных
		// колод это порядок частотности, у личной порядок добавления.
		const insert = `
			WITH candidates AS (
				SELECT di.lexeme_id
				FROM user_courses uc
				JOIN deck_items di ON di.deck_id = uc.deck_id
				LEFT JOIN cards c ON c.user_course_id = uc.id AND c.lexeme_id = di.lexeme_id
				WHERE uc.id = $1 AND c.id IS NULL
				ORDER BY di.position, di.lexeme_id
				LIMIT $2
			)
			INSERT INTO cards (user_course_id, lexeme_id, state, due_at, ease_factor, introduced_at)
			SELECT $1, lexeme_id, 'new', $3, $4, $3
			FROM candidates
			RETURNING ` + cardColumns

		rows, err := tx.Query(ctx, insert, int64(q.CourseID), remaining, q.Now, study.DefaultEaseFactor)
		if err != nil {
			return wrap(op, err)
		}
		cards, err = collectCards(rows)
		if err != nil {
			return wrap(op, err)
		}
		if len(cards) == 0 {
			return nil
		}

		const bump = `
			UPDATE daily_counters
			SET new_introduced = new_introduced + $3
			WHERE user_course_id = $1 AND day = $2`

		if _, err := tx.Exec(ctx, bump, int64(q.CourseID), q.Day, len(cards)); err != nil {
			return wrap("увеличить счётчик новых слов", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return cards, nil
}

// Apply записывает результат ответа: новое состояние карточки, строку
// журнала и дневной счётчик повторений — одной транзакцией.
//
// Порознь это выглядело бы так: карточка уехала на месяц вперёд, а журнал
// об этом не знает, — и статистика начала бы врать при первой же ошибке сети.
func (r *CardRepo) Apply(ctx context.Context, outcome *port.ReviewOutcome) error {
	const op = "записать ответ"

	if err := outcome.State.Validate(); err != nil {
		return err
	}
	if err := outcome.Review.Validate(); err != nil {
		return err
	}
	if outcome.Review.CardID != outcome.CardID {
		return fmt.Errorf("%s: %w (запись журнала о карточке %d)",
			op, port.ErrInvalidData, outcome.Review.CardID)
	}

	return r.inTx(ctx, func(tx queryer) error {
		const update = `
			UPDATE cards
			SET state            = $2,
			    due_at           = $3,
			    interval_days    = $4,
			    ease_factor      = $5,
			    repetitions      = $6,
			    lapses           = $7,
			    learn_step       = $8,
			    last_reviewed_at = $9
			WHERE id = $1`

		state := outcome.State
		tag, err := tx.Exec(ctx, update,
			int64(outcome.CardID), state.State.String(), state.DueAt, state.IntervalDays,
			state.EaseFactor, state.Repetitions, state.Lapses, state.LearnStep,
			outcome.Review.RatedAt)
		if err := requireRows(op, tag, err); err != nil {
			return err
		}

		if err := insertReview(ctx, tx, outcome.UserID, &outcome.Review); err != nil {
			return err
		}
		return bumpReviews(ctx, tx, outcome.CardID, outcome.Day)
	})
}

// ByID возвращает карточку по идентификатору.
func (r *CardRepo) ByID(ctx context.Context, id study.CardID) (study.Card, error) {
	const op = "найти карточку"
	const query = "SELECT " + cardColumns + " FROM cards WHERE id = $1"

	var card study.Card
	if err := scanCard(r.db(ctx).QueryRow(ctx, query, int64(id)), &card); err != nil {
		return study.Card{}, wrap(op, err)
	}
	return card, nil
}

// CountsByState считает карточки курса по фазам. Фазы, в которых карточек
// нет, в ответе отсутствуют — ноль и отсутствие для статистики одно и то же.
func (r *CardRepo) CountsByState(ctx context.Context, courseID study.CourseID) (map[study.State]int, error) {
	const op = "посчитать карточки по фазам"
	const query = "SELECT state, count(*) FROM cards WHERE user_course_id = $1 GROUP BY state"

	rows, err := r.db(ctx).Query(ctx, query, int64(courseID))
	if err != nil {
		return nil, wrap(op, err)
	}
	defer rows.Close()

	out := make(map[study.State]int, len(study.States()))
	for rows.Next() {
		var (
			name  string
			count int
		)
		if err := rows.Scan(&name, &count); err != nil {
			return nil, wrap(op, err)
		}
		state, err := study.ParseState(name)
		if err != nil {
			return nil, wrap(op, err)
		}
		out[state] = count
	}
	if err := rows.Err(); err != nil {
		return nil, wrap(op, err)
	}
	return out, nil
}

// lockCounter берёт строку дневного счётчика под блокировку, создавая её
// при необходимости, и возвращает, сколько новых слов уже введено.
//
// ON CONFLICT DO UPDATE именно здесь не бессмысленное присваивание, а способ
// заблокировать существующую строку: DO NOTHING ничего не вернул бы и ничего
// не заблокировал.
func lockCounter(ctx context.Context, tx queryer, courseID study.CourseID, day time.Time) (int, error) {
	const query = `
		INSERT INTO daily_counters (user_course_id, day)
		VALUES ($1, $2)
		ON CONFLICT (user_course_id, day) DO UPDATE
		SET new_introduced = daily_counters.new_introduced
		RETURNING new_introduced`

	var introduced int
	err := tx.QueryRow(ctx, query, int64(courseID), day).Scan(&introduced)
	if err != nil {
		return 0, wrap("заблокировать дневной счётчик", err)
	}
	return introduced, nil
}

// bumpReviews увеличивает дневной счётчик повторений. Курс берётся из самой
// карточки: сценарий знает карточку, а не курс, и просить у него лишнее
// значило бы дать ему возможность ошибиться.
func bumpReviews(ctx context.Context, tx queryer, cardID study.CardID, day time.Time) error {
	const query = `
		INSERT INTO daily_counters (user_course_id, day, reviews_done)
		SELECT user_course_id, $2, 1 FROM cards WHERE id = $1
		ON CONFLICT (user_course_id, day) DO UPDATE
		SET reviews_done = daily_counters.reviews_done + 1`

	tag, err := tx.Exec(ctx, query, int64(cardID), day)
	return requireRows("увеличить счётчик повторений", tag, err)
}

func scanCard(r row, card *study.Card) error {
	var (
		id         int64
		courseID   int64
		lexemeID   int64
		state      string
		lastReview *time.Time
	)

	err := r.Scan(&id, &courseID, &lexemeID, &state, &card.DueAt, &card.IntervalDays,
		&card.EaseFactor, &card.Repetitions, &card.Lapses, &card.LearnStep,
		&card.IntroducedAt, &lastReview)
	if err != nil {
		return err
	}

	parsed, err := study.ParseState(state)
	if err != nil {
		return fmt.Errorf("фаза %q из базы: %w", state, err)
	}
	card.ID = study.CardID(id)
	card.CourseID = study.CourseID(courseID)
	card.LexemeID = lexicon.LexemeID(lexemeID)
	card.State = parsed
	if lastReview != nil {
		card.LastReviewedAt = *lastReview
	} else {
		card.LastReviewedAt = time.Time{}
	}
	return nil
}

func collectCards(rows pgx.Rows) ([]study.Card, error) {
	defer rows.Close()

	var out []study.Card
	for rows.Next() {
		var card study.Card
		if err := scanCard(rows, &card); err != nil {
			return nil, err
		}
		out = append(out, card)
	}
	return out, rows.Err()
}
