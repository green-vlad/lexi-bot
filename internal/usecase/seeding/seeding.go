// Package seeding загружает встроенные словари в базу.
//
// Сидер запускается на каждом выкате и потому обязан быть идемпотентным:
// второй прогон подряд должен оставить базу ровно в том же состоянии
// и честно сказать, что ничего не изменилось. Отчёт здесь не украшение —
// это единственный способ увидеть, что словарь поехал, до того как это
// увидят пользователи.
package seeding

import (
	"context"
	"errors"
	"fmt"

	"lexi-bot/internal/adapter/seeds"
	"lexi-bot/internal/domain/lexicon"
	"lexi-bot/internal/usecase/port"
)

// Deps — зависимости загрузчика.
type Deps struct {
	Decks   port.DeckRepo
	Lexemes port.LexemeRepo
	Tx      port.TxManager
}

// Service загружает словари.
type Service struct {
	deps Deps
}

// New создаёт загрузчик.
func New(deps Deps) (*Service, error) {
	switch {
	case deps.Decks == nil:
		return nil, errors.New("сидеру нужен DeckRepo")
	case deps.Lexemes == nil:
		return nil, errors.New("сидеру нужен LexemeRepo")
	case deps.Tx == nil:
		return nil, errors.New("сидеру нужен TxManager")
	}
	return &Service{deps: deps}, nil
}

// DeckReport — что стало с одной колодой.
type DeckReport struct {
	Code string
	// Added, Changed и Unchanged считают слова.
	Added     int
	Changed   int
	Unchanged int
}

// Total — сколько слов в колоде всего.
func (r DeckReport) Total() int { return r.Added + r.Changed + r.Unchanged }

// Report — итог загрузки.
type Report struct {
	Decks []DeckReport
}

// Added — сколько слов добавлено по всем колодам.
func (r Report) Added() int { return r.sum(func(d DeckReport) int { return d.Added }) }

// Changed — сколько слов изменилось.
func (r Report) Changed() int { return r.sum(func(d DeckReport) int { return d.Changed }) }

// Unchanged — сколько слов осталось как было. Именно это число говорит,
// что повторный прогон ничего не испортил.
func (r Report) Unchanged() int { return r.sum(func(d DeckReport) int { return d.Unchanged }) }

func (r Report) sum(pick func(DeckReport) int) int {
	total := 0
	for _, deck := range r.Decks {
		total += pick(deck)
	}
	return total
}

// Load записывает разобранные словари в базу.
//
// Каждая колода грузится своей транзакцией: словарь из десяти колод не должен
// пропадать целиком из-за одной кривой, а незагруженная колода лучше
// загруженной наполовину — по наполовину загруженной люди будут учиться.
func (s *Service) Load(ctx context.Context, decks []seeds.Deck) (Report, error) {
	report := Report{Decks: make([]DeckReport, 0, len(decks))}

	for i := range decks {
		deckReport, err := s.loadDeck(ctx, &decks[i])
		if err != nil {
			return report, fmt.Errorf("колода %q: %w", decks[i].Deck.Code, err)
		}
		report.Decks = append(report.Decks, deckReport)
	}
	return report, nil
}

// loadDeck грузит одну колоду.
func (s *Service) loadDeck(ctx context.Context, deck *seeds.Deck) (DeckReport, error) {
	report := DeckReport{Code: deck.Deck.Code}

	err := s.deps.Tx.InTx(ctx, func(ctx context.Context) error {
		saved, err := s.deps.Decks.EnsureBuiltin(ctx, &deck.Deck)
		if err != nil {
			return err
		}

		lexemes := make([]lexicon.Lexeme, 0, len(deck.Words))
		for i := range deck.Words {
			lexemes = append(lexemes, deck.Words[i].Lexeme)
		}

		upserted, err := s.deps.Lexemes.Upsert(ctx, lexemes)
		if err != nil {
			return err
		}
		if len(upserted) != len(deck.Words) {
			// Такого быть не должно: разбор уже отсеял повторы внутри файла.
			// Если случилось — молчать нельзя, иначе переводы уедут не к тем
			// словам.
			return fmt.Errorf("сохранено %d слов из %d", len(upserted), len(deck.Words))
		}

		translations := make([]lexicon.Translation, 0, len(deck.Words)*2)
		items := make([]lexicon.DeckItem, 0, len(deck.Words))
		for i := range deck.Words {
			switch {
			case upserted[i].Created:
				report.Added++
			case upserted[i].Changed:
				report.Changed++
			default:
				report.Unchanged++
			}

			lexemeID := upserted[i].Lexeme.ID
			for _, translation := range deck.Words[i].Translations {
				translation.LexemeID = lexemeID
				translations = append(translations, translation)
			}
			items = append(items, lexicon.DeckItem{
				DeckID:   saved.ID,
				LexemeID: lexemeID,
				Position: i,
			})
		}

		if err := s.deps.Lexemes.SaveTranslations(ctx, translations); err != nil {
			return err
		}
		return s.deps.Decks.AddItems(ctx, items)
	})
	if err != nil {
		return DeckReport{}, err
	}
	return report, nil
}
