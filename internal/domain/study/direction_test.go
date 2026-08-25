package study_test

import (
	"errors"
	"testing"

	"lexi-bot/internal/domain/study"
)

func TestDirectionRoundTrip(t *testing.T) {
	t.Parallel()

	for _, direction := range study.Directions() {
		back, err := study.ParseDirection(direction.String())
		if err != nil {
			t.Fatalf("ParseDirection(%q) вернул ошибку: %v", direction, err)
		}
		if back != direction {
			t.Errorf("ParseDirection(%q) = %v", direction, back)
		}
		if !direction.IsValid() {
			t.Errorf("направление %v должно быть допустимым", direction)
		}
	}

	if _, err := study.ParseDirection(""); !errors.Is(err, study.ErrRequired) {
		t.Error("пустая строка должна давать ErrRequired")
	}
	if _, err := study.ParseDirection("сюда-туда"); !errors.Is(err, study.ErrInvalid) {
		t.Error("неизвестное направление должно давать ErrInvalid")
	}
}

func TestDirectionAllowsTyping(t *testing.T) {
	t.Parallel()

	// Печатать перевод на родной язык бессмысленно: у слова несколько
	// равноправных значений, и любой свободный ввод превращается
	// в угадывание того, какое из них мы сочли основным.
	if study.DirectionRecognize.AllowsTyping() {
		t.Error("в сторону родного языка ввод текстом не годится")
	}
	if !study.DirectionProduce.AllowsTyping() {
		t.Error("в сторону изучаемого языка ввод текстом — самая честная проверка")
	}
}

func TestDirectionFiltersModes(t *testing.T) {
	t.Parallel()

	all := study.Modes()

	recognize := study.DirectionRecognize.Modes(all)
	if len(recognize) != len(all)-1 {
		t.Fatalf("режимов %d, ожидалось на один меньше: %v", len(recognize), recognize)
	}
	for _, mode := range recognize {
		if mode == study.ModeTyping {
			t.Error("ввод текстом остался среди режимов узнавания")
		}
	}

	produce := study.DirectionProduce.Modes(all)
	if len(produce) != len(all) {
		t.Errorf("режимов %d, ожидались все: %v", len(produce), produce)
	}

	// Выбор из вариантов остаётся в обоих направлениях: это единственный
	// режим, которым можно спросить слово в любую сторону.
	for _, direction := range study.Directions() {
		var hasChoice bool
		for _, mode := range direction.Modes(all) {
			if mode == study.ModeChoice {
				hasChoice = true
			}
		}
		if !hasChoice {
			t.Errorf("направление %v потеряло выбор из вариантов", direction)
		}
	}
}
