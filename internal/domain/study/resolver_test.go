package study_test

import (
	"errors"
	"testing"
	"time"

	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/domain/study"
)

func TestResolveRecall(t *testing.T) {
	t.Parallel()

	// В режиме «помню / не помню» оценку выбирает сам пользователь,
	// и резолвер обязан пропустить её без изменений.
	resolver := study.DefaultRatingResolver()

	for _, rating := range study.Ratings() {
		got, err := resolver.Resolve(study.Answer{Mode: study.ModeRecall, SelfRating: rating})
		if err != nil {
			t.Fatalf("Resolve() вернул ошибку: %v", err)
		}
		if got != rating {
			t.Errorf("Resolve() = %v, ожидалось %v", got, rating)
		}
	}

	// Даже мгновенный ответ не превращается в «легко»: пользователь уже сказал,
	// как ему было.
	got, err := resolver.Resolve(study.Answer{
		Mode: study.ModeRecall, SelfRating: study.RatingGood, Elapsed: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Resolve() вернул ошибку: %v", err)
	}
	if got != study.RatingGood {
		t.Errorf("Resolve() = %v, ожидалось good", got)
	}
}

func TestResolveChoice(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		correct bool
		elapsed time.Duration
		want    study.Rating
	}{
		{"верно и быстро", true, time.Second, study.RatingEasy},
		{"верно ровно на границе быстроты", true, study.DefaultFastAnswer, study.RatingGood},
		{"верно, но медленно", true, 10 * time.Second, study.RatingGood},
		{"верно, длительность не измерялась", true, 0, study.RatingGood},
		{"неверно", false, time.Second, study.RatingAgain},
		{"неверно и медленно", false, time.Minute, study.RatingAgain},
	}

	resolver := study.DefaultRatingResolver()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := resolver.Resolve(study.Answer{
				Mode: study.ModeChoice, Correct: tt.correct, Elapsed: tt.elapsed,
			})
			if err != nil {
				t.Fatalf("Resolve() вернул ошибку: %v", err)
			}
			if got != tt.want {
				t.Errorf("Resolve() = %v, ожидалось %v", got, tt.want)
			}
		})
	}
}

func TestResolveTyping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		match lexicon.Match
		want  study.Rating
	}{
		{"точное совпадение", lexicon.MatchExact, study.RatingGood},
		{"совпало после нормализации", lexicon.MatchNormalized, study.RatingHard},
		{"опечатка", lexicon.MatchTypo, study.RatingHard},
		{"не совпало", lexicon.MatchNone, study.RatingAgain},
	}

	resolver := study.DefaultRatingResolver()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := resolver.Resolve(study.Answer{Mode: study.ModeTyping, Match: tt.match})
			if err != nil {
				t.Fatalf("Resolve() вернул ошибку: %v", err)
			}
			if got != tt.want {
				t.Errorf("Resolve() = %v, ожидалось %v", got, tt.want)
			}
		})
	}

	// Скорость в этом режиме ни на что не влияет: печатать быстрее, чем
	// вспоминать, невозможно, и «легко» здесь не выдаётся никогда.
	fast, err := resolver.Resolve(study.Answer{
		Mode: study.ModeTyping, Match: lexicon.MatchExact, Elapsed: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Resolve() вернул ошибку: %v", err)
	}
	if fast != study.RatingGood {
		t.Errorf("Resolve() = %v, ожидалось good", fast)
	}
}

// langEN — язык перевода для сквозной проверки с lexicon.CheckAnswer.
var langEN = lexicon.MustParseLanguage("en")

func TestResolveFromRealAnswerCheck(t *testing.T) {
	t.Parallel()

	// Сквозная проверка: результат lexicon.CheckAnswer ложится в резолвер
	// без переходников, ради чего степени совпадения и заведены.
	accepted := lexicon.AcceptedAnswers([]lexicon.Translation{
		{LexemeID: 1, Lang: langEN, Text: "house; building", IsPrimary: true},
	})

	tests := []struct {
		answer string
		want   study.Rating
	}{
		{"house", study.RatingGood},
		{"House ", study.RatingGood},
		{"building", study.RatingGood},
		{"the house", study.RatingHard},
		{"hous", study.RatingHard},
		{"garden", study.RatingAgain},
		{"", study.RatingAgain},
	}

	resolver := study.DefaultRatingResolver()
	for _, tt := range tests {
		check := lexicon.CheckAnswer(tt.answer, accepted, langEN)

		got, err := resolver.Resolve(study.Answer{Mode: study.ModeTyping, Match: check.Match})
		if err != nil {
			t.Fatalf("Resolve() вернул ошибку: %v", err)
		}
		if got != tt.want {
			t.Errorf("ответ %q: оценка %v, ожидалось %v (совпадение %v)", tt.answer, got, tt.want, check.Match)
		}
	}
}

func TestResolveErrors(t *testing.T) {
	t.Parallel()

	resolver := study.DefaultRatingResolver()

	tests := []struct {
		name   string
		answer study.Answer
		want   error
	}{
		{"без режима", study.Answer{}, study.ErrInvalid},
		{"неизвестный режим", study.Answer{Mode: study.Mode("dictation")}, study.ErrInvalid},
		{"recall без оценки", study.Answer{Mode: study.ModeRecall}, study.ErrRequired},
		{"recall с неизвестной оценкой", study.Answer{Mode: study.ModeRecall, SelfRating: study.Rating(9)}, study.ErrRequired},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := resolver.Resolve(tt.answer)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Resolve() = %v, ожидалась ошибка %v", err, tt.want)
			}
			if got != 0 {
				t.Errorf("при ошибке возвращена оценка %v", got)
			}
		})
	}
}

func TestNewRatingResolver(t *testing.T) {
	t.Parallel()

	// Граница быстрого ответа настраивается: с секундой ответ за две секунды
	// быстрым уже не считается.
	resolver, err := study.NewRatingResolver(time.Second)
	if err != nil {
		t.Fatalf("NewRatingResolver() вернул ошибку: %v", err)
	}

	got, err := resolver.Resolve(study.Answer{Mode: study.ModeChoice, Correct: true, Elapsed: 2 * time.Second})
	if err != nil {
		t.Fatalf("Resolve() вернул ошибку: %v", err)
	}
	if got != study.RatingGood {
		t.Errorf("Resolve() = %v, ожидалось good", got)
	}

	for _, bad := range []time.Duration{0, -time.Second} {
		if _, err := study.NewRatingResolver(bad); !errors.Is(err, study.ErrOutOfRange) {
			t.Errorf("NewRatingResolver(%v) = %v, ожидалась ошибка ErrOutOfRange", bad, err)
		}
	}
}

func TestZeroResolverStillWorks(t *testing.T) {
	t.Parallel()

	// Резолвер, созданный литералом, не должен считать быстрым любой ответ.
	var resolver study.RatingResolver

	got, err := resolver.Resolve(study.Answer{Mode: study.ModeChoice, Correct: true, Elapsed: time.Minute})
	if err != nil {
		t.Fatalf("Resolve() вернул ошибку: %v", err)
	}
	if got != study.RatingGood {
		t.Errorf("Resolve() = %v, ожидалось good", got)
	}
}

func TestAnswerIsCorrect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		answer study.Answer
		want   bool
	}{
		{"recall: вспомнил", study.Answer{Mode: study.ModeRecall, SelfRating: study.RatingHard}, true},
		{"recall: не вспомнил", study.Answer{Mode: study.ModeRecall, SelfRating: study.RatingAgain}, false},
		{"recall: оценки нет", study.Answer{Mode: study.ModeRecall}, false},
		{"choice: верно", study.Answer{Mode: study.ModeChoice, Correct: true}, true},
		{"choice: неверно", study.Answer{Mode: study.ModeChoice}, false},
		{"typing: точно", study.Answer{Mode: study.ModeTyping, Match: lexicon.MatchExact}, true},
		{"typing: опечатка засчитана", study.Answer{Mode: study.ModeTyping, Match: lexicon.MatchTypo}, true},
		{"typing: промах", study.Answer{Mode: study.ModeTyping, Match: lexicon.MatchNone}, false},
		{"неизвестный режим", study.Answer{Mode: study.Mode("dictation"), Correct: true}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.answer.IsCorrect(); got != tt.want {
				t.Errorf("IsCorrect() = %t, ожидалось %t", got, tt.want)
			}
		})
	}
}
