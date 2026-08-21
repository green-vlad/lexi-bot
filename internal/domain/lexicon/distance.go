package lexicon

// Levenshtein возвращает число правок (вставок, удалений, замен), которыми
// одна строка превращается в другую.
//
// Считается по рунам, а не по байтам: «дом» — это три символа и шесть байт,
// и опечатка в одну букву не должна выглядеть как две правки.
func Levenshtein(a, b string) int {
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}
	// Короткую строку кладём в строку матрицы, длинную — в столбцы:
	// памяти нужно по длине короткой.
	if len(ar) > len(br) {
		ar, br = br, ar
	}

	prev := make([]int, len(ar)+1)
	curr := make([]int, len(ar)+1)
	for i := range prev {
		prev[i] = i
	}

	for j := 1; j <= len(br); j++ {
		curr[0] = j
		for i := 1; i <= len(ar); i++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[i] = min(prev[i]+1, curr[i-1]+1, prev[i-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(ar)]
}
