package user_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"lexi-bot/internal/domain/user"
)

func TestNewUser(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		username string
		want     string
	}{
		{"обычное имя", "durov", "durov"},
		{"собачка отбрасывается", "@durov", "durov"},
		{"пробелы отбрасываются", "  @durov  ", "durov"},
		{"имени может не быть", "", ""},
		{"подчёркивания и цифры допустимы", "lexi_bot_2026", "lexi_bot_2026"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			u, err := user.NewUser(777, tt.username, user.UILangRU)
			if err != nil {
				t.Fatalf("NewUser() вернул ошибку: %v", err)
			}
			if u.Username != tt.want {
				t.Errorf("Username = %q, ожидалось %q", u.Username, tt.want)
			}
			if u.TelegramID != 777 {
				t.Errorf("TelegramID = %d, ожидалось 777", u.TelegramID)
			}
			if !u.IsActive() {
				t.Error("новый пользователь должен быть активен")
			}
		})
	}
}

func TestNewUserErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tgID     user.TelegramID
		username string
		lang     user.UILang
		want     error
	}{
		{"без идентификатора Telegram", 0, "durov", user.UILangRU, user.ErrRequired},
		{"отрицательный идентификатор", -1, "durov", user.UILangRU, user.ErrRequired},
		{"неподдержанный язык интерфейса", 777, "durov", user.UILang("ko"), user.ErrInvalid},
		{"пустой язык интерфейса", 777, "durov", user.UILang(""), user.ErrInvalid},
		{"недопустимый символ в имени", 777, "лексибот", user.UILangRU, user.ErrInvalid},
		{"пробел в имени", 777, "lexi bot", user.UILangRU, user.ErrInvalid},
		{"слишком длинное имя", 777, strings.Repeat("a", user.MaxUsernameLen+1), user.UILangRU, user.ErrTooLong},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			u, err := user.NewUser(tt.tgID, tt.username, tt.lang)
			if !errors.Is(err, tt.want) {
				t.Fatalf("NewUser() = %v, ожидалась ошибка %v", err, tt.want)
			}
			if u != (user.User{}) {
				t.Errorf("при ошибке возвращён непустой пользователь %+v", u)
			}
		})
	}
}

func TestUserSoftDelete(t *testing.T) {
	t.Parallel()

	u, err := user.NewUser(777, "durov", user.UILangEN)
	if err != nil {
		t.Fatalf("NewUser() вернул ошибку: %v", err)
	}
	if !u.IsActive() {
		t.Fatal("пользователь должен быть активен до удаления")
	}

	u.DeletedAt = time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	if u.IsActive() {
		t.Error("после мягкого удаления пользователь не должен считаться активным")
	}
	if err := u.Validate(); err != nil {
		t.Errorf("удалённый пользователь должен оставаться валидным: %v", err)
	}
}

func TestUserValidateFromStorage(t *testing.T) {
	t.Parallel()

	broken := user.User{ID: 1, TelegramID: 777, Username: "плохое имя", UILang: user.UILangRU}
	if !errors.Is(broken.Validate(), user.ErrInvalid) {
		t.Error("Validate() пропустил недопустимое имя пользователя")
	}

	ok := user.User{ID: 1, TelegramID: 777, Username: "durov", UILang: user.UILangRU}
	if err := ok.Validate(); err != nil {
		t.Errorf("Validate() вернул ошибку на корректном пользователе: %v", err)
	}
}
