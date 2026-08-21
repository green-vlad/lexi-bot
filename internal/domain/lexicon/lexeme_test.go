package lexicon_test

import (
	"errors"
	"strings"
	"testing"

	"lexi-bot/internal/domain/lexicon"
)

var (
	langKO = lexicon.MustParseLanguage("ko")
	langRU = lexicon.MustParseLanguage("ru")
	langEN = lexicon.MustParseLanguage("en")
	langES = lexicon.MustParseLanguage("es")
)

func TestNewLexemeNormalizes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		params      lexicon.LexemeParams
		wantTerm    string
		wantReading string
	}{
		{
			name:     "краевые пробелы убираются",
			params:   lexicon.LexemeParams{Lang: langRU, Term: "  дом  "},
			wantTerm: "дом",
		},
		{
			name:     "внутренние пробелы схлопываются",
			params:   lexicon.LexemeParams{Lang: langRU, Term: "большой   дом"},
			wantTerm: "большой дом",
		},
		{
			name:     "перевод строки считается пробелом",
			params:   lexicon.LexemeParams{Lang: langEN, Term: "get\tup"},
			wantTerm: "get up",
		},
		{
			name:        "транскрипция нормализуется так же",
			params:      lexicon.LexemeParams{Lang: langKO, Term: "안녕하세요", Reading: "  annyeong  haseyo "},
			wantTerm:    "안녕하세요",
			wantReading: "annyeong haseyo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			lex, err := lexicon.NewLexeme(tt.params)
			if err != nil {
				t.Fatalf("NewLexeme() вернул ошибку: %v", err)
			}
			if lex.Term != tt.wantTerm {
				t.Errorf("Term = %q, ожидалось %q", lex.Term, tt.wantTerm)
			}
			if lex.Reading != tt.wantReading {
				t.Errorf("Reading = %q, ожидалось %q", lex.Reading, tt.wantReading)
			}
		})
	}
}

func TestNewLexemeErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		params lexicon.LexemeParams
		want   error
	}{
		{"без языка", lexicon.LexemeParams{Term: "дом"}, lexicon.ErrRequired},
		{"пустой term", lexicon.LexemeParams{Lang: langRU}, lexicon.ErrRequired},
		{"term из одних пробелов", lexicon.LexemeParams{Lang: langRU, Term: "   "}, lexicon.ErrRequired},
		{"слишком длинный term", lexicon.LexemeParams{Lang: langRU, Term: strings.Repeat("а", lexicon.MaxTermLen+1)}, lexicon.ErrTooLong},
		{"управляющий символ в term", lexicon.LexemeParams{Lang: langRU, Term: "дом\x00"}, lexicon.ErrInvalid},
		{"слишком длинная транскрипция", lexicon.LexemeParams{Lang: langKO, Term: "집", Reading: strings.Repeat("a", lexicon.MaxReadingLen+1)}, lexicon.ErrTooLong},
		{"неизвестная часть речи", lexicon.LexemeParams{Lang: langRU, Term: "дом", POS: lexicon.PartOfSpeech("существительное")}, lexicon.ErrInvalid},
		{"отрицательная частотность", lexicon.LexemeParams{Lang: langRU, Term: "дом", FreqRank: -1}, lexicon.ErrInvalid},
		{"отрицательный владелец", lexicon.LexemeParams{Lang: langRU, Term: "дом", OwnerID: -7}, lexicon.ErrInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			lex, err := lexicon.NewLexeme(tt.params)
			if !errors.Is(err, tt.want) {
				t.Fatalf("NewLexeme() = %v, ожидалась ошибка %v", err, tt.want)
			}
			if lex != (lexicon.Lexeme{}) {
				t.Errorf("при ошибке возвращена непустая лексема %+v", lex)
			}
		})
	}
}

func TestLexemeErrorMentionsField(t *testing.T) {
	t.Parallel()

	_, err := lexicon.NewLexeme(lexicon.LexemeParams{Lang: langRU})
	if err == nil || !strings.Contains(err.Error(), "term") {
		t.Fatalf("ошибка %v не называет поле term", err)
	}
}

func TestLexemeIsBuiltin(t *testing.T) {
	t.Parallel()

	builtin, err := lexicon.NewLexeme(lexicon.LexemeParams{Lang: langKO, Term: "집", FreqRank: 12})
	if err != nil {
		t.Fatalf("NewLexeme() вернул ошибку: %v", err)
	}
	if !builtin.IsBuiltin() {
		t.Error("лексема без владельца должна считаться встроенной")
	}

	personal, err := lexicon.NewLexeme(lexicon.LexemeParams{Lang: langKO, Term: "집", OwnerID: 42})
	if err != nil {
		t.Fatalf("NewLexeme() вернул ошибку: %v", err)
	}
	if personal.IsBuiltin() {
		t.Error("лексема с владельцем не должна считаться встроенной")
	}
}

func TestParsePartOfSpeech(t *testing.T) {
	t.Parallel()

	valid := map[string]lexicon.PartOfSpeech{
		"noun":   lexicon.POSNoun,
		"VERB":   lexicon.POSVerb,
		" adj ":  lexicon.POSAdjective,
		"":       lexicon.POSUnknown,
		"   ":    lexicon.POSUnknown,
		"phrase": lexicon.POSPhrase,
	}
	for in, want := range valid {
		got, err := lexicon.ParsePartOfSpeech(in)
		if err != nil {
			t.Fatalf("ParsePartOfSpeech(%q) вернул ошибку: %v", in, err)
		}
		if got != want {
			t.Errorf("ParsePartOfSpeech(%q) = %q, ожидалось %q", in, got, want)
		}
	}

	for _, in := range []string{"noun."} {
		if _, err := lexicon.ParsePartOfSpeech(in); !errors.Is(err, lexicon.ErrInvalid) {
			t.Errorf("ParsePartOfSpeech(%q) = %v, ожидалась ошибка ErrInvalid", in, err)
		}
	}
	if _, err := lexicon.ParsePartOfSpeech(strings.Repeat("n", 17)); !errors.Is(err, lexicon.ErrTooLong) {
		t.Error("слишком длинная часть речи должна давать ErrTooLong")
	}
}

func TestLexemeValidateFromStorage(t *testing.T) {
	t.Parallel()

	// Значение, восстановленное из базы, минует нормализацию, поэтому Validate
	// обязан ловить те же нарушения самостоятельно.
	broken := lexicon.Lexeme{ID: 1, Lang: langRU, Term: ""}
	if !errors.Is(broken.Validate(), lexicon.ErrRequired) {
		t.Error("Validate() пропустил пустой term")
	}

	ok := lexicon.Lexeme{ID: 1, Lang: langRU, Term: "дом", POS: lexicon.POSNoun, FreqRank: 3}
	if err := ok.Validate(); err != nil {
		t.Errorf("Validate() вернул ошибку на корректной лексеме: %v", err)
	}
}
