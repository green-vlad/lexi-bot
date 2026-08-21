package port

import (
	"context"

	"lexi-bot/internal/domain/lexicon"
)

// LexemeRepo хранит слова и их переводы.
type LexemeRepo interface {
	// Upsert вставляет или обновляет лексемы пачкой и возвращает их
	// с присвоенными идентификаторами. Пачкой, а не по одной: сидер грузит
	// тысячи слов, и тысяча запросов вместо одного превратила бы загрузку
	// словаря в минуты.
	Upsert(ctx context.Context, lexemes []lexicon.Lexeme) ([]lexicon.Lexeme, error)

	// ByTerm ищет слово по языку и написанию среди встроенных (ownerID = 0)
	// или личных слов пользователя. Нужен, чтобы при добавлении своего слова
	// предложить встроенное вместо копии.
	ByTerm(ctx context.Context, lang lexicon.Language, term string, ownerID int64) (lexicon.Lexeme, error)

	// ByIDs возвращает лексемы пачкой: очередь карточек превращается
	// в слова одним запросом, а не запросом на карточку.
	ByIDs(ctx context.Context, ids []lexicon.LexemeID) ([]lexicon.Lexeme, error)

	// SaveTranslations сохраняет переводы пачкой.
	SaveTranslations(ctx context.Context, translations []lexicon.Translation) error

	// Translations возвращает переводы на нужный язык, разложенные по
	// лексемам. Их у слова может быть несколько, и в режиме ввода текстом
	// засчитывается любой.
	Translations(ctx context.Context, ids []lexicon.LexemeID, lang lexicon.Language) (map[lexicon.LexemeID][]lexicon.Translation, error)
}

// DeckRepo хранит колоды и их состав.
type DeckRepo interface {
	// Builtin возвращает встроенные колоды языка изучения.
	Builtin(ctx context.Context, lang lexicon.Language) ([]lexicon.Deck, error)

	// ByID возвращает колоду по идентификатору.
	ByID(ctx context.Context, id lexicon.DeckID) (lexicon.Deck, error)

	// ByCode возвращает встроенную колоду по слагу — так на неё ссылаются
	// сиды и миграции.
	ByCode(ctx context.Context, code string) (lexicon.Deck, error)

	// EnsurePersonal возвращает личную колоду пользователя для языка
	// изучения, создавая её при первом добавлении своего слова.
	EnsurePersonal(ctx context.Context, ownerID int64, lang lexicon.Language, title string) (lexicon.Deck, error)

	// AddItems добавляет слова в колоду.
	AddItems(ctx context.Context, items []lexicon.DeckItem) error

	// Items возвращает состав колоды по возрастанию position — в этом
	// порядке слова и вводятся в курс.
	Items(ctx context.Context, deckID lexicon.DeckID, offset, limit int) ([]lexicon.DeckItem, error)
}
