package onboarding

import "errors"

// Ошибки сценария. Каждая означает ситуацию, о которой пользователю нужно
// сказать своими словами, а не «что-то пошло не так».
var (
	// ErrNothingToLearn — встроенных колод нет. Для пользователя это значит
	// «бот ещё не готов», а для нас — что словари не загружены (T-036).
	ErrNothingToLearn = errors.New("учить нечего: встроенные колоды не загружены")
	// ErrNoTranslations — колода не переведена на выбранный язык.
	ErrNoTranslations = errors.New("для колоды нет переводов на этот язык")
	// ErrNoLanguage — язык не выбран.
	ErrNoLanguage = errors.New("язык не выбран")
	// ErrSameLanguage — язык перевода совпадает с языком изучения.
	ErrSameLanguage = errors.New("язык перевода совпадает с языком изучения")
	// ErrUnsupportedLanguage — язык интерфейса, которого у нас нет.
	ErrUnsupportedLanguage = errors.New("язык интерфейса не поддерживается")
	// ErrNoUser — сценарий позвали без пользователя.
	ErrNoUser = errors.New("онбординг без пользователя")
)
