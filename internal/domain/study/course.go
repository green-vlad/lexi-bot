package study

import (
	"fmt"
	"strings"

	"lexi-bot/internal/domain/lexicon"
)

// CourseStatus — состояние курса.
type CourseStatus string

// Состояния курса.
const (
	// CourseActive — курс идёт: карточки выдаются, напоминания приходят.
	CourseActive CourseStatus = "active"
	// CoursePaused — пауза: ни карточек, ни напоминаний, прогресс сохраняется.
	CoursePaused CourseStatus = "paused"
	// CourseArchived — курс убран из списка, но его карточки и журнал целы.
	CourseArchived CourseStatus = "archived"
)

var courseStatuses = []CourseStatus{CourseActive, CoursePaused, CourseArchived}

// CourseStatuses возвращает все состояния курса.
func CourseStatuses() []CourseStatus {
	out := make([]CourseStatus, len(courseStatuses))
	copy(out, courseStatuses)
	return out
}

// ParseCourseStatus разбирает состояние курса из строки.
func ParseCourseStatus(s string) (CourseStatus, error) {
	status := CourseStatus(strings.ToLower(strings.TrimSpace(s)))
	if status == "" {
		return "", fmt.Errorf("status: %w", ErrRequired)
	}
	if !status.IsValid() {
		return "", fmt.Errorf("status %q: %w", s, ErrInvalid)
	}
	return status, nil
}

// String возвращает код состояния.
func (s CourseStatus) String() string { return string(s) }

// IsValid сообщает, что состояние входит в набор допустимых.
func (s CourseStatus) IsValid() bool {
	for _, known := range courseStatuses {
		if s == known {
			return true
		}
	}
	return false
}

// Course — «пользователь учит колоду X с переводом на язык Y».
//
// Курс, а не колода, — владелец карточек: один и тот же корейский список
// у двух пользователей даёт разные карточки, а один пользователь может учить
// его и с переводом на русский, и с переводом на английский.
//
// UserID здесь обычное число, а не user.ID, по той же причине, что и владелец
// у лексемы: domain/user уже зависит от этого пакета (настройки хранят режимы
// проверки), и обратная связь замкнула бы импорты в кольцо.
type Course struct {
	ID     CourseID
	UserID int64
	DeckID lexicon.DeckID
	// TranslationLang — язык, на который переводятся слова колоды.
	TranslationLang lexicon.Language
	Status          CourseStatus
}

// NewCourse создаёт активный курс.
func NewCourse(userID int64, deckID lexicon.DeckID, translationLang lexicon.Language) (Course, error) {
	course := Course{
		UserID:          userID,
		DeckID:          deckID,
		TranslationLang: translationLang,
		Status:          CourseActive,
	}
	if err := course.Validate(); err != nil {
		return Course{}, err
	}
	return course, nil
}

// Validate проверяет инварианты курса.
func (c Course) Validate() error {
	if c.UserID <= 0 {
		return fmt.Errorf("user_id: %w", ErrRequired)
	}
	if c.DeckID <= 0 {
		return fmt.Errorf("deck_id: %w", ErrRequired)
	}
	if c.TranslationLang.IsZero() {
		return fmt.Errorf("translation_lang: %w", ErrRequired)
	}
	if !c.Status.IsValid() {
		return fmt.Errorf("status %q: %w", c.Status, ErrInvalid)
	}
	return nil
}

// IsActive сообщает, что курс выдаёт карточки и шлёт напоминания.
func (c Course) IsActive() bool { return c.Status == CourseActive }
