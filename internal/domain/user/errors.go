package user

import "errors"

// Ошибки валидации. Категорию даёт errors.Is, подробности — текст обёртки,
// в котором всегда названо поле.
var (
	// ErrRequired — обязательное поле не заполнено.
	ErrRequired = errors.New("обязательное поле не заполнено")
	// ErrTooLong — значение длиннее допустимого предела.
	ErrTooLong = errors.New("значение слишком длинное")
	// ErrInvalid — значение непустое, но не проходит правила поля.
	ErrInvalid = errors.New("недопустимое значение")
	// ErrOutOfRange — число вне допустимых границ настройки.
	ErrOutOfRange = errors.New("значение вне допустимых границ")
)
