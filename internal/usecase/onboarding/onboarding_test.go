package onboarding_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/onboarding"
	"lexi-bot/internal/usecase/port"
)

var (
	langKO = lexicon.MustParseLanguage("ko")
	langRU = lexicon.MustParseLanguage("ru")
	langEN = lexicon.MustParseLanguage("en")
)

// fakeDecks — DeckRepo в памяти: только то, что нужно онбордингу.
type fakeDecks struct {
	decks        map[lexicon.DeckID]lexicon.Deck
	translations map[lexicon.DeckID][]lexicon.Language
	failWith     error
}

func (f *fakeDecks) Languages(context.Context) ([]lexicon.Language, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}

	seen := map[lexicon.Language]bool{}
	var out []lexicon.Language
	for _, deck := range f.decks {
		if !seen[deck.Lang] {
			seen[deck.Lang] = true
			out = append(out, deck.Lang)
		}
	}
	return out, nil
}

func (f *fakeDecks) TranslationLanguages(_ context.Context, id lexicon.DeckID) ([]lexicon.Language, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	return f.translations[id], nil
}

func (f *fakeDecks) Builtin(_ context.Context, lang lexicon.Language) ([]lexicon.Deck, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}

	var out []lexicon.Deck
	for _, deck := range f.decks {
		if deck.Lang == lang {
			out = append(out, deck)
		}
	}
	return out, nil
}

func (f *fakeDecks) ByID(_ context.Context, id lexicon.DeckID) (lexicon.Deck, error) {
	if f.failWith != nil {
		return lexicon.Deck{}, f.failWith
	}
	if deck, ok := f.decks[id]; ok {
		return deck, nil
	}
	return lexicon.Deck{}, port.ErrNotFound
}

func (f *fakeDecks) ByCode(context.Context, string) (lexicon.Deck, error) {
	return lexicon.Deck{}, port.ErrNotFound
}

func (f *fakeDecks) EnsureBuiltin(context.Context, *lexicon.Deck) (lexicon.Deck, error) {
	// Заглушке нечего заводить: онбординг и сессия колоды не создают.
	return lexicon.Deck{}, nil
}

func (f *fakeDecks) EnsurePersonal(context.Context, int64, lexicon.Language, string) (lexicon.Deck, error) {
	return lexicon.Deck{}, nil
}
func (f *fakeDecks) AddItems(context.Context, []lexicon.DeckItem) error { return nil }

func (f *fakeDecks) DistractorTerms(context.Context, port.DistractorQuery) ([]lexicon.Lexeme, error) {
	return nil, nil
}

func (f *fakeDecks) Distractors(context.Context, port.DistractorQuery) ([]lexicon.Translation, error) {
	return nil, nil
}
func (f *fakeDecks) Items(context.Context, lexicon.DeckID, int, int) ([]lexicon.DeckItem, error) {
	return nil, nil
}

// fakeCourses — CourseRepo в памяти.
type fakeCourses struct {
	byID   map[study.CourseID]study.Course
	nextID study.CourseID
}

func newFakeCourses() *fakeCourses {
	return &fakeCourses{byID: map[study.CourseID]study.Course{}, nextID: 1}
}

func (f *fakeCourses) Ensure(_ context.Context, c study.Course) (study.Course, error) {
	for _, existing := range f.byID {
		if existing.UserID == c.UserID && existing.DeckID == c.DeckID && existing.TranslationLang == c.TranslationLang {
			return existing, nil
		}
	}
	c.ID = f.nextID
	f.nextID++
	f.byID[c.ID] = c
	return c, nil
}

func (f *fakeCourses) ByID(_ context.Context, id study.CourseID) (study.Course, error) {
	if c, ok := f.byID[id]; ok {
		return c, nil
	}
	return study.Course{}, port.ErrNotFound
}

func (f *fakeCourses) ByUser(_ context.Context, userID user.ID) ([]study.Course, error) {
	var out []study.Course
	for _, c := range f.byID {
		if c.UserID == int64(userID) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeCourses) SetStatus(_ context.Context, id study.CourseID, status study.CourseStatus) error {
	c, ok := f.byID[id]
	if !ok {
		return port.ErrNotFound
	}
	c.Status = status
	f.byID[id] = c
	return nil
}

// fakeSettings — SettingsRepo в памяти.
type fakeSettings struct {
	byUser map[user.ID]user.Settings
	saves  int
	fail   error
}

func newFakeSettings() *fakeSettings {
	return &fakeSettings{byUser: map[user.ID]user.Settings{}}
}

func (f *fakeSettings) Get(_ context.Context, userID user.ID) (user.Settings, error) {
	if f.fail != nil {
		return user.Settings{}, f.fail
	}
	if s, ok := f.byUser[userID]; ok {
		return s, nil
	}
	return user.Settings{}, port.ErrNotFound
}

func (f *fakeSettings) Save(_ context.Context, userID user.ID, s user.Settings) error {
	if f.fail != nil {
		return f.fail
	}
	f.saves++
	f.byUser[userID] = s
	return nil
}

// fakeUsers — UserRepo в памяти: онбордингу нужен только SetUILang.
type fakeUsers struct {
	langs   map[user.ID]user.UILang
	current map[user.ID]study.CourseID
	fail    error
}

func newFakeUsers() *fakeUsers {
	return &fakeUsers{
		langs:   map[user.ID]user.UILang{},
		current: map[user.ID]study.CourseID{},
	}
}

func (f *fakeUsers) Ensure(_ context.Context, u *user.User) (user.User, bool, error) {
	return *u, false, nil
}
func (f *fakeUsers) ByTelegramID(context.Context, user.TelegramID) (user.User, error) {
	return user.User{}, port.ErrNotFound
}
func (f *fakeUsers) ByID(context.Context, user.ID) (user.User, error) {
	return user.User{}, port.ErrNotFound
}

func (f *fakeUsers) SetUILang(_ context.Context, id user.ID, lang user.UILang) error {
	if f.fail != nil {
		return f.fail
	}
	f.langs[id] = lang
	return nil
}

// SetCurrentCourse запоминает выбранный курс. Заглушка, молча забывавшая
// его, сделала бы бессмысленной проверку того, что онбординг курс запоминает.
func (f *fakeUsers) SetCurrentCourse(_ context.Context, id user.ID, courseID study.CourseID) error {
	if f.fail != nil {
		return f.fail
	}
	f.current[id] = courseID
	return nil
}

func (f *fakeUsers) SoftDelete(context.Context, user.ID, time.Time) error { return nil }
func (f *fakeUsers) Purge(context.Context, user.ID) error                 { return nil }

// fixture — сценарий с готовыми колодами.
type fixture struct {
	service  *onboarding.Service
	decks    *fakeDecks
	courses  *fakeCourses
	settings *fakeSettings
	users    *fakeUsers
}

const (
	koreanDeck  lexicon.DeckID = 1
	englishDeck lexicon.DeckID = 2
)

func newFixture(t *testing.T) *fixture {
	t.Helper()

	decks := &fakeDecks{
		decks: map[lexicon.DeckID]lexicon.Deck{
			koreanDeck:  {ID: koreanDeck, Code: "ko-top-2000", Lang: langKO, Title: "Корейский: топ-2000", Size: 2000},
			englishDeck: {ID: englishDeck, Code: "en-top-1000", Lang: langEN, Title: "English: top 1000", Size: 1000},
		},
		translations: map[lexicon.DeckID][]lexicon.Language{
			koreanDeck:  {langEN, langRU},
			englishDeck: {langRU},
		},
	}

	f := &fixture{decks: decks, courses: newFakeCourses(), settings: newFakeSettings(), users: newFakeUsers()}

	service, err := onboarding.New(onboarding.Deps{
		Users:           f.users,
		Settings:        f.settings,
		Decks:           f.decks,
		Courses:         f.courses,
		DefaultTimezone: user.MustParseTimezone("Europe/Moscow"),
	})
	if err != nil {
		t.Fatalf("New() вернул ошибку: %v", err)
	}
	f.service = service
	return f
}

func TestOnboardingHappyPath(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()

	langs, err := f.service.LearningLanguages(ctx)
	if err != nil {
		t.Fatalf("LearningLanguages() вернул ошибку: %v", err)
	}
	if len(langs) != 2 {
		t.Fatalf("языков %d, ожидалось два", len(langs))
	}

	decks, err := f.service.Decks(ctx, langKO)
	if err != nil {
		t.Fatalf("Decks() вернул ошибку: %v", err)
	}
	if len(decks) != 1 || decks[0].ID != koreanDeck {
		t.Fatalf("колоды = %+v", decks)
	}

	translations, err := f.service.TranslationLanguages(ctx, koreanDeck)
	if err != nil {
		t.Fatalf("TranslationLanguages() вернул ошибку: %v", err)
	}
	if len(translations) != 2 {
		t.Fatalf("языков перевода %d, ожидалось два", len(translations))
	}

	result, err := f.service.Complete(ctx, onboarding.Choice{
		UserID: 42, DeckID: koreanDeck, TranslationLang: langRU,
	})
	if err != nil {
		t.Fatalf("Complete() вернул ошибку: %v", err)
	}

	if result.Course.ID == 0 || !result.Course.IsActive() {
		t.Errorf("курс = %+v", result.Course)
	}
	if result.Course.TranslationLang != langRU || result.Course.DeckID != koreanDeck {
		t.Errorf("курс заведён не по выбору: %+v", result.Course)
	}
	if !result.CourseCreated || !result.SettingsCreated {
		t.Errorf("признаки создания = %+v", result)
	}
	if result.Deck.Title == "" {
		t.Error("колода не вернулась: её название нужно показать в ответе")
	}
	// Выбранный курс запомнен как текущий. Без этого занятие работало бы
	// через запасной путь «первый активный», и человек, заведя второй курс,
	// попадал бы не в тот, который только что выбрал.
	if f.users.current[42] != result.Course.ID {
		t.Errorf("текущий курс = %d, ожидался %d", f.users.current[42], result.Course.ID)
	}

	// Настройки по умолчанию: таймзона из конфигурации, напоминание
	// выключено, лимиты стандартные.
	settings, err := f.settings.Get(ctx, 42)
	if err != nil {
		t.Fatalf("настройки не сохранены: %v", err)
	}
	if settings.Timezone.String() != "Europe/Moscow" {
		t.Errorf("таймзона = %q", settings.Timezone)
	}
	if settings.RemindersEnabled() {
		t.Error("напоминание не должно включаться само")
	}
	if settings.NewPerDay != user.DefaultNewPerDay {
		t.Errorf("слов в день = %d", settings.NewPerDay)
	}
}

func TestOnboardingRepeatKeepsProgress(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()

	first, err := f.service.Complete(ctx, onboarding.Choice{UserID: 42, DeckID: koreanDeck, TranslationLang: langRU})
	if err != nil {
		t.Fatalf("Complete() вернул ошибку: %v", err)
	}

	// Пользователь успел поменять настройки под себя.
	changed, err := f.settings.byUser[42].WithNewPerDay(30)
	if err != nil {
		t.Fatalf("WithNewPerDay() вернул ошибку: %v", err)
	}
	if err := f.settings.Save(ctx, 42, changed); err != nil {
		t.Fatalf("Save() вернул ошибку: %v", err)
	}
	savesBefore := f.settings.saves

	// Повторный проход по тому же выбору: тот же курс, настройки целы.
	second, err := f.service.Complete(ctx, onboarding.Choice{UserID: 42, DeckID: koreanDeck, TranslationLang: langRU})
	if err != nil {
		t.Fatalf("повторный Complete() вернул ошибку: %v", err)
	}

	if second.Course.ID != first.Course.ID {
		t.Errorf("заведён второй курс: %d против %d", second.Course.ID, first.Course.ID)
	}
	if second.CourseCreated || second.SettingsCreated {
		t.Errorf("повторный проход отчитался о создании: %+v", second)
	}
	if f.settings.saves != savesBefore {
		t.Error("настройки перезаписаны: пользователь потерял свои изменения")
	}
	if f.settings.byUser[42].NewPerDay != 30 {
		t.Errorf("слов в день = %d, ожидалось 30", f.settings.byUser[42].NewPerDay)
	}
}

func TestOnboardingSecondCourse(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.service.Complete(ctx, onboarding.Choice{UserID: 42, DeckID: koreanDeck, TranslationLang: langRU}); err != nil {
		t.Fatalf("Complete() вернул ошибку: %v", err)
	}

	// Второй курс по другой колоде — это новый курс, но настройки уже есть.
	second, err := f.service.Complete(ctx, onboarding.Choice{UserID: 42, DeckID: englishDeck, TranslationLang: langRU})
	if err != nil {
		t.Fatalf("Complete() вернул ошибку: %v", err)
	}
	if !second.CourseCreated {
		t.Error("второй курс должен считаться новым")
	}
	if second.SettingsCreated {
		t.Error("настройки заводятся один раз")
	}

	courses, err := f.courses.ByUser(ctx, 42)
	if err != nil {
		t.Fatalf("ByUser() вернул ошибку: %v", err)
	}
	if len(courses) != 2 {
		t.Errorf("курсов %d, ожидалось два", len(courses))
	}
}

func TestOnboardingRejectsBadChoice(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()

	tests := []struct {
		name   string
		choice onboarding.Choice
		want   error
	}{
		{
			name:   "без пользователя",
			choice: onboarding.Choice{DeckID: koreanDeck, TranslationLang: langRU},
			want:   onboarding.ErrNoUser,
		},
		{
			name:   "несуществующая колода",
			choice: onboarding.Choice{UserID: 42, DeckID: 999, TranslationLang: langRU},
			want:   port.ErrNotFound,
		},
		{
			name:   "без языка перевода",
			choice: onboarding.Choice{UserID: 42, DeckID: koreanDeck},
			want:   onboarding.ErrNoLanguage,
		},
		{
			name: "язык перевода без переводов",
			// Кнопка могла остаться от прошлого сообщения: курс из
			// непереведённых слов — это карточки, на которых нечего показать.
			choice: onboarding.Choice{UserID: 42, DeckID: englishDeck, TranslationLang: langEN},
			want:   onboarding.ErrSameLanguage,
		},
		{
			name:   "перевод на язык, которого нет у колоды",
			choice: onboarding.Choice{UserID: 42, DeckID: englishDeck, TranslationLang: langKO},
			want:   onboarding.ErrNoTranslations,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := f.service.Complete(ctx, tt.choice)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Complete() = %v, ожидалась ошибка %v", err, tt.want)
			}
			if result.Course.ID != 0 {
				t.Errorf("при ошибке заведён курс %+v", result.Course)
			}
		})
	}
}

func TestOnboardingWithoutDecks(t *testing.T) {
	t.Parallel()

	// Словари не загружены: пользователю нечего предложить, и делать вид,
	// что всё в порядке, нельзя.
	empty := &fakeDecks{decks: map[lexicon.DeckID]lexicon.Deck{}}
	service, err := onboarding.New(onboarding.Deps{
		Users: newFakeUsers(), Settings: newFakeSettings(), Decks: empty, Courses: newFakeCourses(),
	})
	if err != nil {
		t.Fatalf("New() вернул ошибку: %v", err)
	}

	if _, err := service.LearningLanguages(context.Background()); !errors.Is(err, onboarding.ErrNothingToLearn) {
		t.Errorf("LearningLanguages() = %v, ожидалась ErrNothingToLearn", err)
	}
	if _, err := service.Decks(context.Background(), langKO); !errors.Is(err, onboarding.ErrNothingToLearn) {
		t.Errorf("Decks() = %v, ожидалась ErrNothingToLearn", err)
	}
}

func TestOnboardingSetUILang(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()

	if err := f.service.SetUILang(ctx, 42, user.UILangEN); err != nil {
		t.Fatalf("SetUILang() вернул ошибку: %v", err)
	}
	if f.users.langs[42] != user.UILangEN {
		t.Errorf("язык = %q", f.users.langs[42])
	}

	if err := f.service.SetUILang(ctx, 42, user.UILang("ko")); !errors.Is(err, onboarding.ErrUnsupportedLanguage) {
		t.Errorf("SetUILang() = %v, ожидалась ErrUnsupportedLanguage", err)
	}
}

func TestOnboardingHasCourses(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()

	has, err := f.service.HasCourses(ctx, 42)
	if err != nil {
		t.Fatalf("HasCourses() вернул ошибку: %v", err)
	}
	if has {
		t.Error("у нового пользователя курсов быть не должно")
	}

	if _, err := f.service.Complete(ctx, onboarding.Choice{UserID: 42, DeckID: koreanDeck, TranslationLang: langRU}); err != nil {
		t.Fatalf("Complete() вернул ошибку: %v", err)
	}

	has, err = f.service.HasCourses(ctx, 42)
	if err != nil {
		t.Fatalf("HasCourses() вернул ошибку: %v", err)
	}
	if !has {
		t.Error("после онбординга курс должен найтись")
	}
}

func TestOnboardingNeedsDependencies(t *testing.T) {
	t.Parallel()

	if _, err := onboarding.New(onboarding.Deps{}); err == nil {
		t.Error("сценарий без зависимостей должен быть ошибкой")
	}

	// Таймзона по умолчанию не обязательна: без неё берётся UTC, а не паника
	// посреди онбординга.
	service, err := onboarding.New(onboarding.Deps{
		Users: newFakeUsers(), Settings: newFakeSettings(),
		Decks: &fakeDecks{decks: map[lexicon.DeckID]lexicon.Deck{}}, Courses: newFakeCourses(),
	})
	if err != nil || service == nil {
		t.Fatalf("New() без таймзоны = %v", err)
	}
}

func TestOnboardingReportsStorageFailures(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	broken := errors.New("база недоступна")

	// Ошибка хранилища должна доезжать до вызывающего с объяснением, что
	// именно не получилось: иначе пользователь увидит «что-то пошло не так»
	// на каждом шаге, а в логе будет голое «no rows».
	t.Run("на списке языков", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t)
		f.decks.failWith = broken

		for name, call := range map[string]func() error{
			"LearningLanguages": func() error {
				_, err := f.service.LearningLanguages(ctx)
				return err
			},
			"Decks": func() error {
				_, err := f.service.Decks(ctx, langKO)
				return err
			},
			"TranslationLanguages": func() error {
				_, err := f.service.TranslationLanguages(ctx, koreanDeck)
				return err
			},
			"Complete": func() error {
				_, err := f.service.Complete(ctx, onboarding.Choice{UserID: 42, DeckID: koreanDeck, TranslationLang: langRU})
				return err
			},
		} {
			if err := call(); !errors.Is(err, broken) {
				t.Errorf("%s: ошибка = %v, ожидалась ошибка хранилища", name, err)
			}
		}
	})

	t.Run("на настройках", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t)
		f.settings.fail = broken

		if _, err := f.service.Complete(ctx, onboarding.Choice{UserID: 42, DeckID: koreanDeck, TranslationLang: langRU}); !errors.Is(err, broken) {
			t.Errorf("Complete() = %v, ожидалась ошибка хранилища", err)
		}
		if len(f.courses.byID) != 0 {
			t.Error("курс не должен заводиться, если настройки сохранить не удалось")
		}
	})

	t.Run("на смене языка интерфейса", func(t *testing.T) {
		t.Parallel()

		f := newFixture(t)
		f.users.fail = broken

		if err := f.service.SetUILang(ctx, 42, user.UILangEN); !errors.Is(err, broken) {
			t.Errorf("SetUILang() = %v, ожидалась ошибка хранилища", err)
		}
	})
}

// Contains сообщает, есть ли слово в колоде. Личный словарь заглушке
// не нужен: она обслуживает сценарии, которые своих слов не заводят.
func (*fakeDecks) Contains(context.Context, lexicon.DeckID, lexicon.LexemeID) (bool, error) {
	return false, nil
}
