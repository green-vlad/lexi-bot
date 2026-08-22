package telegram

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxCallbackDataLen — предел Telegram на callback_data: 64 байта на всё.
//
// Именно байта, а не символа: «повторить» в параметре стоит 18 байт, и
// кириллица съедает бюджет вдвое быстрее латиницы. Поэтому действия
// называются короткими латинскими словами, а в параметр не кладут текст,
// который можно заменить идентификатором.
const MaxCallbackDataLen = 64

// callbackSeparator разделяет части. Двоеточие не встречается в наших
// действиях и легко читается в логах.
const callbackSeparator = ":"

// Callback — то, что уезжает в кнопку и возвращается с нажатием.
//
// Формат «действие:идентификатор:параметр» из плана (§6). Разбор идёт
// на три части, поэтому двоеточия внутри параметра допустимы: делить
// его дальше — дело того, кто его положил.
type Callback struct {
	// Action — что делать: rate, deck, lang. Обязателен и не содержит
	// двоеточий, иначе разбор поехал бы.
	Action string
	// ID — над чем действовать. Ноль означает «не относится к строке базы».
	ID int64
	// Param — уточнение: оценка, номер варианта, код языка.
	Param string
}

// Encode собирает callback_data и проверяет, что оно влезает в лимит.
//
// Проверка здесь, а не при отправке: узнать о превышении в момент, когда
// пользователь нажал кнопку и получил ошибку от Telegram, было бы поздно
// и непонятно.
func (c Callback) Encode() (string, error) {
	if c.Action == "" {
		return "", errors.New("callback_data без действия")
	}
	if strings.Contains(c.Action, callbackSeparator) {
		return "", fmt.Errorf("действие %q содержит разделитель %q", c.Action, callbackSeparator)
	}
	if !utf8.ValidString(c.Param) {
		return "", errors.New("параметр callback_data не является корректным UTF-8")
	}

	// Хвостовые части опускаются: так «rate» короче «rate:0:», а разбор
	// даёт то же самое значение.
	data := c.Action
	switch {
	case c.Param != "":
		data += callbackSeparator + strconv.FormatInt(c.ID, 10) + callbackSeparator + c.Param
	case c.ID != 0:
		data += callbackSeparator + strconv.FormatInt(c.ID, 10)
	}

	if len(data) > MaxCallbackDataLen {
		return "", fmt.Errorf("callback_data занимает %d байт при пределе %d: %q",
			len(data), MaxCallbackDataLen, data)
	}
	return data, nil
}

// String возвращает закодированный вид или описание ошибки — годится
// только для логов.
func (c Callback) String() string {
	data, err := c.Encode()
	if err != nil {
		return fmt.Sprintf("callback(%s: %v)", c.Action, err)
	}
	return data
}

// DecodeCallback разбирает callback_data.
//
// Данные приходят из внешнего мира: кнопка могла остаться от прошлой версии
// бота или быть подделана. Поэтому разбор ничему не верит на слово,
// а хендлер обязан проверить, что действие ему знакомо.
func DecodeCallback(data string) (Callback, error) {
	if data == "" {
		return Callback{}, errors.New("пустое callback_data")
	}
	if len(data) > MaxCallbackDataLen {
		return Callback{}, fmt.Errorf("callback_data длиннее %d байт", MaxCallbackDataLen)
	}

	parts := strings.SplitN(data, callbackSeparator, 3)
	c := Callback{Action: parts[0]}
	if c.Action == "" {
		return Callback{}, fmt.Errorf("callback_data %q без действия", data)
	}

	if len(parts) > 1 && parts[1] != "" {
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return Callback{}, fmt.Errorf("callback_data %q: идентификатор не разбирается: %w", data, err)
		}
		c.ID = id
	}
	if len(parts) > 2 {
		c.Param = parts[2]
	}
	return c, nil
}
