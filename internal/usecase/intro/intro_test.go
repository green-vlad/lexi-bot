package intro_test

import (
	"context"
	"testing"
	"time"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/intro"
	"lexi-bot/internal/usecase/port"
)

const courseID = study.CourseID(1)

var (
	langKO = lexicon.MustParseLanguage("ko")
	langRU = lexicon.MustParseLanguage("ru")
)

type fixture struct {
	service  *intro.Service
	cards    *fakeCards
	counters *fakeCounters
	courses  *fakeCourses
	settings *fakeSettings
	now      time.Time
}

func newFixture(t *testing.T, words int) *fixture {
	t.Helper()

	f := &fixture{
		counters: &fakeCounters{byDay: map[string]port.DailyCounter{}},
		// Полдень в Сеуле, чтобы «завтра» не совпало с «через сутки по UTC».
		now: time.Date(2026, 8, 24, 3, 0, 0, 0, time.UTC),
	}

	lexemes := map[lexicon.LexemeID]lexicon.Lexeme{}
	translations := map[lexicon.LexemeID][]lexicon.Translation{}
	pool := make([]lexicon.LexemeID, 0, words)
	terms := []string{"집", "개", "물", "불"}
	meanings := []string{"дом", "собака", "вода", "огонь"}
	for i := 0; i < words; i++ {
		id := lexicon.LexemeID(i + 1)
		pool = append(pool, id)
		lexemes[id] = lexicon.Lexeme{
			ID: id, Lang: langKO, Term: terms[i], Reading: "чтение",
			Example: "пример", POS: lexicon.POSNoun,
		}
		translations[id] = []lexicon.Translation{{LexemeID: id, Lang: langRU, Text: meanings[i], IsPrimary: true}}
	}

	f.cards = &fakeCards{pool: pool, counters: f.counters}
	f.courses = &fakeCourses{course: study.Course{
		ID: courseID, UserID: 42, DeckID: 7, TranslationLang: langRU, Status: study.CourseActive,
	}}
	f.settings = &fakeSettings{settings: user.DefaultSettings(user.MustParseTimezone("Asia/Seoul"))}

	scheduler, err := study.NewSM2(study.DefaultSM2Config(), nil)
	if err != nil {
		t.Fatalf("NewSM2() вернул ошибку: %v", err)
	}

	service, err := intro.New(&intro.Deps{
		Cards:     f.cards,
		Counters:  f.counters,
		Courses:   f.courses,
		Settings:  f.settings,
		Lexemes:   &fakeLexemes{lexemes: lexemes, translations: translations},
		Clock:     port.ClockFunc(func() time.Time { return f.now }),
		Scheduler: scheduler,
	})
	if err != nil {
		t.Fatalf("intro.New() вернул ошибку: %v", err)
	}
	f.service = service
	return f
}

func (f *fixture) next(t *testing.T) (intro.Word, intro.Reason) {
	t.Helper()

	word, reason, err := f.service.Next(context.Background(), courseID)
	if err != nil {
		t.Fatalf("Next() вернул ошибку: %v", err)
	}
	return word, reason
}

func TestNextShowsWordWithEverythingToRead(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 4)

	word, reason := f.next(t)
	if reason != intro.ReasonNone {
		t.Fatalf("причина = %v, ожидалось слово", reason)
	}
	// Знакомство не проверяет, а показывает: человек должен увидеть слово
	// целиком, иначе решать ему не о чем.
	if word.Lexeme.Term != "집" {
		t.Errorf("слово = %q, ожидалось первое в колоде", word.Lexeme.Term)
	}
	if word.Lexeme.Example == "" {
		t.Error("пример употребления не дошёл до карточки")
	}
	if len(word.Translations) != 1 || word.Translations[0].Text != "дом" {
		t.Errorf("переводы = %v", word.Translations)
	}
}

func TestRememberStartsLearning(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 4)

	word, _ := f.next(t)
	accepted, err := f.service.Remember(context.Background(), courseID, word.Lexeme.ID)
	if err != nil {
		t.Fatalf("Remember() вернул ошибку: %v", err)
	}
	if !accepted {
		t.Fatal("норма не выбрана, слово должно было уйти в обучение")
	}

	card, ok := f.cards.byLexeme(word.Lexeme.ID)
	if !ok {
		t.Fatal("карточка не заведена")
	}
	if card.State != study.StateLearning {
		t.Errorf("фаза = %v, ожидалось обучение", card.State)
	}
	// Срок ставит планировщик: знакомство не выдумывает интервалы само.
	if !card.DueAt.After(f.now) {
		t.Errorf("срок = %v, ожидался момент после знакомства", card.DueAt)
	}

	// Место в дневной норме потрачено.
	counter, _ := f.counters.Get(context.Background(), courseID, f.settings.settings.DayStart(f.now))
	if counter.NewIntroduced != 1 {
		t.Errorf("счётчик новых = %d, ожидалась единица", counter.NewIntroduced)
	}

	// И следующим показывается уже другое слово.
	next, reason := f.next(t)
	if reason != intro.ReasonNone || next.Lexeme.ID == word.Lexeme.ID {
		t.Errorf("следующее слово = %v (%v), ожидалось новое", next.Lexeme.Term, reason)
	}
}

func TestAlreadyKnowKeepsWordOutOfEverything(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 4)

	word, _ := f.next(t)
	if err := f.service.AlreadyKnow(context.Background(), courseID, word.Lexeme.ID); err != nil {
		t.Fatalf("AlreadyKnow() вернул ошибку: %v", err)
	}

	card, ok := f.cards.byLexeme(word.Lexeme.ID)
	if !ok {
		t.Fatal("карточка не заведена")
	}
	if card.State != study.StateKnown {
		t.Errorf("фаза = %v, ожидалось «уже знаю»", card.State)
	}
	// В повторения слово не попадёт: повторять в нём нечего.
	if card.IsDue(f.now.AddDate(0, 0, 30)) {
		t.Error("слово, которое человек знает, вернулось повторением")
	}

	// Дневная норма не тронута: человек ничего не начинал учить.
	counter, _ := f.counters.Get(context.Background(), courseID, f.settings.settings.DayStart(f.now))
	if counter.NewIntroduced != 0 {
		t.Errorf("счётчик новых = %d, ожидался ноль", counter.NewIntroduced)
	}

	if next, _ := f.next(t); next.Lexeme.ID == word.Lexeme.ID {
		t.Error("слово показано снова")
	}
}

func TestSkipReturnsWordTomorrow(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 4)

	word, _ := f.next(t)
	if err := f.service.Skip(context.Background(), courseID, word.Lexeme.ID); err != nil {
		t.Fatalf("Skip() вернул ошибку: %v", err)
	}

	// Сегодня его заменило следующее слово.
	next, reason := f.next(t)
	if reason != intro.ReasonNone {
		t.Fatalf("причина = %v, ожидалось следующее слово", reason)
	}
	if next.Lexeme.ID == word.Lexeme.ID {
		t.Error("пропущенное слово показано снова в том же занятии")
	}

	// Дневная норма не тронута: пропуск ничего не начинает.
	counter, _ := f.counters.Get(context.Background(), courseID, f.settings.settings.DayStart(f.now))
	if counter.NewIntroduced != 0 {
		t.Errorf("счётчик новых = %d, ожидался ноль", counter.NewIntroduced)
	}

	// А завтра оно вернётся: пропуск означает «не сейчас», а не «никогда».
	f.now = f.settings.settings.NextDayStart(f.now).Add(time.Hour)
	if back, _ := f.next(t); back.Lexeme.ID != word.Lexeme.ID {
		t.Errorf("завтра показано %v, ожидалось пропущенное слово", back.Lexeme.Term)
	}
}

func TestNextStopsOnDailyQuota(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 4)
	f.settings.settings.NewPerDay = 2

	for i := 0; i < 2; i++ {
		word, reason := f.next(t)
		if reason != intro.ReasonNone {
			t.Fatalf("слово %d: причина = %v", i+1, reason)
		}
		if accepted, err := f.service.Remember(context.Background(), courseID, word.Lexeme.ID); err != nil || !accepted {
			t.Fatalf("Remember() = %v, %v", accepted, err)
		}
	}

	// Норма выбрана: слова в колоде ещё есть, но на сегодня довольно.
	if _, reason := f.next(t); reason != intro.ReasonDailyLimit {
		t.Errorf("причина = %v, ожидалась дневная норма", reason)
	}

	// И «запомнил» на слове, показанном до исчерпания нормы, тоже
	// не проходит: между показом и нажатием норма могла кончиться.
	accepted, err := f.service.Remember(context.Background(), courseID, 4)
	if err != nil {
		t.Fatalf("Remember() вернул ошибку: %v", err)
	}
	if accepted {
		t.Error("норма выбрана, а слово всё равно ушло в обучение")
	}
}

func TestNextWhenDeckIsDone(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 1)

	word, _ := f.next(t)
	if _, err := f.service.Remember(context.Background(), courseID, word.Lexeme.ID); err != nil {
		t.Fatalf("Remember() вернул ошибку: %v", err)
	}

	// Норма не выбрана, но новых слов в колоде не осталось — это другая
	// причина остановки, и сказать о ней надо иначе.
	if _, reason := f.next(t); reason != intro.ReasonDeckDone {
		t.Errorf("причина = %v, ожидался конец колоды", reason)
	}
}

func TestNextSkipsPausedCourse(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 4)
	f.courses.course.Status = study.CoursePaused

	if _, reason := f.next(t); reason != intro.ReasonPaused {
		t.Errorf("причина = %v, ожидалась пауза", reason)
	}
	if available, err := f.service.Available(context.Background(), courseID); err != nil || available != 0 {
		t.Errorf("Available() = %d, %v: у курса на паузе слов нет", available, err)
	}
}

func TestAvailableMatchesWhatPersonWillSee(t *testing.T) {
	t.Parallel()

	f := newFixture(t, 4)
	f.settings.settings.NewPerDay = 3

	// Меньшее из двух: остатка нормы и остатка колоды.
	if got := f.available(t); got != 3 {
		t.Errorf("Available() = %d, ожидался остаток нормы", got)
	}

	f.settings.settings.NewPerDay = 10
	if got := f.available(t); got != 4 {
		t.Errorf("Available() = %d, ожидался остаток колоды", got)
	}

	word, _ := f.next(t)
	if _, err := f.service.Remember(context.Background(), courseID, word.Lexeme.ID); err != nil {
		t.Fatalf("Remember() вернул ошибку: %v", err)
	}
	if got := f.available(t); got != 3 {
		t.Errorf("Available() = %d: одно слово уже начато", got)
	}
}

func (f *fixture) available(t *testing.T) int {
	t.Helper()

	available, err := f.service.Available(context.Background(), courseID)
	if err != nil {
		t.Fatalf("Available() вернул ошибку: %v", err)
	}
	return available
}

func TestNewNeedsDependencies(t *testing.T) {
	t.Parallel()

	if _, err := intro.New(nil); err == nil {
		t.Error("сценарий без зависимостей должен быть ошибкой")
	}
	if _, err := intro.New(&intro.Deps{}); err == nil {
		t.Error("сценарий с пустыми зависимостями должен быть ошибкой")
	}
}
