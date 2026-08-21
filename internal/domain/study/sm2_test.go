package study_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"lexi-bot/internal/domain/study"
)

// newScheduler возвращает планировщик с параметрами по умолчанию и без
// джиттера: интервалы получаются точными, и их можно записать в таблицу.
func newScheduler(t *testing.T) study.SM2 {
	t.Helper()

	s, err := study.NewSM2(study.DefaultSM2Config(), nil)
	if err != nil {
		t.Fatalf("NewSM2() вернул ошибку: %v", err)
	}
	return s
}

// closeEnough сравнивает интервалы в сутках с допуском на арифметику с плавающей
// точкой: 0.1 + 0.2 != 0.3 и в планировщике тоже.
func closeEnough(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

func days(d float64) time.Duration { return time.Duration(d * float64(24*time.Hour)) }

// reviewState — выученная карточка с интервалом в 10 суток: удобная точка
// отсчёта для проверки формулы SM-2.
func reviewState() study.CardState {
	return study.CardState{
		State:        study.StateReview,
		DueAt:        now,
		IntervalDays: 10,
		EaseFactor:   2.5,
		Repetitions:  3,
	}
}

func TestSM2Transitions(t *testing.T) {
	t.Parallel()

	learning := func(step int) study.CardState {
		return study.CardState{
			State:      study.StateLearning,
			DueAt:      now,
			EaseFactor: study.DefaultEaseFactor,
			LearnStep:  step,
		}
	}
	relearning := study.CardState{
		State:        study.StateRelearning,
		DueAt:        now,
		IntervalDays: 5,
		EaseFactor:   2.18,
		Lapses:       1,
	}

	tests := []struct {
		name         string
		cur          study.CardState
		rating       study.Rating
		wantState    study.State
		wantDue      time.Duration
		wantInterval float64
		wantEase     float64
		wantReps     int
		wantLapses   int
		wantStep     int
	}{
		{
			name: "новая + again — первый шаг обучения",
			cur:  study.NewCardState(now), rating: study.RatingAgain,
			wantState: study.StateLearning, wantDue: time.Minute,
			wantInterval: 0, wantEase: 2.5,
		},
		{
			name: "новая + hard — тот же шаг",
			cur:  study.NewCardState(now), rating: study.RatingHard,
			wantState: study.StateLearning, wantDue: time.Minute,
			wantInterval: 0, wantEase: 2.5,
		},
		{
			name: "новая + good — следующий шаг",
			cur:  study.NewCardState(now), rating: study.RatingGood,
			wantState: study.StateLearning, wantDue: 10 * time.Minute,
			wantInterval: 0, wantEase: 2.5, wantStep: 1,
		},
		{
			name: "новая + easy — сразу в повторение на четыре дня",
			cur:  study.NewCardState(now), rating: study.RatingEasy,
			wantState: study.StateReview, wantDue: days(4),
			wantInterval: 4, wantEase: 2.5, wantReps: 1,
		},
		{
			name: "последний шаг + good — выпуск через сутки",
			cur:  learning(1), rating: study.RatingGood,
			wantState: study.StateReview, wantDue: days(1),
			wantInterval: 1, wantEase: 2.5, wantReps: 1,
		},
		{
			name: "шаг обучения + hard — повтор того же шага",
			cur:  learning(1), rating: study.RatingHard,
			wantState: study.StateLearning, wantDue: 10 * time.Minute,
			wantInterval: 0, wantEase: 2.5, wantStep: 1,
		},
		{
			name: "шаг обучения + again — откат на первый шаг",
			cur:  learning(1), rating: study.RatingAgain,
			wantState: study.StateLearning, wantDue: time.Minute,
			wantInterval: 0, wantEase: 2.5,
		},
		{
			name: "повторение + good — интервал растёт на ease",
			cur:  reviewState(), rating: study.RatingGood,
			wantState: study.StateReview, wantDue: days(25),
			wantInterval: 25, wantEase: 2.5, wantReps: 4,
		},
		{
			name: "повторение + hard — интервал растёт медленно, ease падает",
			cur:  reviewState(), rating: study.RatingHard,
			wantState: study.StateReview, wantDue: days(12),
			wantInterval: 12, wantEase: 2.36, wantReps: 4,
		},
		{
			name: "повторение + easy — интервал растёт с бонусом, ease растёт",
			cur:  reviewState(), rating: study.RatingEasy,
			wantState: study.StateReview, wantDue: days(33.8),
			wantInterval: 33.8, wantEase: 2.6, wantReps: 4,
		},
		{
			name: "повторение + again — провал в переобучение",
			cur:  reviewState(), rating: study.RatingAgain,
			wantState: study.StateRelearning, wantDue: 10 * time.Minute,
			wantInterval: 5, wantEase: 2.18, wantReps: 0, wantLapses: 1,
		},
		{
			name: "переобучение + good — возврат с уполовиненным интервалом",
			cur:  relearning, rating: study.RatingGood,
			wantState: study.StateReview, wantDue: days(5),
			wantInterval: 5, wantEase: 2.18, wantReps: 1, wantLapses: 1,
		},
		{
			name: "переобучение + again — снова первый шаг, интервал сохраняется",
			cur:  relearning, rating: study.RatingAgain,
			wantState: study.StateRelearning, wantDue: 10 * time.Minute,
			wantInterval: 5, wantEase: 2.18, wantLapses: 1,
		},
		{
			name: "переобучение + hard — тот же шаг",
			cur:  relearning, rating: study.RatingHard,
			wantState: study.StateRelearning, wantDue: 10 * time.Minute,
			wantInterval: 5, wantEase: 2.18, wantLapses: 1,
		},
		{
			name: "переобучение + easy — возврат не меньше интервала «легко»",
			cur:  relearning, rating: study.RatingEasy,
			wantState: study.StateReview, wantDue: days(5),
			wantInterval: 5, wantEase: 2.18, wantReps: 1, wantLapses: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := newScheduler(t).Next(tt.cur, tt.rating, now)

			if got.State != tt.wantState {
				t.Errorf("State = %v, ожидалось %v", got.State, tt.wantState)
			}
			if want := now.Add(tt.wantDue); !got.DueAt.Equal(want) {
				t.Errorf("DueAt = %v (через %v), ожидалось %v (через %v)",
					got.DueAt, got.DueAt.Sub(now), want, tt.wantDue)
			}
			if !closeEnough(got.IntervalDays, tt.wantInterval) {
				t.Errorf("IntervalDays = %v, ожидалось %v", got.IntervalDays, tt.wantInterval)
			}
			if !closeEnough(got.EaseFactor, tt.wantEase) {
				t.Errorf("EaseFactor = %v, ожидалось %v", got.EaseFactor, tt.wantEase)
			}
			if got.Repetitions != tt.wantReps {
				t.Errorf("Repetitions = %d, ожидалось %d", got.Repetitions, tt.wantReps)
			}
			if got.Lapses != tt.wantLapses {
				t.Errorf("Lapses = %d, ожидалось %d", got.Lapses, tt.wantLapses)
			}
			if got.LearnStep != tt.wantStep {
				t.Errorf("LearnStep = %d, ожидалось %d", got.LearnStep, tt.wantStep)
			}
			if err := got.Validate(); err != nil {
				t.Errorf("состояние после Next() не проходит валидацию: %v", err)
			}
		})
	}
}

func TestSM2ReferenceSequence(t *testing.T) {
	t.Parallel()

	// Эталон: десять ответов «хорошо» подряд от новой карточки. Значения
	// зафиксированы намеренно — если алгоритм поменяется, тест обязан упасть.
	want := []struct {
		interval float64
		due      time.Duration
		state    study.State
	}{
		{0, 10 * time.Minute, study.StateLearning}, // 1: второй шаг обучения
		{1, days(1), study.StateReview},            // 2: выпуск
		{2.5, days(2.5), study.StateReview},        // 3: 1 × 2.5
		{6.25, days(6.25), study.StateReview},      // 4
		{15.625, days(15.625), study.StateReview},  // 5
		{39.0625, days(39.0625), study.StateReview},
		{97.65625, days(97.65625), study.StateReview},
		{244.140625, days(244.140625), study.StateReview},
		{365, days(365), study.StateReview}, // 9: упёрлись в потолок
		{365, days(365), study.StateReview}, // 10: дальше не растёт
	}

	scheduler := newScheduler(t)
	state := study.NewCardState(now)
	at := now

	for i, step := range want {
		state = scheduler.Next(state, study.RatingGood, at)

		if state.State != step.state {
			t.Fatalf("повторение %d: State = %v, ожидалось %v", i+1, state.State, step.state)
		}
		if !closeEnough(state.IntervalDays, step.interval) {
			t.Fatalf("повторение %d: IntervalDays = %v, ожидалось %v", i+1, state.IntervalDays, step.interval)
		}
		if want := at.Add(step.due); !state.DueAt.Equal(want) {
			t.Fatalf("повторение %d: DueAt = %v, ожидалось %v", i+1, state.DueAt, want)
		}
		// Ответ «хорошо» не меняет коэффициент лёгкости — так устроена формула.
		if !closeEnough(state.EaseFactor, study.DefaultEaseFactor) {
			t.Fatalf("повторение %d: EaseFactor = %v, ожидалось %v", i+1, state.EaseFactor, study.DefaultEaseFactor)
		}
		at = state.DueAt
	}

	if state.Repetitions != len(want)-1 {
		t.Errorf("Repetitions = %d, ожидалось %d (первый ответ прошёл на шагах обучения)",
			state.Repetitions, len(want)-1)
	}
	if state.Lapses != 0 {
		t.Errorf("Lapses = %d, ожидалось 0", state.Lapses)
	}
}

func TestSM2EaseFormula(t *testing.T) {
	t.Parallel()

	// Изменения коэффициента по формуле SM-2 при отображении оценок
	// в качество ответа: again=2, hard=3, good=4, easy=5.
	tests := []struct {
		rating study.Rating
		want   float64
	}{
		{study.RatingAgain, 2.5 - 0.32},
		{study.RatingHard, 2.5 - 0.14},
		{study.RatingGood, 2.5},
		{study.RatingEasy, 2.5 + 0.1},
	}

	scheduler := newScheduler(t)
	for _, tt := range tests {
		got := scheduler.Next(reviewState(), tt.rating, now)
		if !closeEnough(got.EaseFactor, tt.want) {
			t.Errorf("оценка %v: EaseFactor = %v, ожидалось %v", tt.rating, got.EaseFactor, tt.want)
		}
	}
}

func TestSM2EaseFloor(t *testing.T) {
	t.Parallel()

	// Пол 1.3 — то, что не даёт трудному слову схлопнуть интервал в ноль
	// и всплывать в каждой сессии.
	scheduler := newScheduler(t)
	state := reviewState()

	for i := 0; i < 20; i++ {
		state = scheduler.Next(state, study.RatingAgain, now)
		state.State = study.StateReview // возвращаем в повторение, минуя шаги
		if state.EaseFactor < study.MinEaseFactor {
			t.Fatalf("после %d провалов EaseFactor = %v, ниже пола %v", i+1, state.EaseFactor, study.MinEaseFactor)
		}
	}
	if !closeEnough(state.EaseFactor, study.MinEaseFactor) {
		t.Errorf("EaseFactor = %v, ожидался пол %v", state.EaseFactor, study.MinEaseFactor)
	}
}

func TestSM2EaseCeiling(t *testing.T) {
	t.Parallel()

	scheduler := newScheduler(t)
	state := reviewState()
	state.EaseFactor = study.MaxEaseFactor

	got := scheduler.Next(state, study.RatingEasy, now)
	if got.EaseFactor > study.MaxEaseFactor {
		t.Errorf("EaseFactor = %v, ожидался потолок %v", got.EaseFactor, study.MaxEaseFactor)
	}
}

func TestSM2RepeatedLapsesHalveInterval(t *testing.T) {
	t.Parallel()

	scheduler := newScheduler(t)
	state := reviewState()
	state.IntervalDays = 100

	first := scheduler.Next(state, study.RatingAgain, now)
	if !closeEnough(first.IntervalDays, 50) {
		t.Errorf("после провала IntervalDays = %v, ожидалось 50", first.IntervalDays)
	}
	if first.Lapses != state.Lapses+1 {
		t.Errorf("Lapses = %d, ожидалось %d", first.Lapses, state.Lapses+1)
	}

	// Интервал не опускается ниже суток, сколько бы раз карточку ни забыли.
	state.IntervalDays = 1
	if got := scheduler.Next(state, study.RatingAgain, now); !closeEnough(got.IntervalDays, 1) {
		t.Errorf("IntervalDays = %v, ожидался пол в сутки", got.IntervalDays)
	}
}

func TestSM2Jitter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		random float64
		want   float64
	}{
		{"нижняя граница разброса", 0, 25 * 0.95},
		{"середина — интервал не меняется", 0.5, 25},
		{"верхняя граница разброса", 0.999999999, 25 * 1.05},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheduler, err := study.NewSM2(study.DefaultSM2Config(), study.RandFunc(func() float64 { return tt.random }))
			if err != nil {
				t.Fatalf("NewSM2() вернул ошибку: %v", err)
			}

			got := scheduler.Next(reviewState(), study.RatingGood, now)
			if math.Abs(got.IntervalDays-tt.want) > 0.01 {
				t.Errorf("IntervalDays = %v, ожидалось около %v", got.IntervalDays, tt.want)
			}
			// Срок показа всегда согласован с сохранённым интервалом.
			if want := now.Add(days(got.IntervalDays)); !got.DueAt.Equal(want) {
				t.Errorf("DueAt = %v, ожидалось %v", got.DueAt, want)
			}
		})
	}
}

func TestSM2JitterSkipsLearningSteps(t *testing.T) {
	t.Parallel()

	// Разброс касается интервалов в сутках; шаги обучения в минутах он не
	// трогает, иначе «через 10 минут» превращалось бы в лотерею.
	scheduler, err := study.NewSM2(study.DefaultSM2Config(), study.RandFunc(func() float64 { return 0 }))
	if err != nil {
		t.Fatalf("NewSM2() вернул ошибку: %v", err)
	}

	got := scheduler.Next(study.NewCardState(now), study.RatingGood, now)
	if want := now.Add(10 * time.Minute); !got.DueAt.Equal(want) {
		t.Errorf("DueAt = %v, ожидалось %v", got.DueAt, want)
	}
}

func TestSM2DoesNotUseWallClock(t *testing.T) {
	t.Parallel()

	// Момент ответа приходит аргументом: планировщик обязан считать от него,
	// а не от текущего времени. Дата взята заведомо не «сейчас».
	past := time.Date(1999, 12, 31, 23, 59, 0, 0, time.UTC)
	scheduler := newScheduler(t)

	got := scheduler.Next(reviewState(), study.RatingGood, past)
	if want := past.Add(days(25)); !got.DueAt.Equal(want) {
		t.Fatalf("DueAt = %v, ожидалось %v: срок должен отсчитываться от момента ответа", got.DueAt, want)
	}

	// И тот же вызов дважды даёт тот же результат.
	if again := scheduler.Next(reviewState(), study.RatingGood, past); again != got {
		t.Errorf("повторный вызов дал другое состояние: %+v против %+v", again, got)
	}
}

func TestSM2LeavesStateAloneWhenNothingToDo(t *testing.T) {
	t.Parallel()

	scheduler := newScheduler(t)

	suspended := study.CardState{
		State:        study.StateSuspended,
		DueAt:        now,
		IntervalDays: 10,
		EaseFactor:   2.5,
	}
	if got := scheduler.Next(suspended, study.RatingGood, now.Add(time.Hour)); got != suspended {
		t.Errorf("отложенная карточка изменилась: %+v", got)
	}

	state := reviewState()
	if got := scheduler.Next(state, study.Rating(9), now); got != state {
		t.Errorf("неизвестная оценка изменила состояние: %+v", got)
	}
	if got := scheduler.Next(state, 0, now); got != state {
		t.Errorf("незаданная оценка изменила состояние: %+v", got)
	}
}

func TestSM2RepairsBrokenState(t *testing.T) {
	t.Parallel()

	scheduler := newScheduler(t)

	// Нулевой ease из недозаполненной строки — это «значение не задано»,
	// а не «карточка безнадёжна»: берём значение по умолчанию.
	broken := study.CardState{State: study.StateReview, DueAt: now, IntervalDays: 10}
	if got := scheduler.Next(broken, study.RatingGood, now); !closeEnough(got.EaseFactor, study.DefaultEaseFactor) {
		t.Errorf("EaseFactor = %v, ожидалось %v", got.EaseFactor, study.DefaultEaseFactor)
	}

	// Выученная карточка без интервала считается только что выпустившейся.
	noInterval := study.CardState{State: study.StateReview, DueAt: now, EaseFactor: 2.5}
	if got := scheduler.Next(noInterval, study.RatingGood, now); !closeEnough(got.IntervalDays, 2.5) {
		t.Errorf("IntervalDays = %v, ожидалось 2.5", got.IntervalDays)
	}
}

func TestSM2AlwaysProducesValidState(t *testing.T) {
	t.Parallel()

	// Длинная цепочка ответов: что бы ни ответил пользователь, состояние
	// остаётся валидным, а срок показа — не в прошлом.
	scheduler := newScheduler(t)
	state := study.NewCardState(now)
	at := now
	seed := uint64(42)

	for i := 0; i < 500; i++ {
		seed = seed*6364136223846793005 + 1442695040888963407
		rating := study.Rating(seed>>33%4 + 1)

		state = scheduler.Next(state, rating, at)
		if err := state.Validate(); err != nil {
			t.Fatalf("шаг %d (оценка %v): состояние невалидно: %v", i, rating, err)
		}
		if state.DueAt.Before(at) {
			t.Fatalf("шаг %d: срок показа %v раньше момента ответа %v", i, state.DueAt, at)
		}
		if state.IntervalDays > study.DefaultMaxInterval {
			t.Fatalf("шаг %d: интервал %v превысил потолок", i, state.IntervalDays)
		}
		at = state.DueAt
	}
}

func TestSM2ConfigValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(c *study.SM2Config)
		want   error
	}{
		{"без шагов обучения", func(c *study.SM2Config) { c.LearningSteps = nil }, study.ErrRequired},
		{"без шагов переобучения", func(c *study.SM2Config) { c.RelearningSteps = nil }, study.ErrRequired},
		{"нулевой шаг", func(c *study.SM2Config) { c.LearningSteps = []time.Duration{0} }, study.ErrOutOfRange},
		{"отрицательный шаг", func(c *study.SM2Config) { c.RelearningSteps = []time.Duration{-time.Minute} }, study.ErrOutOfRange},
		{"нулевой интервал выпуска", func(c *study.SM2Config) { c.GraduatingInterval = 0 }, study.ErrOutOfRange},
		{"отрицательный множитель", func(c *study.SM2Config) { c.HardMultiplier = -1 }, study.ErrOutOfRange},
		{"множитель провала больше единицы", func(c *study.SM2Config) { c.LapseMultiplier = 1.5 }, study.ErrOutOfRange},
		{"нулевой множитель провала", func(c *study.SM2Config) { c.LapseMultiplier = 0 }, study.ErrOutOfRange},
		{"отрицательный разброс", func(c *study.SM2Config) { c.JitterRatio = -0.1 }, study.ErrOutOfRange},
		{"разброс в единицу", func(c *study.SM2Config) { c.JitterRatio = 1 }, study.ErrOutOfRange},
		{"потолок ниже интервала «легко»", func(c *study.SM2Config) { c.MaxInterval = 2 }, study.ErrOutOfRange},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := study.DefaultSM2Config()
			tt.mutate(&cfg)

			if _, err := study.NewSM2(cfg, nil); !errors.Is(err, tt.want) {
				t.Errorf("NewSM2() = %v, ожидалась ошибка %v", err, tt.want)
			}
		})
	}

	if err := study.DefaultSM2Config().Validate(); err != nil {
		t.Errorf("параметры по умолчанию не проходят валидацию: %v", err)
	}
}

func TestSM2CustomSteps(t *testing.T) {
	t.Parallel()

	// Три шага обучения вместо двух: карточка выпускается только с третьего
	// ответа «хорошо».
	cfg := study.DefaultSM2Config()
	cfg.LearningSteps = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute}

	scheduler, err := study.NewSM2(cfg, nil)
	if err != nil {
		t.Fatalf("NewSM2() вернул ошибку: %v", err)
	}

	state := study.NewCardState(now)
	for i, want := range []time.Duration{5 * time.Minute, 30 * time.Minute} {
		state = scheduler.Next(state, study.RatingGood, now)
		if state.State != study.StateLearning {
			t.Fatalf("ответ %d: State = %v, ожидалось learning", i+1, state.State)
		}
		if got := state.DueAt.Sub(now); got != want {
			t.Fatalf("ответ %d: срок через %v, ожидалось %v", i+1, got, want)
		}
	}

	state = scheduler.Next(state, study.RatingGood, now)
	if state.State != study.StateReview {
		t.Errorf("State = %v, ожидалось review после последнего шага", state.State)
	}
}
