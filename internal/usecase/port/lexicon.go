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

// DistractorQuery — запрос ложных вариантов для режима выбора.
type DistractorQuery struct {
	DeckID lexicon.DeckID
	// Lang — язык перевода: варианты показываются на нём же, что и ответ.
	Lang lexicon.Language
	// POS — часть речи правильного ответа. Совпадение желательно, но
	// не обязательно: выбор между четырьмя существительными сложнее
	// и честнее, чем между существительным и тремя глаголами, где
	// правильный вариант виден по форме слова.
	POS lexicon.PartOfSpeech
	// Exclude — слово, для которого подбираются варианты: его собственный
	// перевод ложным быть не может.
	Exclude lexicon.LexemeID
	Limit   int
}

// DeckRepo хранит колоды и их состав.
type DeckRepo interface {
	// Distractors возвращает переводы других слов колоды — из них
	// собираются ложные варианты ответа при проверке на узнавание.
	Distractors(ctx context.Context, q DistractorQuery) ([]lexicon.Translation, error)

	// DistractorTerms возвращает сами слова колоды: они нужны, когда
	// спрашивают в обратную сторону и выбирать надо из слов изучаемого
	// языка, а не из переводов.
	DistractorTerms(ctx context.Context, q DistractorQuery) ([]lexicon.Lexeme, error)

	// Languages возвращает языки, для которых есть встроенные колоды:
	// из них состоит первый вопрос онбординга. Спрашивать по справочнику
	// языков нельзя — там есть языки, учить которые пока нечем.
	Languages(ctx context.Context) ([]lexicon.Language, error)

	// TranslationLanguages возвращает языки, на которые переведены слова
	// колоды. Предлагать язык, перевода на который нет, значило бы завести
	// пользователю курс из пустых карточек.
	TranslationLanguages(ctx context.Context, deckID lexicon.DeckID) ([]lexicon.Language, error)

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
