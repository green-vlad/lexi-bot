package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// CourseRepo хранит курсы: какую колоду и с каким переводом учит пользователь.
type CourseRepo struct {
	base
}

// NewCourseRepo создаёт репозиторий курсов.
func NewCourseRepo(pool *pgxpool.Pool) *CourseRepo {
	return &CourseRepo{base: base{pool: pool}}
}

var _ port.CourseRepo = (*CourseRepo)(nil)

const courseColumns = "id, user_id, deck_id, translation_lang, status"

// Ensure создаёт курс или возвращает существующий.
//
// Повторный выбор той же колоды с тем же языком перевода — это возврат
// к своему курсу, а не второй курс с нуля: карточки и прогресс остаются.
// Состояние при этом не трогается, иначе «продолжить» воскрешало бы курс,
// поставленный на паузу.
func (r *CourseRepo) Ensure(ctx context.Context, c study.Course) (study.Course, error) {
	const op = "создать или найти курс"

	if err := c.Validate(); err != nil {
		return study.Course{}, err
	}

	const query = `
		INSERT INTO user_courses (user_id, deck_id, translation_lang, status)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, deck_id, translation_lang) DO UPDATE
		SET status = user_courses.status
		RETURNING ` + courseColumns

	var saved study.Course
	row := r.db(ctx).QueryRow(ctx, query, c.UserID, int64(c.DeckID), c.TranslationLang.String(), c.Status.String())
	if err := scanCourse(row, &saved); err != nil {
		return study.Course{}, wrap(op, err)
	}
	return saved, nil
}

// ByID возвращает курс по идентификатору.
func (r *CourseRepo) ByID(ctx context.Context, id study.CourseID) (study.Course, error) {
	const op = "найти курс"
	const query = "SELECT " + courseColumns + " FROM user_courses WHERE id = $1"

	var course study.Course
	if err := scanCourse(r.db(ctx).QueryRow(ctx, query, int64(id)), &course); err != nil {
		return study.Course{}, wrap(op, err)
	}
	return course, nil
}

// ByUser возвращает все курсы пользователя, включая приостановленные:
// список курсов показывает и их тоже.
func (r *CourseRepo) ByUser(ctx context.Context, userID user.ID) ([]study.Course, error) {
	const op = "получить курсы пользователя"
	const query = "SELECT " + courseColumns + ` FROM user_courses
		WHERE user_id = $1
		ORDER BY created_at, id`

	rows, err := r.db(ctx).Query(ctx, query, int64(userID))
	if err != nil {
		return nil, wrap(op, err)
	}
	defer rows.Close()

	var out []study.Course
	for rows.Next() {
		var course study.Course
		if err := scanCourse(rows, &course); err != nil {
			return nil, wrap(op, err)
		}
		out = append(out, course)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap(op, err)
	}
	return out, nil
}

// SetStatus переключает курс между активным, паузой и архивом.
func (r *CourseRepo) SetStatus(ctx context.Context, id study.CourseID, status study.CourseStatus) error {
	const op = "сменить состояние курса"

	if !status.IsValid() {
		return fmt.Errorf("%s: %w (%q)", op, study.ErrInvalid, status)
	}

	const query = "UPDATE user_courses SET status = $2 WHERE id = $1"
	tag, err := r.db(ctx).Exec(ctx, query, int64(id), status.String())
	return requireRows(op, tag, err)
}

func scanCourse(r row, course *study.Course) error {
	var (
		id     int64
		deckID int64
		lang   string
		status string
	)
	if err := r.Scan(&id, &course.UserID, &deckID, &lang, &status); err != nil {
		return err
	}

	parsedLang, err := lexicon.ParseLanguage(lang)
	if err != nil {
		return fmt.Errorf("код языка %q из базы: %w", lang, err)
	}
	parsedStatus, err := study.ParseCourseStatus(status)
	if err != nil {
		return fmt.Errorf("состояние %q из базы: %w", status, err)
	}

	course.ID = study.CourseID(id)
	course.DeckID = lexicon.DeckID(deckID)
	course.TranslationLang = parsedLang
	course.Status = parsedStatus
	return nil
}

// Проверка на этапе компиляции, что pgx.Row подходит под наш узкий row.
var _ row = pgx.Row(nil)
