package lexicon

import "fmt"

// TranslationID — идентификатор перевода в хранилище.
type TranslationID int64

// Translation — одно допустимое значение лексемы на языке перевода.
//
// Переводов у лексемы может быть несколько на один язык, и это не избыточность,
// а необходимость: в режиме ввода текстом засчитывается любой из них, поэтому
// «дом», «здание» и «жилище» хранятся отдельными строками, а не через запятую.
type Translation struct {
	ID       TranslationID
	LexemeID LexemeID
	Lang     Language
	Text     string
	// IsPrimary отмечает основное значение: его показывают в карточке и в
	// вариантах ответа, остальные принимаются, но не показываются.
	IsPrimary bool
	// Note — уточнение вроде «разг.» или «только о людях».
	Note string
}

// TranslationParams — входные данные конструктора.
type TranslationParams struct {
	LexemeID  LexemeID
	Lang      Language
	Text      string
	IsPrimary bool
	Note      string
}

// NewTranslation нормализует текст перевода и примечание и проверяет поля.
func NewTranslation(p TranslationParams) (Translation, error) {
	text, err := requireText("text", p.Text, MaxTranslationLen)
	if err != nil {
		return Translation{}, err
	}
	note, err := cleanText("note", p.Note, MaxNoteLen)
	if err != nil {
		return Translation{}, err
	}

	tr := Translation{
		LexemeID:  p.LexemeID,
		Lang:      p.Lang,
		Text:      text,
		IsPrimary: p.IsPrimary,
		Note:      note,
	}
	if err := tr.Validate(); err != nil {
		return Translation{}, err
	}
	return tr, nil
}

// Validate проверяет инварианты перевода, ничего не изменяя.
func (t Translation) Validate() error {
	if t.LexemeID <= 0 {
		return fmt.Errorf("lexeme_id: %w", ErrRequired)
	}
	if t.Lang.IsZero() {
		return fmt.Errorf("lang_code: %w", ErrRequired)
	}
	if t.Text == "" {
		return fmt.Errorf("text: %w", ErrRequired)
	}
	return nil
}
