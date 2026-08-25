-- Пример употребления слова. В плане (§4) его не было, но в настоящих
-- словарях он есть, и это самое полезное после самого перевода: слово
-- в живой фразе запоминается иначе, чем в столбике.
--
-- Колонка на лексеме, а не на переводе: пример написан на изучаемом языке
-- и не зависит от того, на какой язык переводят.

-- +goose Up

ALTER TABLE lexemes
    ADD COLUMN example TEXT NOT NULL DEFAULT '' CHECK (char_length(example) <= 500);

COMMENT ON COLUMN lexemes.example IS 'Пример употребления на изучаемом языке';

-- +goose Down

ALTER TABLE lexemes DROP COLUMN example;
