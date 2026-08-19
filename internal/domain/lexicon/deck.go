package lexicon

import "fmt"

// DeckID — идентификатор колоды в хранилище.
type DeckID int64

// Deck — набор лексем в заданном порядке: встроенный частотный список либо
// личная колода пользователя, куда попадают добавленные им слова.
//
// Колода описывает только «что учить». Направление перевода задаётся курсом
// (user_course), поэтому одна корейская колода обслуживает и перевод на русский,
// и перевод на английский.
type Deck struct {
	ID DeckID
	// Code — устойчивый слаг встроенной колоды (ko-top-2000), по которому её
	// находят сиды и миграции. У личных колод пуст.
	Code string
	// OwnerID — владелец личной колоды. Ноль означает встроенную колоду.
	OwnerID int64
	// Lang — язык изучения: язык всех лексем колоды.
	Lang        Language
	Title       string
	Description string
	// Size — число лексем в колоде. Поддерживается хранилищем; в домене нужен
	// только для показа списка колод без пересчёта.
	Size int
}

// NewBuiltinDeck создаёт встроенную колоду, общую для всех пользователей.
func NewBuiltinDeck(code string, lang Language, title, description string) (Deck, error) {
	slug, err := parseDeckCode(code)
	if err != nil {
		return Deck{}, err
	}
	t, err := requireText("title", title, MaxTitleLen)
	if err != nil {
		return Deck{}, err
	}
	d, err := cleanText("description", description, MaxDescriptionLen)
	if err != nil {
		return Deck{}, err
	}

	deck := Deck{Code: slug, Lang: lang, Title: t, Description: d}
	if err := deck.Validate(); err != nil {
		return Deck{}, err
	}
	return deck, nil
}

// NewPersonalDeck создаёт личную колоду пользователя. Слаг ей не нужен: её
// находят по владельцу и языку изучения.
func NewPersonalDeck(ownerID int64, lang Language, title string) (Deck, error) {
	t, err := requireText("title", title, MaxTitleLen)
	if err != nil {
		return Deck{}, err
	}

	deck := Deck{OwnerID: ownerID, Lang: lang, Title: t}
	if err := deck.Validate(); err != nil {
		return Deck{}, err
	}
	return deck, nil
}

// Validate проверяет инварианты колоды, ничего не изменяя.
func (d Deck) Validate() error {
	if d.Lang.IsZero() {
		return fmt.Errorf("lang_code: %w", ErrRequired)
	}
	if d.Title == "" {
		return fmt.Errorf("title: %w", ErrRequired)
	}
	if d.Size < 0 {
		return fmt.Errorf("size: %w (ожидалось неотрицательное число)", ErrInvalid)
	}
	if d.OwnerID < 0 {
		return fmt.Errorf("owner_user_id: %w (ожидался неотрицательный идентификатор)", ErrInvalid)
	}

	if d.IsBuiltin() {
		if d.Code == "" {
			return fmt.Errorf("code: %w (у встроенной колоды обязателен слаг)", ErrRequired)
		}
		if _, err := parseDeckCode(d.Code); err != nil {
			return err
		}
		return nil
	}
	if d.Code != "" {
		return fmt.Errorf("code: %w (слаг бывает только у встроенной колоды)", ErrInvalid)
	}
	return nil
}

// IsBuiltin сообщает, что колода встроенная и доступна всем пользователям.
func (d Deck) IsBuiltin() bool { return d.OwnerID == 0 }

// parseDeckCode проверяет слаг: строчная латиница, цифры и дефисы между ними.
// Слаг попадает в callback_data кнопок, где на всё сообщение отведено 64 байта,
// поэтому он должен оставаться коротким и однобайтовым.
func parseDeckCode(code string) (string, error) {
	slug, err := requireText("code", code, MaxDeckCodeLen)
	if err != nil {
		return "", err
	}
	slug = lowerASCII(slug)

	prevHyphen := true // строка не может начинаться с дефиса
	for i := 0; i < len(slug); i++ {
		c := slug[i]
		switch {
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'):
			prevHyphen = false
		case c == '-':
			if prevHyphen {
				return "", fmt.Errorf("code %q: %w (дефис не на своём месте)", code, ErrInvalid)
			}
			prevHyphen = true
		default:
			return "", fmt.Errorf("code %q: %w (допустимы латиница, цифры и дефис)", code, ErrInvalid)
		}
	}
	if prevHyphen {
		return "", fmt.Errorf("code %q: %w (дефис не на своём месте)", code, ErrInvalid)
	}
	return slug, nil
}

// DeckItem — место лексемы в колоде. Порядок задаёт очередь изучения: у
// встроенных колод он повторяет частотность, у личной — порядок добавления.
type DeckItem struct {
	DeckID   DeckID
	LexemeID LexemeID
	Position int
}

// NewDeckItem создаёт элемент колоды. Идентификаторы обязательны: элемент
// связывает уже сохранённые колоду и лексему.
func NewDeckItem(deckID DeckID, lexemeID LexemeID, position int) (DeckItem, error) {
	item := DeckItem{DeckID: deckID, LexemeID: lexemeID, Position: position}
	if err := item.Validate(); err != nil {
		return DeckItem{}, err
	}
	return item, nil
}

// Validate проверяет инварианты элемента колоды.
func (i DeckItem) Validate() error {
	if i.DeckID <= 0 {
		return fmt.Errorf("deck_id: %w", ErrRequired)
	}
	if i.LexemeID <= 0 {
		return fmt.Errorf("lexeme_id: %w", ErrRequired)
	}
	if i.Position < 0 {
		return fmt.Errorf("position: %w (ожидалось неотрицательное число)", ErrInvalid)
	}
	return nil
}
