-- Какой курс пользователь учит сейчас.
--
-- Курсов у человека может быть несколько, а занятие идёт по одному: без
-- этой отметки /learn пришлось бы каждый раз спрашивать, что учить, или
-- молча брать первый попавшийся.
--
-- ON DELETE SET NULL, а не CASCADE: удаление курса не должно уносить
-- пользователя. Пустое значение означает «возьмём любой активный».

-- +goose Up

ALTER TABLE users
    ADD COLUMN current_course_id BIGINT REFERENCES user_courses (id) ON DELETE SET NULL;

COMMENT ON COLUMN users.current_course_id IS 'Курс, который пользователь учит сейчас';

-- +goose Down

ALTER TABLE users DROP COLUMN current_course_id;
