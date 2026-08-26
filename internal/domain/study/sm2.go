package study

import (
	"fmt"
	"time"
)

// Rand — источник случайности для джиттера интервалов. Интерфейс, а не
// math/rand напрямую: тест подставляет предсказуемую последовательность и
// получает воспроизводимые интервалы.
type Rand interface {
	// Float64 возвращает число в промежутке [0, 1).
	Float64() float64
}

// RandFunc превращает обычную функцию в Rand.
type RandFunc func() float64

// Float64 вызывает саму функцию.
func (f RandFunc) Float64() float64 { return f() }

// Значения по умолчанию — те же, что в Anki: они проверены на миллионах
// карточек, и отклоняться от них без данных смысла нет.
const (
	// DefaultGraduatingInterval — интервал карточки, только что прошедшей
	// шаги обучения, в сутках.
	DefaultGraduatingInterval = 1.0
	// DefaultEasyInterval — интервал карточки, которую на шагах обучения
	// оценили как «легко», в сутках.
	DefaultEasyInterval = 4.0
	// DefaultHardMultiplier — во сколько раз растёт интервал при оценке «трудно».
	DefaultHardMultiplier = 1.2
	// DefaultEasyBonus — дополнительный множитель интервала при оценке «легко».
	DefaultEasyBonus = 1.3
	// DefaultLapseMultiplier — какая часть интервала остаётся после провала.
	DefaultLapseMultiplier = 0.5
	// DefaultMaxInterval — потолок интервала в сутках. Год — предел, после
	// которого рост интервала перестаёт что-либо значить для запоминания.
	DefaultMaxInterval = 365.0
	// DefaultJitterRatio — разброс интервала, ±5%. Нужен, чтобы карточки,
	// введённые в один день, не возвращались все разом на много лет вперёд.
	DefaultJitterRatio = 0.05
)

// SM2Config — параметры планировщика. Вынесены в структуру не ради
// настраиваемости в проде, а ради тестов: подменив шаги обучения, можно
// проверить переходы, не завися от конкретных значений по умолчанию.
type SM2Config struct {
	// LearningSteps — шаги обучения новой карточки. Пустым быть не может.
	LearningSteps []time.Duration
	// RelearningSteps — шаги переобучения провáленной карточки.
	RelearningSteps []time.Duration

	GraduatingInterval float64
	EasyInterval       float64
	HardMultiplier     float64
	EasyBonus          float64
	LapseMultiplier    float64
	MaxInterval        float64
	JitterRatio        float64
}

// DefaultSM2Config возвращает параметры по умолчанию: шаги 1 и 10 минут,
// выпуск через сутки, «легко» — сразу четыре дня.
func DefaultSM2Config() SM2Config {
	return SM2Config{
		LearningSteps:      []time.Duration{time.Minute, 10 * time.Minute},
		RelearningSteps:    []time.Duration{10 * time.Minute},
		GraduatingInterval: DefaultGraduatingInterval,
		EasyInterval:       DefaultEasyInterval,
		HardMultiplier:     DefaultHardMultiplier,
		EasyBonus:          DefaultEasyBonus,
		LapseMultiplier:    DefaultLapseMultiplier,
		MaxInterval:        DefaultMaxInterval,
		JitterRatio:        DefaultJitterRatio,
	}
}

// Validate проверяет, что параметры осмысленны.
func (c SM2Config) Validate() error {
	if len(c.LearningSteps) == 0 {
		return fmt.Errorf("learning_steps: %w (нужен хотя бы один шаг обучения)", ErrRequired)
	}
	if len(c.RelearningSteps) == 0 {
		return fmt.Errorf("relearning_steps: %w (нужен хотя бы один шаг переобучения)", ErrRequired)
	}
	for _, steps := range [][]time.Duration{c.LearningSteps, c.RelearningSteps} {
		for _, step := range steps {
			if step <= 0 {
				return fmt.Errorf("шаг обучения %v: %w (ожидалась положительная длительность)", step, ErrOutOfRange)
			}
		}
	}

	positives := map[string]float64{
		"graduating_interval": c.GraduatingInterval,
		"easy_interval":       c.EasyInterval,
		"hard_multiplier":     c.HardMultiplier,
		"easy_bonus":          c.EasyBonus,
		"max_interval":        c.MaxInterval,
	}
	for name, value := range positives {
		if value <= 0 {
			return fmt.Errorf("%s = %v: %w (ожидалось положительное число)", name, value, ErrOutOfRange)
		}
	}
	if c.LapseMultiplier <= 0 || c.LapseMultiplier > 1 {
		return fmt.Errorf("lapse_multiplier = %v: %w (ожидалось значение в промежутке (0, 1])", c.LapseMultiplier, ErrOutOfRange)
	}
	if c.JitterRatio < 0 || c.JitterRatio >= 1 {
		return fmt.Errorf("jitter_ratio = %v: %w (ожидалось значение в промежутке [0, 1))", c.JitterRatio, ErrOutOfRange)
	}
	if c.MaxInterval < c.EasyInterval {
		return fmt.Errorf("max_interval = %v: %w (потолок ниже интервала «легко» %v)", c.MaxInterval, ErrOutOfRange, c.EasyInterval)
	}
	return nil
}

// SM2 — планировщик по алгоритму SM-2 в том виде, в каком его использует Anki:
// короткие шаги обучения в минутах, затем растущий интервал в сутках, а темп
// роста задаёт коэффициент лёгкости.
//
// Next — чистая функция от (состояние, оценка, момент): к текущему времени она
// не обращается, поэтому цепочку из сотни повторений тест проигрывает
// мгновенно и получает те же интервалы, что и прод.
type SM2 struct {
	cfg  SM2Config
	rand Rand
}

// Проверка на этапе компиляции, что планировщик реализует интерфейс.
var _ Scheduler = SM2{}

// NewSM2 создаёт планировщик. Источник случайности нужен для джиттера; если
// передать nil, интервалы будут точными — так удобно в тестах и допустимо
// в проде, если джиттер не нужен.
func NewSM2(cfg SM2Config, r Rand) (SM2, error) {
	if err := cfg.Validate(); err != nil {
		return SM2{}, err
	}
	return SM2{cfg: cfg, rand: r}, nil
}

// Next возвращает состояние карточки после ответа с оценкой r в момент now.
//
// Неизвестная оценка оставляет состояние без изменений: планировщик — не место
// для проверки входных данных, этим занят сценарий, а молча испортить
// состояние хуже, чем ничего не сделать. Так же он поступает с карточками,
// которых в повторениях нет: отложенной и той, про которую сказали «уже знаю».
func (s SM2) Next(cur CardState, r Rating, now time.Time) CardState {
	if !r.IsValid() || cur.State == StateSuspended || cur.State == StateKnown {
		return cur
	}

	next := cur
	// Коэффициент из базы мог оказаться нулевым или испорченным; приводим его
	// к допустимому промежутку до того, как на него умножать.
	next.EaseFactor = s.clampEase(cur.EaseFactor)

	switch cur.State {
	case StateNew, StateLearning:
		return s.learn(next, r, now, s.cfg.LearningSteps, StateLearning)
	case StateRelearning:
		return s.learn(next, r, now, s.cfg.RelearningSteps, StateRelearning)
	case StateReview:
		return s.review(next, r, now)
	default:
		return cur
	}
}

// learn обрабатывает ответ на шагах обучения и переобучения. Коэффициент
// лёгкости здесь не меняется: на шагах карточка ещё не показала, насколько
// хорошо запоминается, и портить им статистику рано.
func (s SM2) learn(next CardState, r Rating, now time.Time, steps []time.Duration, phase State) CardState {
	next.State = phase

	switch r {
	case RatingAgain:
		// Провал возвращает на первый шаг. Счётчик успехов подряд обнуляется,
		// но провалом это не считается: на шагах карточка ещё не была выучена,
		// а Lapses считает забытые именно выученные слова.
		next.LearnStep = 0
		next.Repetitions = 0
		next.DueAt = now.Add(steps[0])
		if phase != StateRelearning {
			next.IntervalDays = 0
		}
		// В переобучении интервал уже уполовинен провалом — он сохраняется,
		// чтобы карточка вернулась к нему после успешного шага.
		return next

	case RatingHard:
		// Шаг не двигается: повторяем тот же интервал.
		step := next.LearnStep
		if step >= len(steps) {
			step = len(steps) - 1
		}
		next.DueAt = now.Add(steps[step])
		return next

	case RatingGood:
		next.LearnStep++
		if next.LearnStep < len(steps) {
			next.DueAt = now.Add(steps[next.LearnStep])
			return next
		}
		return s.graduate(next, s.graduatingInterval(phase, next.IntervalDays), now)

	case RatingEasy:
		// «Легко» пропускает оставшиеся шаги.
		return s.graduate(next, s.easyGraduatingInterval(phase, next.IntervalDays), now)

	default:
		return next
	}
}

// review обрабатывает ответ на выученную карточку: здесь работает формула SM-2
// и здесь же карточка может провалиться в переобучение.
func (s SM2) review(next CardState, r Rating, now time.Time) CardState {
	prevInterval := next.IntervalDays
	if prevInterval <= 0 {
		// Выученная карточка без интервала — испорченная строка; считаем,
		// что она только что выпустилась.
		prevInterval = s.cfg.GraduatingInterval
	}
	next.EaseFactor = s.clampEase(next.EaseFactor + easeDelta(r))

	if r == RatingAgain {
		next.Lapses++
		next.Repetitions = 0
		next.State = StateRelearning
		next.LearnStep = 0
		next.IntervalDays = s.clampInterval(prevInterval * s.cfg.LapseMultiplier)
		next.DueAt = now.Add(s.cfg.RelearningSteps[0])
		return next
	}

	var interval float64
	switch r {
	case RatingHard:
		interval = prevInterval * s.cfg.HardMultiplier
	case RatingGood:
		interval = prevInterval * next.EaseFactor
	case RatingEasy:
		interval = prevInterval * next.EaseFactor * s.cfg.EasyBonus
	default:
		interval = prevInterval
	}
	return s.graduate(next, interval, now)
}

// graduate переводит карточку в фазу повторения с заданным интервалом.
func (s SM2) graduate(next CardState, interval float64, now time.Time) CardState {
	next.State = StateReview
	next.LearnStep = 0
	next.Repetitions++
	next.IntervalDays = s.clampInterval(s.jitter(interval))
	next.DueAt = now.Add(daysToDuration(next.IntervalDays))
	return next
}

// graduatingInterval — интервал, с которым карточка выходит с шагов по оценке
// «хорошо». Новая начинает с суток, а переобученная возвращается с тем
// интервалом, который остался у неё после провала.
func (s SM2) graduatingInterval(phase State, current float64) float64 {
	if phase == StateRelearning {
		return max(current, s.cfg.GraduatingInterval)
	}
	return s.cfg.GraduatingInterval
}

// easyGraduatingInterval — то же по оценке «легко».
func (s SM2) easyGraduatingInterval(phase State, current float64) float64 {
	if phase == StateRelearning {
		return max(current, s.cfg.EasyInterval)
	}
	return s.cfg.EasyInterval
}

// jitter разбрасывает интервал в пределах JitterRatio. К шагам обучения
// не применяется: смысла в разбросе десяти минут нет, а тесты переходов
// он бы усложнил.
func (s SM2) jitter(days float64) float64 {
	if s.rand == nil || s.cfg.JitterRatio <= 0 || days < 1 {
		return days
	}
	factor := 1 + (s.rand.Float64()*2-1)*s.cfg.JitterRatio
	return days * factor
}

func (s SM2) clampEase(ease float64) float64 {
	if ease < MinEaseFactor {
		// Ноль означает «значение не заполнено», а не «карточка безнадёжна».
		if ease <= 0 {
			return DefaultEaseFactor
		}
		return MinEaseFactor
	}
	if ease > MaxEaseFactor {
		return MaxEaseFactor
	}
	return ease
}

func (s SM2) clampInterval(days float64) float64 {
	if days < s.cfg.GraduatingInterval {
		return s.cfg.GraduatingInterval
	}
	if days > s.cfg.MaxInterval {
		return s.cfg.MaxInterval
	}
	return days
}

// easeDelta — изменение коэффициента лёгкости по формуле SM-2
//
//	EF' = EF + (0.1 - (5-q) * (0.08 + (5-q) * 0.02)),
//
// где q — качество ответа от 0 до 5. Наши четыре оценки отображаются в q как
// again=2, hard=3, good=4, easy=5, что даёт -0.32, -0.14, 0 и +0.1.
// Ноль у «хорошо» — не упущение: ровный ответ не меняет представления
// алгоритма о сложности слова.
func easeDelta(r Rating) float64 {
	q := float64(r) + 1
	return 0.1 - (5-q)*(0.08+(5-q)*0.02)
}

func daysToDuration(days float64) time.Duration {
	return time.Duration(days * float64(24*time.Hour))
}
