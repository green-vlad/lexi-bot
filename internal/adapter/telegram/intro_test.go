package telegram_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"lexi-bot/internal/domain/study"
)

func TestMenuOffersBothOccupations(t *testing.T) {
	t.Parallel()

	f := newLearnFixture(t, 4)
	// Два слова человек уже начал учить, остальные два — новые.
	f.startedWords(t, 2)

	f.send(t, "/learn")

	text, buttons := f.screen(t)
	if !strings.Contains(text, "Чем займёмся") {
		t.Errorf("меню = %q", text)
	}
	if len(buttons) != 2 {
		t.Fatalf("кнопок %d, ожидались повторение и новые слова: %v", len(buttons), buttons)
	}
	if !strings.HasPrefix(buttons[0], "rev:") || !strings.HasPrefix(buttons[1], "new:") {
		t.Errorf("кнопки = %v, ожидались повторение и знакомство", buttons)
	}
}

func TestMenuHidesWhatIsNotThere(t *testing.T) {
	t.Parallel()

	// Ничего не начато: повторять нечего, и обещать это кнопкой нельзя.
	f := newLearnFixture(t, 4)

	f.send(t, "/learn")
	_, buttons := f.screen(t)
	if len(buttons) != 1 || !strings.HasPrefix(buttons[0], "new:") {
		t.Fatalf("кнопки = %v, ожидалось только знакомство", buttons)
	}

	// А когда кончилось и то и другое, кнопок нет вовсе.
	empty := newLearnFixture(t, 0)
	empty.send(t, "/learn")
	text, buttons := empty.screen(t)
	if len(buttons) != 0 {
		t.Errorf("кнопки = %v, ожидалось пустое меню", buttons)
	}
	if !strings.Contains(strings.ToLower(text), "сегодня") {
		t.Errorf("меню = %q, ожидалось «на сегодня всё»", text)
	}
}

func TestIntroShowsWordWithThreeDecisions(t *testing.T) {
	t.Parallel()

	f := newLearnFixture(t, 4)

	f.words(t)

	text, buttons := f.screen(t)
	// Слово показано целиком: написание, чтение, перевод и пример.
	for _, want := range []string{"집", "чтение", "дом", "пример"} {
		if !strings.Contains(text, want) {
			t.Errorf("карточка = %q, в ней нет %q", text, want)
		}
	}
	if len(buttons) != 3 {
		t.Fatalf("кнопок %d, ожидались три решения: %v", len(buttons), buttons)
	}
	for i, prefix := range []string{"rem:", "kno:", "skp:"} {
		if !strings.HasPrefix(buttons[i], prefix) {
			t.Errorf("кнопка %d = %q, ожидалась %q", i, buttons[i], prefix)
		}
	}
}

func TestIntroRememberSendsWordToRepetition(t *testing.T) {
	t.Parallel()

	f := newLearnFixture(t, 4)

	f.words(t)
	_, buttons := f.screen(t)
	f.press(t, buttons[0])

	// Показано следующее слово.
	text, _ := f.screen(t)
	if !strings.Contains(text, "개") {
		t.Errorf("после «запомнил» %q, ожидалось следующее слово", text)
	}

	card, ok := f.cards.byLexeme(1)
	if !ok {
		t.Fatal("карточка не заведена")
	}
	if card.State != study.StateLearning {
		t.Fatalf("фаза = %v, ожидалось обучение", card.State)
	}

	// И оно действительно возвращается повторением, когда подойдёт срок.
	f.now = card.DueAt.Add(time.Minute)
	f.review(t)
	text, _ = f.screen(t)
	if !strings.Contains(text, "집") {
		t.Errorf("повторение = %q, ожидалось начатое слово", text)
	}
}

func TestIntroAlreadyKnownWordNeverComesBack(t *testing.T) {
	t.Parallel()

	f := newLearnFixture(t, 4)

	f.words(t)
	_, buttons := f.screen(t)
	f.press(t, buttons[1])

	card, ok := f.cards.byLexeme(1)
	if !ok {
		t.Fatal("карточка не заведена")
	}
	if card.State != study.StateKnown {
		t.Errorf("фаза = %v, ожидалось «уже знаю»", card.State)
	}

	// Ни в знакомстве, ни в повторениях слова больше нет.
	text, _ := f.screen(t)
	if strings.Contains(text, "집") {
		t.Errorf("знакомство = %q: слово показано снова", text)
	}

	f.now = f.now.AddDate(0, 0, 30)
	due, err := f.cards.CountDue(context.Background(), card.CourseID, f.now)
	if err != nil {
		t.Fatalf("CountDue() вернул ошибку: %v", err)
	}
	if due != 0 {
		t.Errorf("к повторению %d карточек, ожидался ноль", due)
	}
}

func TestIntroSkippedWordWaitsUntilTomorrow(t *testing.T) {
	t.Parallel()

	f := newLearnFixture(t, 4)

	f.words(t)
	_, buttons := f.screen(t)
	f.press(t, buttons[2])

	// Сегодня его заменило следующее слово.
	text, _ := f.screen(t)
	if strings.Contains(text, "집") {
		t.Errorf("знакомство = %q: пропущенное слово показано снова", text)
	}
	if !strings.Contains(text, "개") {
		t.Errorf("знакомство = %q, ожидалось следующее слово", text)
	}

	card, ok := f.cards.byLexeme(1)
	if !ok {
		t.Fatal("карточка отложенного слова не заведена")
	}
	if card.State != study.StateNew {
		t.Errorf("фаза = %v, ожидалось ожидание знакомства", card.State)
	}
	if !card.DueAt.After(f.now) {
		t.Errorf("срок возврата = %v, ожидались следующие сутки", card.DueAt)
	}
}

func TestIntroStopsOnDailyQuota(t *testing.T) {
	t.Parallel()

	f := newLearnFixture(t, 4)
	f.quota(t, 1)

	f.words(t)
	_, buttons := f.screen(t)
	f.press(t, buttons[0])

	// Норма — одно слово в день: второго не будет, и вместо карточки
	// человек получает объяснение и дорогу назад в меню.
	text, buttons := f.screen(t)
	if !strings.Contains(text, "норма") {
		t.Errorf("экран = %q, ожидалось объяснение про дневную норму", text)
	}
	if len(buttons) != 1 || !strings.HasPrefix(buttons[0], "menu:") {
		t.Errorf("кнопки = %v, ожидался возврат в меню", buttons)
	}

	// Кнопка ведёт обратно в меню, и знакомства там уже нет.
	f.press(t, buttons[0])
	_, buttons = f.screen(t)
	for _, data := range buttons {
		if strings.HasPrefix(data, "new:") {
			t.Errorf("меню = %v: норма выбрана, а знакомство предлагается", buttons)
		}
	}
}

func TestIntroFinishesWhenDeckIsDone(t *testing.T) {
	t.Parallel()

	f := newLearnFixture(t, 1)

	f.words(t)
	_, buttons := f.screen(t)
	f.press(t, buttons[0])

	text, buttons := f.screen(t)
	if !strings.Contains(text, "колоде") {
		t.Errorf("экран = %q, ожидалось объяснение про конец колоды", text)
	}
	if len(buttons) != 1 || !strings.HasPrefix(buttons[0], "menu:") {
		t.Errorf("кнопки = %v, ожидался возврат в меню", buttons)
	}
}
