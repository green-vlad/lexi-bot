// Package i18n переводит сообщения интерфейса на язык пользователя.
//
// Обёртка над go-i18n нужна не ради абстракции ради абстракции: библиотека
// оперирует языковыми тегами BCP-47 и своими структурами конфигурации,
// а сценариям нужен ровно один вопрос — «как это сказать по-русски».
// Всё остальное остаётся здесь.
//
// Множественные формы — главная причина брать библиотеку, а не fmt.Sprintf:
// в русском их три («1 карточка», «2 карточки», «5 карточек»), в английском
// две, и выбирать их самим означало бы зашить правила языка в код.
package i18n

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/BurntSushi/toml"
	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"

	"lexi-bot/internal/domain/user"
	"lexi-bot/internal/usecase/port"
)

// Catalog хранит разобранные каталоги переводов.
type Catalog struct {
	bundle *goi18n.Bundle
	// localizers готовятся заранее: их немного, а создание на каждое
	// сообщение — лишняя работа в горячем пути обработки апдейта.
	localizers map[user.UILang]port.Localizer
}

var _ port.Catalog = (*Catalog)(nil)

// NewCatalog читает файлы переводов из файловой системы.
//
// Языком по умолчанию объявлен английский: go-i18n требует базовый язык
// для разбора файлов, а user.DefaultUILang — единственное разумное значение,
// чтобы каталог и остальное приложение сходились в этом вопросе.
func NewCatalog(fsys fs.FS) (*Catalog, error) {
	bundle := goi18n.NewBundle(mustTag(user.DefaultUILang))
	bundle.RegisterUnmarshalFunc("toml", toml.Unmarshal)

	files, err := fs.Glob(fsys, "*.toml")
	if err != nil {
		return nil, fmt.Errorf("найти файлы переводов: %w", err)
	}
	if len(files) == 0 {
		return nil, errors.New("каталог переводов пуст")
	}

	for _, name := range files {
		if _, err := bundle.LoadMessageFileFS(fsys, name); err != nil {
			return nil, fmt.Errorf("разобрать %s: %w", name, err)
		}
	}

	c := &Catalog{
		bundle:     bundle,
		localizers: make(map[user.UILang]port.Localizer, len(user.SupportedUILangs())),
	}
	for _, lang := range user.SupportedUILangs() {
		if err := c.check(lang, files); err != nil {
			return nil, err
		}
		c.localizers[lang] = c.newLocalizer(lang)
	}
	return c, nil
}

// check убеждается, что для поддерживаемого языка есть файл переводов.
// Без этого бот молча начал бы отвечать по-английски тому, кто выбрал
// русский, — и заметили бы это уже пользователи.
func (c *Catalog) check(lang user.UILang, files []string) error {
	for _, name := range files {
		if strings.TrimSuffix(path.Base(name), ".toml") == lang.String() {
			return nil
		}
	}
	return fmt.Errorf("нет файла переводов для языка %q", lang)
}

// For возвращает локализатор языка, а для неизвестного — язык по умолчанию.
func (c *Catalog) For(lang user.UILang) port.Localizer {
	if l, ok := c.localizers[lang]; ok {
		return l
	}
	return c.localizers[user.DefaultUILang]
}

func (c *Catalog) newLocalizer(lang user.UILang) port.Localizer {
	// Второй язык — запасной: если ключ есть только в английском каталоге,
	// пользователь получит английскую строку вместо ошибки. Расхождение
	// каталогов при этом не остаётся незамеченным — его ловит тест полноты.
	return &localizer{
		lang:  lang,
		inner: goi18n.NewLocalizer(c.bundle, lang.String(), user.DefaultUILang.String()),
	}
}

// localizer переводит сообщения на один язык.
type localizer struct {
	lang  user.UILang
	inner *goi18n.Localizer
}

// T переводит сообщение без числа.
func (l *localizer) T(key string, args port.Args) (string, error) {
	return l.localize(&goi18n.LocalizeConfig{
		MessageID:    key,
		TemplateData: map[string]any(args),
	})
}

// Plural переводит сообщение с числом, выбирая нужную форму.
func (l *localizer) Plural(key string, count int, args port.Args) (string, error) {
	// Count кладётся в данные шаблона всегда: без этого {{.Count}} в переводе
	// превратится в пустоту, и «карточек ждут повторения» уйдёт в чат без числа.
	data := make(map[string]any, len(args)+1)
	for k, v := range args {
		data[k] = v
	}
	data["Count"] = count

	return l.localize(&goi18n.LocalizeConfig{
		MessageID:    key,
		TemplateData: data,
		PluralCount:  count,
	})
}

// Lang возвращает язык локализатора.
func (l *localizer) Lang() user.UILang { return l.lang }

func (l *localizer) localize(cfg *goi18n.LocalizeConfig) (string, error) {
	text, err := l.inner.Localize(cfg)
	if err != nil {
		return "", fmt.Errorf("перевести %q на %s: %w", cfg.MessageID, l.lang, err)
	}
	return text, nil
}

// mustTag превращает код языка интерфейса в тег BCP-47. Набор языков
// закрыт и проверен в domain/user, поэтому разбор здесь не может провалиться.
func mustTag(lang user.UILang) language.Tag {
	return language.MustParse(lang.String())
}
