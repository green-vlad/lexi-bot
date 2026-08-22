// Package onboarding проводит нового пользователя от первого сообщения
// до готового курса.
//
// Путь намеренно короткий: язык изучения, колода, язык перевода — и всё,
// можно учить. Остальное (сколько слов в день, таймзона, напоминания)
// получает разумные значения по умолчанию и настраивается потом через
// /settings. План (§6) предлагал спрашивать всё сразу, но семь экранов
// до первого выученного слова — это семь возможностей передумать.
//
// Сценарий ничего не знает про Telegram: он отвечает на вопросы «что можно
// выбрать» и «вот выбор, заведи курс». Как это показать кнопками — дело
// адаптера.
package onboarding

import (
	"context"
	"errors"
	"fmt"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// Deps — зависимости сценария.
type Deps struct {
	Users    port.UserRepo
	Settings port.SettingsRepo
	Decks    port.DeckRepo
	Courses  port.CourseRepo
	// DefaultTimezone достаётся пользователю, которого мы не спрашивали
	// про таймзону. Ошибиться здесь не страшно: дневная граница сдвинется
	// на несколько часов, а поправить её можно в настройках.
	DefaultTimezone user.Timezone
}

// Service — сценарий онбординга.
type Service struct {
	deps Deps
}

// New создаёт сценарий.
func New(deps Deps) (*Service, error) {
	switch {
	case deps.Users == nil:
		return nil, errors.New("онбордингу нужен UserRepo")
	case deps.Settings == nil:
		return nil, errors.New("онбордингу нужен SettingsRepo")
	case deps.Decks == nil:
		return nil, errors.New("онбордингу нужен DeckRepo")
	case deps.Courses == nil:
		return nil, errors.New("онбордингу нужен CourseRepo")
	}

	if deps.DefaultTimezone.IsZero() {
		deps.DefaultTimezone = user.UTCTimezone()
	}
	return &Service{deps: deps}, nil
}

// LearningLanguages — что можно учить: языки, для которых есть непустые
// встроенные колоды.
func (s *Service) LearningLanguages(ctx context.Context) ([]lexicon.Language, error) {
	langs, err := s.deps.Decks.Languages(ctx)
	if err != nil {
		return nil, fmt.Errorf("получить языки изучения: %w", err)
	}
	if len(langs) == 0 {
		// Словари не загружены (T-036, T-037). Пользователю нечего
		// предложить, и делать вид, что всё в порядке, нельзя.
		return nil, ErrNothingToLearn
	}
	return langs, nil
}

// Decks — какие колоды есть для выбранного языка.
func (s *Service) Decks(ctx context.Context, lang lexicon.Language) ([]lexicon.Deck, error) {
	if lang.IsZero() {
		return nil, fmt.Errorf("выбор колоды: %w", ErrNoLanguage)
	}

	decks, err := s.deps.Decks.Builtin(ctx, lang)
	if err != nil {
		return nil, fmt.Errorf("получить колоды языка %s: %w", lang, err)
	}
	if len(decks) == 0 {
		return nil, ErrNothingToLearn
	}
	return decks, nil
}

// TranslationLanguages — на какие языки переведены слова колоды.
func (s *Service) TranslationLanguages(ctx context.Context, deckID lexicon.DeckID) ([]lexicon.Language, error) {
	langs, err := s.deps.Decks.TranslationLanguages(ctx, deckID)
	if err != nil {
		return nil, fmt.Errorf("получить языки перевода: %w", err)
	}
	if len(langs) == 0 {
		return nil, ErrNoTranslations
	}
	return langs, nil
}

// SetUILang меняет язык интерфейса. Спрашивают его только у тех, чей клиент
// Telegram говорит на языке, которого мы не знаем: остальным он уже подобран.
func (s *Service) SetUILang(ctx context.Context, userID user.ID, lang user.UILang) error {
	if !lang.IsSupported() {
		return fmt.Errorf("язык интерфейса %q: %w", lang, ErrUnsupportedLanguage)
	}
	if err := s.deps.Users.SetUILang(ctx, userID, lang); err != nil {
		return fmt.Errorf("сменить язык интерфейса: %w", err)
	}
	return nil
}

// Choice — то, что пользователь выбрал.
type Choice struct {
	UserID          user.ID
	DeckID          lexicon.DeckID
	TranslationLang lexicon.Language
}

// Result — чем закончился онбординг.
type Result struct {
	Course study.Course
	Deck   lexicon.Deck
	// SettingsCreated сообщает, что настройки завели только что. У человека,
	// пришедшего за вторым курсом, они уже есть и не трогаются.
	SettingsCreated bool
	// CourseCreated отличает новый курс от возврата к уже начатому.
	CourseCreated bool
}

// Complete заводит курс по сделанному выбору.
//
// Повторный проход не сбрасывает прогресс: настройки, если они есть,
// остаются как были, а курс с той же колодой и тем же языком перевода —
// это тот же курс со всеми его карточками, а не второй с нуля.
func (s *Service) Complete(ctx context.Context, choice Choice) (Result, error) {
	if choice.UserID <= 0 {
		return Result{}, ErrNoUser
	}

	deck, err := s.deps.Decks.ByID(ctx, choice.DeckID)
	if err != nil {
		return Result{}, fmt.Errorf("найти колоду: %w", err)
	}
	if err := s.checkTranslation(ctx, &deck, choice.TranslationLang); err != nil {
		return Result{}, err
	}

	settingsCreated, err := s.ensureSettings(ctx, choice.UserID)
	if err != nil {
		return Result{}, err
	}

	wanted, err := study.NewCourse(int64(choice.UserID), deck.ID, choice.TranslationLang)
	if err != nil {
		return Result{}, err
	}

	existing, err := s.deps.Courses.ByUser(ctx, choice.UserID)
	if err != nil {
		return Result{}, fmt.Errorf("получить курсы пользователя: %w", err)
	}

	course, err := s.deps.Courses.Ensure(ctx, wanted)
	if err != nil {
		return Result{}, fmt.Errorf("завести курс: %w", err)
	}

	return Result{
		Course:          course,
		Deck:            deck,
		SettingsCreated: settingsCreated,
		CourseCreated:   !contains(existing, course.ID),
	}, nil
}

// CourseSummary — курс вместе с колодой: этого хватает, чтобы напомнить
// вернувшемуся, что он учит.
type CourseSummary struct {
	Course study.Course
	Deck   lexicon.Deck
}

// Courses возвращает курсы пользователя с их колодами.
//
// Колода запрашивается на каждый курс отдельно: курсов у человека единицы,
// и городить ради них запрос с объединением значило бы усложнять то, что
// и так дёшево.
func (s *Service) Courses(ctx context.Context, userID user.ID) ([]CourseSummary, error) {
	courses, err := s.deps.Courses.ByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("получить курсы пользователя: %w", err)
	}

	out := make([]CourseSummary, 0, len(courses))
	for _, course := range courses {
		deck, err := s.deps.Decks.ByID(ctx, course.DeckID)
		if err != nil {
			return nil, fmt.Errorf("найти колоду курса %d: %w", course.ID, err)
		}
		out = append(out, CourseSummary{Course: course, Deck: deck})
	}
	return out, nil
}

// HasCourses сообщает, учит ли пользователь уже что-нибудь: по этому
// признаку /start отличает знакомство от повторного захода.
func (s *Service) HasCourses(ctx context.Context, userID user.ID) (bool, error) {
	courses, err := s.deps.Courses.ByUser(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("получить курсы пользователя: %w", err)
	}
	return len(courses) > 0, nil
}

// checkTranslation убеждается, что колода переведена на выбранный язык.
//
// Проверка не формальность: кнопка с языком могла остаться от прошлого
// сообщения, а курс из непереведённых слов — это карточки, на которых
// нечего показать.
func (s *Service) checkTranslation(ctx context.Context, deck *lexicon.Deck, lang lexicon.Language) error {
	if lang.IsZero() {
		return ErrNoLanguage
	}
	if lang == deck.Lang {
		return fmt.Errorf("язык перевода совпадает с языком изучения (%s): %w", lang, ErrSameLanguage)
	}

	available, err := s.deps.Decks.TranslationLanguages(ctx, deck.ID)
	if err != nil {
		return fmt.Errorf("получить языки перевода: %w", err)
	}
	for _, known := range available {
		if known == lang {
			return nil
		}
	}
	return fmt.Errorf("колода %q не переведена на %s: %w", deck.Code, lang, ErrNoTranslations)
}

// ensureSettings заводит настройки по умолчанию, если их ещё нет.
func (s *Service) ensureSettings(ctx context.Context, userID user.ID) (bool, error) {
	_, err := s.deps.Settings.Get(ctx, userID)
	switch {
	case err == nil:
		// Настройки уже есть — это второй курс или повторный /start.
		// Перезаписать их значило бы отобрать у человека то, что он менял.
		return false, nil
	case !errors.Is(err, port.ErrNotFound):
		return false, fmt.Errorf("прочитать настройки: %w", err)
	}

	defaults := user.DefaultSettings(s.deps.DefaultTimezone)
	if err := s.deps.Settings.Save(ctx, userID, defaults); err != nil {
		return false, fmt.Errorf("сохранить настройки: %w", err)
	}
	return true, nil
}

func contains(courses []study.Course, id study.CourseID) bool {
	for _, course := range courses {
		if course.ID == id {
			return true
		}
	}
	return false
}
