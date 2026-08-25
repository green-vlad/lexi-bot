-- Режим самооценки «помню / не помню» убран из бота.
--
-- Он спрашивал слово, показывал перевод по кнопке и предлагал человеку
-- самому поставить себе оценку. Проверкой это не было: отметку ставил тот
-- же, кого проверяли, и расписание повторений строилось на его снисхождении
-- к себе. Осталось два режима, где правильность видна боту, — выбор
-- из вариантов и ввод текстом.
--
-- Записи журнала со старым режимом не выбрасываются: это история занятий,
-- и переписывать её задним числом нельзя. Ограничение на mode поэтому
-- допускает 'recall' как значение, которое больше не появляется.

-- +goose Up

ALTER TABLE user_settings DROP CONSTRAINT user_settings_quiz_modes_check;

-- Порядок режимов сохраняется: EXCEPT его не гарантирует, а он значащий —
-- в нём режимы показываются в настройках.
UPDATE user_settings
SET quiz_modes = ARRAY(SELECT mode FROM unnest(quiz_modes) AS mode WHERE mode <> 'recall')
WHERE 'recall' = ANY (quiz_modes);

-- Настройки, где самооценка была единственным включённым режимом,
-- остались бы пустыми: возвращаем такому пользователю набор по умолчанию.
UPDATE user_settings
SET quiz_modes = ARRAY['choice', 'typing']
WHERE COALESCE(array_length(quiz_modes, 1), 0) = 0;

ALTER TABLE user_settings
    ALTER COLUMN quiz_modes SET DEFAULT ARRAY['choice', 'typing'],
    ADD CONSTRAINT user_settings_quiz_modes_check
        CHECK (array_length(quiz_modes, 1) >= 1
               AND quiz_modes <@ ARRAY['choice', 'typing']::TEXT[]);

-- +goose Down

ALTER TABLE user_settings DROP CONSTRAINT user_settings_quiz_modes_check;

ALTER TABLE user_settings
    ALTER COLUMN quiz_modes SET DEFAULT ARRAY['recall', 'choice', 'typing'],
    ADD CONSTRAINT user_settings_quiz_modes_check
        CHECK (array_length(quiz_modes, 1) >= 1
               AND quiz_modes <@ ARRAY['recall', 'choice', 'typing']::TEXT[]);
