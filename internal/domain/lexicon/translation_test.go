package lexicon_test

import (
	"errors"
	"strings"
	"testing"

	"lexi-bot/internal/domain/lexicon"
)

func TestNewTranslation(t *testing.T) {
	t.Parallel()

	tr, err := lexicon.NewTranslation(lexicon.TranslationParams{
		LexemeID:  7,
		Lang:      langRU,
		Text:      "  дом   родной ",
		IsPrimary: true,
		Note:      "  разг. ",
	})
	if err != nil {
		t.Fatalf("NewTranslation() вернул ошибку: %v", err)
	}
	if tr.Text != "дом родной" {
		t.Errorf("Text = %q, ожидалось %q", tr.Text, "дом родной")
	}
	if tr.Note != "разг." {
		t.Errorf("Note = %q, ожидалось %q", tr.Note, "разг.")
	}
	if !tr.IsPrimary {
		t.Error("IsPrimary не сохранён")
	}
	if tr.LexemeID != 7 {
		t.Errorf("LexemeID = %d, ожидалось 7", tr.LexemeID)
	}
}

func TestNewTranslationErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		params lexicon.TranslationParams
		want   error
	}{
		{"без лексемы", lexicon.TranslationParams{Lang: langRU, Text: "дом"}, lexicon.ErrRequired},
		{"отрицательная лексема", lexicon.TranslationParams{LexemeID: -1, Lang: langRU, Text: "дом"}, lexicon.ErrRequired},
		{"без языка", lexicon.TranslationParams{LexemeID: 1, Text: "дом"}, lexicon.ErrRequired},
		{"пустой текст", lexicon.TranslationParams{LexemeID: 1, Lang: langRU}, lexicon.ErrRequired},
		{"текст из пробелов", lexicon.TranslationParams{LexemeID: 1, Lang: langRU, Text: " \t "}, lexicon.ErrRequired},
		{"слишком длинный текст", lexicon.TranslationParams{LexemeID: 1, Lang: langRU, Text: strings.Repeat("а", lexicon.MaxTranslationLen+1)}, lexicon.ErrTooLong},
		{"слишком длинное примечание", lexicon.TranslationParams{LexemeID: 1, Lang: langRU, Text: "дом", Note: strings.Repeat("а", lexicon.MaxNoteLen+1)}, lexicon.ErrTooLong},
		{"управляющий символ", lexicon.TranslationParams{LexemeID: 1, Lang: langRU, Text: "дом\x07"}, lexicon.ErrInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tr, err := lexicon.NewTranslation(tt.params)
			if !errors.Is(err, tt.want) {
				t.Fatalf("NewTranslation() = %v, ожидалась ошибка %v", err, tt.want)
			}
			if tr != (lexicon.Translation{}) {
				t.Errorf("при ошибке возвращён непустой перевод %+v", tr)
			}
		})
	}
}

func TestTranslationsForSameLexemeDifferByLang(t *testing.T) {
	t.Parallel()

	// Одна лексема обслуживает несколько языков перевода — ради этого переводы
	// и вынесены в отдельную сущность.
	ru, err := lexicon.NewTranslation(lexicon.TranslationParams{LexemeID: 1, Lang: langRU, Text: "дом", IsPrimary: true})
	if err != nil {
		t.Fatalf("NewTranslation() вернул ошибку: %v", err)
	}
	en, err := lexicon.NewTranslation(lexicon.TranslationParams{LexemeID: 1, Lang: langEN, Text: "house", IsPrimary: true})
	if err != nil {
		t.Fatalf("NewTranslation() вернул ошибку: %v", err)
	}
	if ru.LexemeID != en.LexemeID {
		t.Fatal("переводы должны ссылаться на одну лексему")
	}
	if ru.Lang == en.Lang {
		t.Error("языки переводов должны различаться")
	}
}
