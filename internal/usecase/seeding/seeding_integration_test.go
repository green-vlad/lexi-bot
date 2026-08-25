//go:build integration

package seeding_test

import (
	"context"
	"testing"

	"lexi-bot/internal/adapter/seeds"
	storage "lexi-bot/internal/adapter/storage/postgres"
	"lexi-bot/internal/usecase/seeding"
	seedfiles "lexi-bot/seeds"
	"lexi-bot/test/pgtest"
)

func TestSeederLoadsAndRepeats(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()

	decks, err := seeds.Load(seedfiles.FS)
	if err != nil {
		t.Fatalf("Load() вернул ошибку: %v", err)
	}

	service, err := seeding.New(seeding.Deps{
		Decks:   storage.NewDeckRepo(pool),
		Lexemes: storage.NewLexemeRepo(pool),
		Tx:      storage.NewTxManager(pool),
	})
	if err != nil {
		t.Fatalf("seeding.New() вернул ошибку: %v", err)
	}

	first, err := service.Load(ctx, decks)
	if err != nil {
		t.Fatalf("первая загрузка вернула ошибку: %v", err)
	}
	if first.Added() == 0 {
		t.Fatal("первая загрузка ничего не добавила")
	}
	if first.Changed() != 0 || first.Unchanged() != 0 {
		t.Errorf("первая загрузка = %+v, ожидались только добавления", first)
	}

	// Второй прогон — самое важное: сидер запускается на каждом выкате,
	// и он обязан оставить базу ровно в том же состоянии.
	second, err := service.Load(ctx, decks)
	if err != nil {
		t.Fatalf("повторная загрузка вернула ошибку: %v", err)
	}
	if second.Added() != 0 || second.Changed() != 0 {
		t.Errorf("повторная загрузка = %+v, ожидалось «ничего не изменилось»", second)
	}
	if second.Unchanged() != first.Added() {
		t.Errorf("без изменений %d слов, ожидалось %d", second.Unchanged(), first.Added())
	}

	// Слова без переводов и без места в колоде карточкам бесполезны.
	var translations, items, size int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM translations").Scan(&translations); err != nil {
		t.Fatalf("подсчёт переводов не прошёл: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM deck_items").Scan(&items); err != nil {
		t.Fatalf("подсчёт состава не прошёл: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT sum(size) FROM decks").Scan(&size); err != nil {
		t.Fatalf("чтение размеров не прошло: %v", err)
	}

	if items != first.Added() {
		t.Errorf("в колодах %d слов, загружено %d", items, first.Added())
	}
	if translations < items {
		t.Errorf("переводов %d при %d словах: у кого-то перевода нет", translations, items)
	}
	if size != items {
		t.Errorf("размер колод = %d, а слов в них %d", size, items)
	}
}

func TestSeederNoticesChanges(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()

	decks, err := seeds.Load(seedfiles.FS)
	if err != nil {
		t.Fatalf("Load() вернул ошибку: %v", err)
	}

	service, err := seeding.New(seeding.Deps{
		Decks:   storage.NewDeckRepo(pool),
		Lexemes: storage.NewLexemeRepo(pool),
		Tx:      storage.NewTxManager(pool),
	})
	if err != nil {
		t.Fatalf("seeding.New() вернул ошибку: %v", err)
	}

	if _, err := service.Load(ctx, decks); err != nil {
		t.Fatalf("первая загрузка вернула ошибку: %v", err)
	}

	// Словарь поправили: у одного слова изменился пример.
	decks[0].Words[0].Lexeme.Example = "новый пример"

	report, err := service.Load(ctx, decks)
	if err != nil {
		t.Fatalf("повторная загрузка вернула ошибку: %v", err)
	}
	if report.Changed() != 1 {
		t.Errorf("изменённых слов %d, ожидалось одно: %+v", report.Changed(), report.Decks)
	}
	if report.Added() != 0 {
		t.Errorf("добавлено %d слов, ожидался ноль", report.Added())
	}

	var example string
	err = pool.QueryRow(ctx, "SELECT example FROM lexemes WHERE term = $1", decks[0].Words[0].Lexeme.Term).
		Scan(&example)
	if err != nil {
		t.Fatalf("чтение примера не прошло: %v", err)
	}
	if example != "новый пример" {
		t.Errorf("пример = %q, правка не доехала", example)
	}
}

func TestSeederRollsBackBrokenDeck(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()

	decks, err := seeds.Load(seedfiles.FS)
	if err != nil {
		t.Fatalf("Load() вернул ошибку: %v", err)
	}

	// Ломаем одно слово в первой колоде так, как разбор не поймал бы:
	// перевод без текста отвергнет уже база.
	decks[0].Words[1].Translations[0].Text = ""

	service, err := seeding.New(seeding.Deps{
		Decks:   storage.NewDeckRepo(pool),
		Lexemes: storage.NewLexemeRepo(pool),
		Tx:      storage.NewTxManager(pool),
	})
	if err != nil {
		t.Fatalf("seeding.New() вернул ошибку: %v", err)
	}

	if _, err := service.Load(ctx, decks[:1]); err == nil {
		t.Fatal("битая колода должна ронять загрузку")
	}

	// Колода грузится своей транзакцией: наполовину загруженной колоды
	// остаться не должно — по ней бы учились.
	var words int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM deck_items").Scan(&words); err != nil {
		t.Fatalf("подсчёт состава не прошёл: %v", err)
	}
	if words != 0 {
		t.Errorf("в колоде осталось %d слов, ожидалась пустота", words)
	}
}

func TestSeederNeedsDependencies(t *testing.T) {
	if _, err := seeding.New(seeding.Deps{}); err == nil {
		t.Error("загрузчик без зависимостей должен быть ошибкой")
	}
}
