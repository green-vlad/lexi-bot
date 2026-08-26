-- Знакомство с новыми словами стало отдельным занятием.
--
-- Раньше новое слово попадало в очередь повторений само: бот вводил его
-- и тут же спрашивал перевод, которого человек ещё не видел. Теперь новые
-- слова показываются карточкой — слово, перевод, пример — и человек решает
-- сам: «запомнил», «уже знаю» или «пропустить».
--
-- Отсюда две правки схемы. Появилась фаза known: слово, про которое сказали
-- «уже знаю», остаётся в курсе, чтобы не предлагаться снова, но повторять
-- в нём нечего. И главный индекс сузился до фаз, которые участвуют
-- в повторениях: карточка в фазе new ждёт знакомства, а не срока.
--
-- У карточки в фазе new due_at означает не срок повторения, а момент,
-- с которого слово снова показывается в знакомстве: «пропустить» отодвигает
-- его на следующие сутки.

-- +goose Up

ALTER TABLE cards DROP CONSTRAINT cards_state_check;

ALTER TABLE cards
    ADD CONSTRAINT cards_state_check
        CHECK (state IN ('new', 'learning', 'review', 'relearning', 'suspended', 'known'));

DROP INDEX cards_due_idx;

CREATE INDEX cards_due_idx ON cards (user_course_id, due_at)
    WHERE state IN ('learning', 'review', 'relearning');

-- Индекс знакомства: «следующие слова курса, которые пора показать».
CREATE INDEX cards_new_idx ON cards (user_course_id, due_at) WHERE state = 'new';

-- Пять новых слов в день вместо десяти. Десять — это около получаса
-- повторений через месяц, и первым, что человек видит от бота, становится
-- очередь, которую он не просил.
ALTER TABLE user_settings ALTER COLUMN new_per_day SET DEFAULT 5;

-- Норма меняется и у тех, кто уже заведён. Обычно трогать настройки
-- пользователя миграцией нельзя, но менять их пока негде — /settings ещё
-- не написан (T-044), — поэтому десятка в строке означает не выбор человека,
-- а прежнее значение по умолчанию.
UPDATE user_settings SET new_per_day = 5 WHERE new_per_day = 10;

COMMENT ON COLUMN cards.due_at IS
    'Когда карточка снова попадёт человеку: срок повторения, а в фазе new — момент показа в знакомстве';

-- +goose Down

ALTER TABLE user_settings ALTER COLUMN new_per_day SET DEFAULT 10;

DROP INDEX cards_new_idx;
DROP INDEX cards_due_idx;

CREATE INDEX cards_due_idx ON cards (user_course_id, due_at) WHERE state <> 'suspended';

UPDATE cards SET state = 'suspended' WHERE state = 'known';

ALTER TABLE cards DROP CONSTRAINT cards_state_check;

ALTER TABLE cards
    ADD CONSTRAINT cards_state_check
        CHECK (state IN ('new', 'learning', 'review', 'relearning', 'suspended'));
