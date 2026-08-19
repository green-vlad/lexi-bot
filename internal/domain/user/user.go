// Package user описывает пользователя бота и его настройки обучения.
//
// Здесь живут правила, которые не зависят ни от Telegram, ни от базы: границы
// допустимых настроек, язык интерфейса и календарь пользователя. Календарь
// важнее, чем кажется: дневные лимиты («N новых слов в день») считаются по
// суткам в таймзоне пользователя, а не по UTC, и вся эта арифметика собрана
// в Timezone, чтобы её можно было проверить тестами без базы и без сети.
//
// Пакет не обращается к текущему времени сам: момент всегда приходит аргументом.
package user

import (
	"fmt"
	"strings"
	"time"
)

// MaxUsernameLen — предел длины имени пользователя Telegram.
const MaxUsernameLen = 32

// ID — внутренний идентификатор пользователя в нашей базе.
type ID int64

// TelegramID — идентификатор пользователя в Telegram. Именно он приходит в
// апдейтах и служит ключом, по которому мы узнаём пользователя.
type TelegramID int64

// User — учётная запись. Всё, что относится к обучению, вынесено в Settings;
// здесь остаётся только то, что отвечает на вопрос «кто это».
type User struct {
	ID         ID
	TelegramID TelegramID
	// Username — имя вида @durov без собачки; у пользователя его может не быть.
	Username string
	UILang   UILang
	// DeletedAt — момент мягкого удаления. Нулевое значение означает, что
	// учётная запись активна: строки не удаляются физически, чтобы журнал
	// повторений и статистика оставались непротиворечивыми.
	DeletedAt time.Time
}

// NewUser создаёт учётную запись по данным из апдейта Telegram.
func NewUser(tgID TelegramID, username string, lang UILang) (User, error) {
	name, err := normalizeUsername(username)
	if err != nil {
		return User{}, err
	}

	u := User{TelegramID: tgID, Username: name, UILang: lang}
	if err := u.Validate(); err != nil {
		return User{}, err
	}
	return u, nil
}

// Validate проверяет инварианты учётной записи, ничего не изменяя.
func (u User) Validate() error {
	if u.TelegramID <= 0 {
		return fmt.Errorf("tg_user_id: %w", ErrRequired)
	}
	if !u.UILang.IsSupported() {
		return fmt.Errorf("ui_lang %q: %w", u.UILang, ErrInvalid)
	}
	if u.Username != "" {
		if _, err := normalizeUsername(u.Username); err != nil {
			return err
		}
	}
	return nil
}

// IsActive сообщает, что учётная запись не удалена.
func (u User) IsActive() bool { return u.DeletedAt.IsZero() }

// normalizeUsername убирает пробелы и ведущую собачку и проверяет состав.
// Пустое имя допустимо: в Telegram username необязателен.
func normalizeUsername(s string) (string, error) {
	name := strings.TrimPrefix(strings.TrimSpace(s), "@")
	if name == "" {
		return "", nil
	}
	if len(name) > MaxUsernameLen {
		return "", fmt.Errorf("tg_username: %w (не более %d символов)", ErrTooLong, MaxUsernameLen)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
		if !ok {
			return "", fmt.Errorf("tg_username %q: %w (допустимы латиница, цифры и подчёркивание)", s, ErrInvalid)
		}
	}
	return name, nil
}
