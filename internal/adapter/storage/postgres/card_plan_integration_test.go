//go:build integration

// Тест внутри пакета, а не рядом с остальными: он проверяет план того же
// самого запроса, которым ходит Due, и для этого ему нужен неэкспортируемый
// dueCardsQuery.
package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"lexi-bot/test/pgtest"
)

// TestDueQueryUsesIndex проверяет, что выдача карточек идёт по индексу.
//
// Это главный запрос приложения: он выполняется на каждый показ карточки,
// и последовательное чтение таблицы здесь означало бы, что с ростом числа
// пользователей бот будет замедляться линейно. Проверять план на пустой
// таблице бессмысленно — планировщик разумно предпочтёт ей seq scan,
// поэтому тест сначала набивает базу.
func TestDueQueryUsesIndex(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()

	var (
		userID   int64
		deckID   int64
		courseID int64
	)
	err := pool.QueryRow(ctx,
		"INSERT INTO users (tg_user_id, ui_lang) VALUES (777, 'ru') RETURNING id").Scan(&userID)
	if err != nil {
		t.Fatalf("вставка пользователя не прошла: %v", err)
	}
	err = pool.QueryRow(ctx,
		"INSERT INTO decks (code, lang_code, title) VALUES ('ko-top-2000', 'ko', 'топ') RETURNING id").Scan(&deckID)
	if err != nil {
		t.Fatalf("вставка колоды не прошла: %v", err)
	}
	err = pool.QueryRow(ctx,
		`INSERT INTO user_courses (user_id, deck_id, translation_lang)
		 VALUES ($1, $2, 'ru') RETURNING id`, userID, deckID).Scan(&courseID)
	if err != nil {
		t.Fatalf("вставка курса не прошла: %v", err)
	}

	// Карточек нужно столько, чтобы индекс стал выгоднее чтения таблицы.
	const cards = 5000
	_, err = pool.Exec(ctx, `
		INSERT INTO lexemes (lang_code, term)
		SELECT 'ko', 'слово-' || i FROM generate_series(1, $1) AS i`, cards)
	if err != nil {
		t.Fatalf("вставка слов не прошла: %v", err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO cards (user_course_id, lexeme_id, state, due_at)
		SELECT $1, id, 'review', now() + (random() * interval '60 days')
		FROM lexemes WHERE term LIKE 'слово-%'`, courseID)
	if err != nil {
		t.Fatalf("вставка карточек не прошла: %v", err)
	}

	// Без свежей статистики планировщик считает таблицу пустой.
	if _, err := pool.Exec(ctx, "ANALYZE cards"); err != nil {
		t.Fatalf("ANALYZE не прошёл: %v", err)
	}

	// FORMAT JSON, а не TEXT: текстовый план приходит построчно, и читать
	// его пришлось бы циклом, теряя строки при первой невнимательности.
	var plan string
	err = pool.QueryRow(ctx,
		"EXPLAIN (FORMAT JSON) "+dueCardsQuery, courseID, time.Now().Add(24*time.Hour), 20).Scan(&plan)
	if err != nil {
		t.Fatalf("EXPLAIN не прошёл: %v", err)
	}

	if !strings.Contains(plan, "cards_due_idx") {
		t.Errorf("запрос не использует индекс cards_due_idx, план:\n%s", plan)
	}
	if strings.Contains(plan, `"Node Type": "Seq Scan"`) {
		t.Errorf("запрос читает таблицу целиком, план:\n%s", plan)
	}
	// Сортировка тоже должна доставаться из индекса, а не считаться заново.
	if strings.Contains(plan, `"Node Type": "Sort"`) {
		t.Errorf("запрос сортирует результат вручную, план:\n%s", plan)
	}
}
