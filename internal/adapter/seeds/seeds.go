// Package seeds читает встроенные словари.
//
// Формат выбран под то, в каком виде словари существуют: колода — это
// CSV-файл со словом, переводом и примером употребления, а описания колод
// собраны в одном манифесте. План (§3) предлагал JSONL, но словари правят
// руками и в таблицах, а не программой, и CSV для этого удобнее — его
// открывает любой редактор таблиц, и переводчику не нужно следить
// за кавычками и скобками JSON.
//
// Разбор ничего не сохраняет: он превращает файлы в доменные сущности
// и объясняет, что не так, указывая файл и номер строки. Загрузкой
// занимается сидер (T-036).
package seeds

import (
	"fmt"
	"io/fs"
	"path"

	"gopkg.in/yaml.v3"

	"lexi-bot/internal/domain/lexicon"
)

// ManifestName — файл с описанием колод.
const ManifestName = "decks.yaml"

// Ограничения на размер словаря. Они не про технику, а про здравый смысл:
// колода на десять тысяч слов — это не колода, а свалка, и учить её никто
// не станет.
const (
	MaxDeckSize = 5000
	// MaxTranslations — сколько значений допускается у одного слова.
	// Больше пяти — признак того, что в перевод записали толкование.
	MaxTranslations = 5
)

// Deck — колода вместе со словами.
type Deck struct {
	Deck  lexicon.Deck
	Words []Word
	// TranslationLang — язык, на котором записаны переводы в файле.
	TranslationLang lexicon.Language
}

// Word — слово с переводами.
type Word struct {
	Lexeme       lexicon.Lexeme
	Translations []lexicon.Translation
	// Line — номер строки в файле: по нему человек найдёт ошибку глазами.
	Line int
}

// manifest — то, что лежит в decks.yaml.
type manifest struct {
	Decks []manifestDeck `yaml:"decks"`
}

type manifestDeck struct {
	Code            string `yaml:"code"`
	Title           string `yaml:"title"`
	Description     string `yaml:"description"`
	Lang            string `yaml:"lang"`
	TranslationLang string `yaml:"translation_lang"`
	File            string `yaml:"file"`
}

// Load читает манифест и все колоды, на которые он ссылается.
func Load(fsys fs.FS) ([]Deck, error) {
	raw, err := fs.ReadFile(fsys, ManifestName)
	if err != nil {
		return nil, fmt.Errorf("прочитать %s: %w", ManifestName, err)
	}

	var m manifest
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("разобрать %s: %w", ManifestName, err)
	}
	if len(m.Decks) == 0 {
		return nil, fmt.Errorf("%s: не описано ни одной колоды", ManifestName)
	}

	seen := make(map[string]bool, len(m.Decks))
	decks := make([]Deck, 0, len(m.Decks))
	for i := range m.Decks {
		described := &m.Decks[i]
		if seen[described.Code] {
			return nil, fmt.Errorf("%s: колода %q описана дважды", ManifestName, described.Code)
		}
		seen[described.Code] = true

		deck, err := loadDeck(fsys, described)
		if err != nil {
			return nil, err
		}
		decks = append(decks, deck)
	}
	return decks, nil
}

// loadDeck читает одну колоду.
func loadDeck(fsys fs.FS, described *manifestDeck) (Deck, error) {
	lang, err := lexicon.ParseLanguage(described.Lang)
	if err != nil {
		return Deck{}, fmt.Errorf("колода %q: язык изучения: %w", described.Code, err)
	}
	translationLang, err := lexicon.ParseLanguage(described.TranslationLang)
	if err != nil {
		return Deck{}, fmt.Errorf("колода %q: язык перевода: %w", described.Code, err)
	}
	if lang == translationLang {
		return Deck{}, fmt.Errorf("колода %q: язык перевода совпадает с языком изучения (%s)",
			described.Code, lang)
	}

	meta, err := lexicon.NewBuiltinDeck(described.Code, lang, described.Title, described.Description)
	if err != nil {
		return Deck{}, fmt.Errorf("колода %q: %w", described.Code, err)
	}
	if described.File == "" {
		return Deck{}, fmt.Errorf("колода %q: не указан файл со словами", described.Code)
	}

	file, err := fsys.Open(path.Clean(described.File))
	if err != nil {
		return Deck{}, fmt.Errorf("колода %q: открыть %s: %w", described.Code, described.File, err)
	}
	defer func() { _ = file.Close() }()

	words, err := parseWords(file, described.File, lang, translationLang)
	if err != nil {
		return Deck{}, fmt.Errorf("колода %q: %w", described.Code, err)
	}

	meta.Size = len(words)
	return Deck{Deck: meta, Words: words, TranslationLang: translationLang}, nil
}
