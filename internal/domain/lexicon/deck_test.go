package lexicon_test

import (
	"errors"
	"strings"
	"testing"

	"lexi-bot/internal/domain/lexicon"
)

func TestNewBuiltinDeck(t *testing.T) {
	t.Parallel()

	deck, err := lexicon.NewBuiltinDeck("KO-top-2000", langKO, "  Корейский:  топ-2000 ", " Частотный список ")
	if err != nil {
		t.Fatalf("NewBuiltinDeck() вернул ошибку: %v", err)
	}
	if deck.Code != "ko-top-2000" {
		t.Errorf("Code = %q, ожидалось %q", deck.Code, "ko-top-2000")
	}
	if deck.Title != "Корейский: топ-2000" {
		t.Errorf("Title = %q", deck.Title)
	}
	if deck.Description != "Частотный список" {
		t.Errorf("Description = %q", deck.Description)
	}
	if !deck.IsBuiltin() {
		t.Error("колода без владельца должна считаться встроенной")
	}
}

func TestNewBuiltinDeckErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		code  string
		lang  lexicon.Language
		title string
		want  error
	}{
		{"без слага", "", langKO, "Корейский", lexicon.ErrRequired},
		{"без языка", "ko-top-2000", lexicon.Language{}, "Корейский", lexicon.ErrRequired},
		{"без названия", "ko-top-2000", langKO, "  ", lexicon.ErrRequired},
		{"слишком длинное название", "ko-top-2000", langKO, strings.Repeat("а", lexicon.MaxTitleLen+1), lexicon.ErrTooLong},
		{"слишком длинный слаг", strings.Repeat("a", lexicon.MaxDeckCodeLen+1), langKO, "Корейский", lexicon.ErrTooLong},
		{"пробел в слаге", "ko top", langKO, "Корейский", lexicon.ErrInvalid},
		{"кириллица в слаге", "ко-топ", langKO, "Корейский", lexicon.ErrInvalid},
		{"подчёркивание в слаге", "ko_top", langKO, "Корейский", lexicon.ErrInvalid},
		{"дефис в начале слага", "-ko-top", langKO, "Корейский", lexicon.ErrInvalid},
		{"дефис в конце слага", "ko-top-", langKO, "Корейский", lexicon.ErrInvalid},
		{"двойной дефис в слаге", "ko--top", langKO, "Корейский", lexicon.ErrInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			deck, err := lexicon.NewBuiltinDeck(tt.code, tt.lang, tt.title, "")
			if !errors.Is(err, tt.want) {
				t.Fatalf("NewBuiltinDeck() = %v, ожидалась ошибка %v", err, tt.want)
			}
			if deck != (lexicon.Deck{}) {
				t.Errorf("при ошибке возвращена непустая колода %+v", deck)
			}
		})
	}
}

func TestNewPersonalDeck(t *testing.T) {
	t.Parallel()

	deck, err := lexicon.NewPersonalDeck(42, langEN, "Мои слова")
	if err != nil {
		t.Fatalf("NewPersonalDeck() вернул ошибку: %v", err)
	}
	if deck.IsBuiltin() {
		t.Error("колода с владельцем не должна считаться встроенной")
	}
	if deck.Code != "" {
		t.Errorf("Code = %q, у личной колоды слага быть не должно", deck.Code)
	}
	if deck.OwnerID != 42 {
		t.Errorf("OwnerID = %d, ожидалось 42", deck.OwnerID)
	}
}

func TestNewPersonalDeckErrors(t *testing.T) {
	t.Parallel()

	if _, err := lexicon.NewPersonalDeck(42, langEN, " "); !errors.Is(err, lexicon.ErrRequired) {
		t.Errorf("пустое название = %v, ожидалась ErrRequired", err)
	}
	if _, err := lexicon.NewPersonalDeck(-1, langEN, "Мои слова"); !errors.Is(err, lexicon.ErrInvalid) {
		t.Errorf("отрицательный владелец = %v, ожидалась ErrInvalid", err)
	}
	if _, err := lexicon.NewPersonalDeck(42, lexicon.Language{}, "Мои слова"); !errors.Is(err, lexicon.ErrRequired) {
		t.Errorf("отсутствие языка = %v, ожидалась ErrRequired", err)
	}
}

func TestDeckValidateFromStorage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		deck lexicon.Deck
		want error
	}{
		{"встроенная без слага", lexicon.Deck{ID: 1, Lang: langKO, Title: "Корейский"}, lexicon.ErrRequired},
		{"личная со слагом", lexicon.Deck{ID: 1, OwnerID: 5, Code: "my", Lang: langKO, Title: "Мои"}, lexicon.ErrInvalid},
		{"битый слаг", lexicon.Deck{ID: 1, Code: "ko top", Lang: langKO, Title: "Корейский"}, lexicon.ErrInvalid},
		{"отрицательный размер", lexicon.Deck{ID: 1, Code: "ko-top", Lang: langKO, Title: "Корейский", Size: -1}, lexicon.ErrInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if err := tt.deck.Validate(); !errors.Is(err, tt.want) {
				t.Errorf("Validate() = %v, ожидалась ошибка %v", err, tt.want)
			}
		})
	}

	ok := lexicon.Deck{ID: 1, Code: "ko-top-2000", Lang: langKO, Title: "Корейский", Size: 2000}
	if err := ok.Validate(); err != nil {
		t.Errorf("Validate() вернул ошибку на корректной колоде: %v", err)
	}
}

func TestNewDeckItem(t *testing.T) {
	t.Parallel()

	item, err := lexicon.NewDeckItem(3, 17, 0)
	if err != nil {
		t.Fatalf("NewDeckItem() вернул ошибку: %v", err)
	}
	if item.DeckID != 3 || item.LexemeID != 17 || item.Position != 0 {
		t.Errorf("NewDeckItem() = %+v, поля не сохранены", item)
	}
}

func TestNewDeckItemErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		deckID   lexicon.DeckID
		lexemeID lexicon.LexemeID
		position int
		want     error
	}{
		{"без колоды", 0, 17, 1, lexicon.ErrRequired},
		{"без лексемы", 3, 0, 1, lexicon.ErrRequired},
		{"отрицательная позиция", 3, 17, -1, lexicon.ErrInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			item, err := lexicon.NewDeckItem(tt.deckID, tt.lexemeID, tt.position)
			if !errors.Is(err, tt.want) {
				t.Fatalf("NewDeckItem() = %v, ожидалась ошибка %v", err, tt.want)
			}
			if item != (lexicon.DeckItem{}) {
				t.Errorf("при ошибке возвращён непустой элемент %+v", item)
			}
		})
	}
}
