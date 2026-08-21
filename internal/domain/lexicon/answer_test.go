package lexicon_test

import (
	"testing"

	"lexi-bot/internal/domain/lexicon"
)

func TestNormalize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		lang lexicon.Language
		want string
	}{
		{"регистр опускается", "Человек", langRU, "человек"},
		{"краевые пробелы убираются", "  человек  ", langRU, "человек"},
		{"внутренние пробелы схлопываются", "большой   дом", langRU, "большой дом"},
		{"артикль the отбрасывается", "the house", langEN, "house"},
		{"артикль a отбрасывается", "A cat", langEN, "cat"},
		{"глагольное to отбрасывается", "to run", langEN, "run"},
		{"артикль и to вместе", "to the point", langEN, "point"},
		{"испанский артикль", "La casa", langES, "casa"},
		{"артикль без остатка сохраняется", "the", langEN, "the"},
		{"в русском артиклей нет", "a дом", langRU, "a дом"},
		{"пунктуация убирается", "дом, милый дом!", langRU, "дом милый дом"},
		{"дефис становится пробелом", "well-known", langEN, "well known"},
		{"апостроф исчезает", "don't", langEN, "dont"},
		{"пустая строка", "   ", langRU, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := lexicon.Normalize(tt.in, tt.lang); got != tt.want {
				t.Errorf("Normalize(%q) = %q, ожидалось %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNormalizeUnicodeComposition(t *testing.T) {
	t.Parallel()

	// «й» записывается одним кодом или как «и» с комбинирующей краткой:
	// выглядит одинаково, а байты разные. Без NFC такой ответ не засчитался бы.
	precomposed := "й" // й
	decomposed := "й" // и + ̆
	if precomposed == decomposed {
		t.Fatal("тест бессмыслен: строки и так равны")
	}
	if lexicon.Normalize(precomposed, langRU) != lexicon.Normalize(decomposed, langRU) {
		t.Error("NFC не применена: одинаковые на вид строки не сравнялись")
	}

	// То же для корейского: слог собирается из отдельных чамо.
	syllable := "한" // 한
	jamo := "한"   // ᄒ + ᅡ + ᆫ
	if lexicon.Normalize(syllable, langKO) != lexicon.Normalize(jamo, langKO) {
		t.Error("корейский слог из чамо не привёлся к составному виду")
	}
}

func TestSplitVariants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want []string
	}{
		{"дом", []string{"дом"}},
		{"дом; здание", []string{"дом", "здание"}},
		{"дом;здание;жилище", []string{"дом", "здание", "жилище"}},
		{" дом ; ; здание ", []string{"дом", "здание"}},
		{"", nil},
		{" ; ", nil},
	}

	for _, tt := range tests {
		got := lexicon.SplitVariants(tt.in)
		if len(got) != len(tt.want) {
			t.Fatalf("SplitVariants(%q) = %q, ожидалось %q", tt.in, got, tt.want)
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("SplitVariants(%q)[%d] = %q, ожидалось %q", tt.in, i, got[i], tt.want[i])
			}
		}
	}
}

func TestAcceptedAnswersCountsEveryVariant(t *testing.T) {
	t.Parallel()

	// Все значения перевода, перечисленные через «;», засчитываются наравне.
	translations := []lexicon.Translation{
		{LexemeID: 1, Lang: langRU, Text: "дом; здание", IsPrimary: true},
		{LexemeID: 1, Lang: langRU, Text: "жилище"},
	}
	accepted := lexicon.AcceptedAnswers(translations)

	want := []string{"дом", "здание", "жилище"}
	if len(accepted) != len(want) {
		t.Fatalf("AcceptedAnswers() = %q, ожидалось %q", accepted, want)
	}

	for _, answer := range want {
		got := lexicon.CheckAnswer(answer, accepted, langRU)
		if got.Match != lexicon.MatchExact {
			t.Errorf("ответ %q: Match = %v, ожидалось точное совпадение", answer, got.Match)
		}
	}
}

func TestCheckAnswer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		answer   string
		accepted []string
		lang     lexicon.Language
		want     lexicon.Match
	}{
		{
			name:   "совпадение как есть",
			answer: "человек", accepted: []string{"человек"}, lang: langRU,
			want: lexicon.MatchExact,
		},
		{
			name:   "регистр и хвостовой пробел ошибкой не считаются",
			answer: "Человек", accepted: []string{"человек "}, lang: langRU,
			want: lexicon.MatchExact,
		},
		{
			name:   "артикль отброшен — совпадение после нормализации",
			answer: "the house", accepted: []string{"house"}, lang: langEN,
			want: lexicon.MatchNormalized,
		},
		{
			name:   "артикль в ожидаемом ответе",
			answer: "house", accepted: []string{"the house"}, lang: langEN,
			want: lexicon.MatchNormalized,
		},
		{
			name:   "глагол с to",
			answer: "to run", accepted: []string{"run"}, lang: langEN,
			want: lexicon.MatchNormalized,
		},
		{
			name:   "пунктуация не мешает",
			answer: "дом!", accepted: []string{"дом"}, lang: langRU,
			want: lexicon.MatchExact,
		},
		{
			name:   "опечатка на один символ",
			answer: "человекк", accepted: []string{"человек"}, lang: langRU,
			want: lexicon.MatchTypo,
		},
		{
			name:   "пропущенная буква",
			answer: "чловек", accepted: []string{"человек"}, lang: langRU,
			want: lexicon.MatchTypo,
		},
		{
			name:   "перепутанная буква",
			answer: "человрк", accepted: []string{"человек"}, lang: langRU,
			want: lexicon.MatchTypo,
		},
		{
			name:   "две ошибки — это уже не опечатка",
			answer: "чловк", accepted: []string{"человек"}, lang: langRU,
			want: lexicon.MatchNone,
		},
		{
			name:   "другое слово",
			answer: "кошка", accepted: []string{"собака"}, lang: langRU,
			want: lexicon.MatchNone,
		},
		{
			name:   "пустой ответ не засчитывается",
			answer: "   ", accepted: []string{"дом"}, lang: langRU,
			want: lexicon.MatchNone,
		},
		{
			name:   "совпал второй из допустимых",
			answer: "здание", accepted: []string{"дом", "здание"}, lang: langRU,
			want: lexicon.MatchExact,
		},
		{
			name:   "нет допустимых переводов",
			answer: "дом", accepted: nil, lang: langRU,
			want: lexicon.MatchNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := lexicon.CheckAnswer(tt.answer, tt.accepted, tt.lang)
			if got.Match != tt.want {
				t.Errorf("CheckAnswer(%q, %q) = %v (расстояние %d), ожидалось %v",
					tt.answer, tt.accepted, got.Match, got.Distance, tt.want)
			}
			if got.Match.Accepted() != (tt.want != lexicon.MatchNone) {
				t.Errorf("Accepted() = %t при совпадении %v", got.Match.Accepted(), got.Match)
			}
		})
	}
}

func TestCheckAnswerShortWordsGetNoTypoMercy(t *testing.T) {
	t.Parallel()

	// На коротких словах правка в один символ — это другое слово, а не промах
	// по клавише, и засчитывать её нельзя.
	for _, tt := range []struct{ answer, expected string }{
		{"дон", "дом"},
		{"car", "cat"},
		{"да", "де"},
	} {
		got := lexicon.CheckAnswer(tt.answer, []string{tt.expected}, langRU)
		if got.Match != lexicon.MatchNone {
			t.Errorf("ответ %q при ожидаемом %q = %v, ожидалось MatchNone", tt.answer, tt.expected, got.Match)
		}
	}
}

func TestCheckAnswerPicksBestCandidate(t *testing.T) {
	t.Parallel()

	// Среди допустимых переводов выбирается лучшее совпадение, а не первое.
	got := lexicon.CheckAnswer("здание", []string{"дом", "здания", "здание"}, langRU)
	if got.Match != lexicon.MatchExact {
		t.Fatalf("Match = %v, ожидалось точное совпадение", got.Match)
	}
	if got.Expected != "здание" {
		t.Errorf("Expected = %q, ожидалось %q", got.Expected, "здание")
	}
}

func TestCheckAnswerReportsClosestOnMiss(t *testing.T) {
	t.Parallel()

	// При промахе возвращается ближайший перевод и расстояние до него:
	// по ним хендлер показывает разбор ответа.
	got := lexicon.CheckAnswer("кошка", []string{"пёс", "кошелёк"}, langRU)
	if got.Match != lexicon.MatchNone {
		t.Fatalf("Match = %v, ожидалось MatchNone", got.Match)
	}
	if got.Expected != "кошелёк" {
		t.Errorf("Expected = %q, ожидался ближайший перевод «кошелёк»", got.Expected)
	}
	if got.Distance < 1 {
		t.Errorf("Distance = %d, ожидалось положительное расстояние", got.Distance)
	}
}

func TestLevenshtein(t *testing.T) {
	t.Parallel()

	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"дом", "", 3},
		{"", "дом", 3},
		{"дом", "дом", 0},
		{"дом", "дым", 1},
		{"человек", "человекк", 1},
		{"человек", "чловек", 1},
		{"kitten", "sitting", 3},
		{"안녕", "안녕하세요", 3},
	}

	for _, tt := range tests {
		if got := lexicon.Levenshtein(tt.a, tt.b); got != tt.want {
			t.Errorf("Levenshtein(%q, %q) = %d, ожидалось %d", tt.a, tt.b, got, tt.want)
		}
		// Расстояние симметрично.
		if got := lexicon.Levenshtein(tt.b, tt.a); got != tt.want {
			t.Errorf("Levenshtein(%q, %q) = %d, ожидалось %d", tt.b, tt.a, got, tt.want)
		}
	}
}

func TestLevenshteinCountsRunesNotBytes(t *testing.T) {
	t.Parallel()

	// Кириллическая буква — два байта; побайтовый счёт дал бы здесь двойку.
	if got := lexicon.Levenshtein("дом", "дым"); got != 1 {
		t.Errorf("Levenshtein() = %d, ожидалась одна правка", got)
	}
}
