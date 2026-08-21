package study_test

import (
	"errors"
	"testing"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
)

func TestNewCourse(t *testing.T) {
	t.Parallel()

	course, err := study.NewCourse(42, 7, langEN)
	if err != nil {
		t.Fatalf("NewCourse() вернул ошибку: %v", err)
	}
	if course.UserID != 42 || course.DeckID != 7 || course.TranslationLang != langEN {
		t.Errorf("NewCourse() = %+v, поля не сохранены", course)
	}
	if !course.IsActive() {
		t.Error("новый курс должен быть активным")
	}
}

func TestNewCourseErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		userID int64
		deckID lexicon.DeckID
		lang   lexicon.Language
		want   error
	}{
		{"без пользователя", 0, 7, langEN, study.ErrRequired},
		{"отрицательный пользователь", -1, 7, langEN, study.ErrRequired},
		{"без колоды", 42, 0, langEN, study.ErrRequired},
		{"без языка перевода", 42, 7, lexicon.Language{}, study.ErrRequired},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			course, err := study.NewCourse(tt.userID, tt.deckID, tt.lang)
			if !errors.Is(err, tt.want) {
				t.Fatalf("NewCourse() = %v, ожидалась ошибка %v", err, tt.want)
			}
			if course != (study.Course{}) {
				t.Errorf("при ошибке возвращён непустой курс %+v", course)
			}
		})
	}
}

func TestCourseStatusRoundTrip(t *testing.T) {
	t.Parallel()

	for _, status := range study.CourseStatuses() {
		back, err := study.ParseCourseStatus(status.String())
		if err != nil {
			t.Fatalf("ParseCourseStatus(%q) вернул ошибку: %v", status, err)
		}
		if back != status {
			t.Errorf("ParseCourseStatus(%q) = %q", status, back)
		}
	}

	if got, err := study.ParseCourseStatus("  PAUSED "); err != nil || got != study.CoursePaused {
		t.Errorf("ParseCourseStatus() = %q, %v; ожидалось paused", got, err)
	}
	if _, err := study.ParseCourseStatus(""); !errors.Is(err, study.ErrRequired) {
		t.Error("пустая строка должна давать ErrRequired")
	}
	if _, err := study.ParseCourseStatus("deleted"); !errors.Is(err, study.ErrInvalid) {
		t.Error("неизвестный статус должен давать ErrInvalid")
	}
}

func TestCourseOnlyActiveIsActive(t *testing.T) {
	t.Parallel()

	course, err := study.NewCourse(42, 7, langEN)
	if err != nil {
		t.Fatalf("NewCourse() вернул ошибку: %v", err)
	}

	// Приостановленный курс не выдаёт карточки и не шлёт напоминаний —
	// на этом стоит /pause.
	for _, status := range []study.CourseStatus{study.CoursePaused, study.CourseArchived} {
		course.Status = status
		if course.IsActive() {
			t.Errorf("курс в состоянии %q не должен считаться активным", status)
		}
		if err := course.Validate(); err != nil {
			t.Errorf("состояние %q должно быть допустимым: %v", status, err)
		}
	}

	course.Status = study.CourseStatus("deleted")
	if !errors.Is(course.Validate(), study.ErrInvalid) {
		t.Error("Validate() пропустил неизвестный статус")
	}
}
