package seeds_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"lexi-bot/internal/adapter/seeds"
	seedfiles "lexi-bot/seeds"
)

func TestLoadRealSeeds(t *testing.T) {
	t.Parallel()

	decks, err := seeds.Load(seedfiles.FS)
	if err != nil {
		t.Fatalf("Load() вернул ошибку: %v", err)
	}
	if len(decks) == 0 {
		t.Fatal("встроенных колод нет")
	}

	for _, deck := range decks {
		if err := deck.Deck.Validate(); err != nil {
			t.Errorf("колода %q не проходит валидацию: %v", deck.Deck.Code, err)
		}
		if len(deck.Words) == 0 {
			t.Errorf("колода %q пуста", deck.Deck.Code)
		}
		if deck.Deck.Size != len(deck.Words) {
			t.Errorf("колода %q: размер %d при %d словах", deck.Deck.Code, deck.Deck.Size, len(deck.Words))
		}

		for _, word := range deck.Words {
			if err := word.Lexeme.Validate(); err != nil {
				t.Errorf("колода %q, строка %d: %v", deck.Deck.Code, word.Line, err)
			}
			if len(word.Translations) == 0 {
				t.Errorf("колода %q, строка %d: слово без перевода", deck.Deck.Code, word.Line)
			}
			// Порядок изучения задаёт файл: слова в учебнике идут по темам.
			if word.Lexeme.FreqRank == 0 {
				t.Errorf("колода %q, строка %d: не проставлен порядок", deck.Deck.Code, word.Line)
			}
		}
	}
}

func TestLoadSplitsTranslationVariants(t *testing.T) {
	t.Parallel()

	decks, err := seeds.Load(seedfiles.FS)
	if err != nil {
		t.Fatalf("Load() вернул ошибку: %v", err)
	}

	// «профессия / работа» — два допустимых ответа, а не один длинный:
	// написавший «работа» ответил правильно.
	var found bool
	for _, deck := range decks {
		for _, word := range deck.Words {
			if word.Lexeme.Term != "직업" {
				continue
			}
			found = true

			if len(word.Translations) != 2 {
				t.Fatalf("переводов %d, ожидалось два: %+v", len(word.Translations), word.Translations)
			}
			if word.Translations[0].Text != "профессия" || word.Translations[1].Text != "работа" {
				t.Errorf("переводы = %q и %q", word.Translations[0].Text, word.Translations[1].Text)
			}
			if !word.Translations[0].IsPrimary {
				t.Error("первое значение должно быть основным")
			}
			if word.Translations[1].IsPrimary {
				t.Error("основное значение может быть только одно")
			}
		}
	}
	if !found {
		t.Fatal("в словаре нет слова, на котором проверяется разбор вариантов")
	}
}

func TestLoadKeepsExamples(t *testing.T) {
	t.Parallel()

	decks, err := seeds.Load(seedfiles.FS)
	if err != nil {
		t.Fatalf("Load() вернул ошибку: %v", err)
	}

	withExample := 0
	for _, deck := range decks {
		for _, word := range deck.Words {
			if word.Lexeme.Example != "" {
				withExample++
			}
		}
	}
	if withExample == 0 {
		t.Error("примеры употребления потерялись при разборе")
	}
}

func TestParseNoteInParentheses(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		"decks.yaml": file(`
			decks:
			  - code: ko-test
			    title: Проверка
			    lang: ko
			    translation_lang: ru
			    file: words.csv
		`),
		"words.csv": file(`
			Слово,Перевод,Пример
			회사,компания / офис (место работы),"회사에 컴퓨터가 있어요."
		`),
	}

	decks, err := seeds.Load(fsys)
	if err != nil {
		t.Fatalf("Load() вернул ошибку: %v", err)
	}

	translations := decks[0].Words[0].Translations
	if len(translations) != 2 {
		t.Fatalf("переводов %d, ожидалось два: %+v", len(translations), translations)
	}
	// Уточнение в скобках — примечание, а не часть ответа: печатать
	// «место работы» никто не должен.
	if translations[1].Text != "офис" {
		t.Errorf("перевод = %q, ожидалось «офис»", translations[1].Text)
	}
	if translations[1].Note != "место работы" {
		t.Errorf("примечание = %q, ожидалось «место работы»", translations[1].Note)
	}
}

func TestLoadRejectsBrokenInput(t *testing.T) {
	t.Parallel()

	manifest := `
		decks:
		  - code: ko-test
		    title: Проверка
		    lang: ko
		    translation_lang: ru
		    file: words.csv
	`

	tests := []struct {
		name  string
		files fstest.MapFS
		hint  string
	}{
		{
			name: "перепутанные колонки",
			files: fstest.MapFS{
				"decks.yaml": file(manifest),
				"words.csv": file(`
					Перевод,Слово,Пример
					имя,이름,"이름이 뭐예요?"
				`),
			},
			// Перепутанные местами слово и перевод дали бы словарь наизнанку,
			// и заметили бы это не скоро.
			hint: "колонка 1",
		},
		{
			name: "слово без перевода",
			files: fstest.MapFS{
				"decks.yaml": file(manifest),
				"words.csv": file(`
					Слово,Перевод,Пример
					이름,,"이름이 뭐예요?"
				`),
			},
			hint: "строка 2",
		},
		{
			name: "повтор слова",
			files: fstest.MapFS{
				"decks.yaml": file(manifest),
				"words.csv": file(`
					Слово,Перевод,Пример
					이름,имя,""
					이름,название,""
				`),
			},
			hint: "уже встречалось",
		},
		{
			name: "лишняя колонка",
			files: fstest.MapFS{
				"decks.yaml": file(manifest),
				"words.csv": file(`
					Слово,Перевод,Пример,Лишнее
					이름,имя,"",что-то
				`),
			},
			hint: "колонки",
		},
		{
			name: "нет файла со словами",
			files: fstest.MapFS{
				"decks.yaml": file(manifest),
			},
			hint: "words.csv",
		},
		{
			name: "язык перевода совпадает с изучаемым",
			files: fstest.MapFS{
				"decks.yaml": file(`
					decks:
					  - code: ko-test
					    title: Проверка
					    lang: ko
					    translation_lang: ko
					    file: words.csv
				`),
				"words.csv": file(`
					Слово,Перевод,Пример
					이름,имя,""
				`),
			},
			hint: "совпадает",
		},
		{
			name: "две колоды с одним слагом",
			files: fstest.MapFS{
				"decks.yaml": file(`
					decks:
					  - code: ko-test
					    title: Первая
					    lang: ko
					    translation_lang: ru
					    file: words.csv
					  - code: ko-test
					    title: Вторая
					    lang: ko
					    translation_lang: ru
					    file: words.csv
				`),
				"words.csv": file(`
					Слово,Перевод,Пример
					이름,имя,""
				`),
			},
			hint: "дважды",
		},
		{
			name:  "нет манифеста",
			files: fstest.MapFS{},
			hint:  "decks.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := seeds.Load(tt.files)
			if err == nil {
				t.Fatal("Load() не заметил ошибку в словаре")
			}
			if !strings.Contains(err.Error(), tt.hint) {
				t.Errorf("ошибка %v не подсказывает, где искать (ожидалось упоминание %q)", err, tt.hint)
			}
		})
	}
}

func TestParseSkipsEmptyLines(t *testing.T) {
	t.Parallel()

	// Пустая строка в конце — обычное дело для выгрузок из таблиц,
	// и обрывать на ней загрузку словаря незачем.
	fsys := fstest.MapFS{
		"decks.yaml": file(`
			decks:
			  - code: ko-test
			    title: Проверка
			    lang: ko
			    translation_lang: ru
			    file: words.csv
		`),
		"words.csv": file(`
			Слово,Перевод,Пример
			이름,имя,""
			,,
		`),
	}

	decks, err := seeds.Load(fsys)
	if err != nil {
		t.Fatalf("Load() вернул ошибку: %v", err)
	}
	if len(decks[0].Words) != 1 {
		t.Errorf("слов %d, ожидалось одно", len(decks[0].Words))
	}
}

// file убирает отступы, которыми файл выровнен в тесте.
func file(content string) *fstest.MapFile {
	lines := strings.Split(strings.TrimPrefix(content, "\n"), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimLeft(line, "\t")
	}
	return &fstest.MapFile{Data: []byte(strings.Join(lines, "\n"))}
}
