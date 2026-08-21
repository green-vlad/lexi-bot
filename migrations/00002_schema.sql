-- Схема из плана (PLAN.md §4). Ключевое решение модели: лексема хранится один
-- раз, переводы вынесены в отдельную таблицу с собственным языком — благодаря
-- этому один частотный список обслуживает любое число языков перевода.
--
-- Перечисления сделаны текстовыми колонками с CHECK, а не типами ENUM: домен
-- уже умеет разбирать эти строки, а добавление значения в ENUM — отдельная
-- морока с блокировками, которой при пяти состояниях карточки взяться неоткуда.

-- +goose Up

-- ---------- справочник языков ----------

CREATE TABLE languages (
    code        TEXT PRIMARY KEY CHECK (code ~ '^[a-z]{2,8}(-[A-Za-z0-9]{2,8})*$'),
    name_native TEXT    NOT NULL CHECK (name_native <> ''),
    -- has_reading отмечает языки, где слову нужна транскрипция: корейскому
    -- и японскому нужна, испанскому нет.
    has_reading BOOLEAN NOT NULL DEFAULT FALSE,
    -- is_ui отмечает языки, на которые переведён сам бот.
    is_ui       BOOLEAN NOT NULL DEFAULT FALSE
);

COMMENT ON TABLE languages IS 'Языки: и изучаемые, и языки перевода, и языки интерфейса';

-- Базовые языки нужны сразу: на них ссылаются все остальные таблицы, и без
-- них не создать ни пользователя, ни колоду.
INSERT INTO languages (code, name_native, has_reading, is_ui) VALUES
    ('ru', 'Русский',  FALSE, TRUE),
    ('en', 'English',  FALSE, TRUE),
    ('ko', '한국어',    TRUE,  FALSE),
    ('es', 'Español',  FALSE, FALSE);

-- ---------- пользователи ----------

CREATE TABLE users (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tg_user_id  BIGINT      NOT NULL UNIQUE CHECK (tg_user_id > 0),
    tg_username TEXT        NOT NULL DEFAULT '' CHECK (tg_username ~ '^[A-Za-z0-9_]{0,32}$'),
    ui_lang     TEXT        NOT NULL REFERENCES languages (code),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- deleted_at — мягкое удаление: строки не исчезают, чтобы журнал повторений
    -- оставался непротиворечивым.
    deleted_at  TIMESTAMPTZ
);

CREATE TABLE user_settings (
    user_id             BIGINT  PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    new_per_day         INTEGER NOT NULL DEFAULT 10  CHECK (new_per_day BETWEEN 1 AND 100),
    max_reviews_per_day INTEGER NOT NULL DEFAULT 200 CHECK (max_reviews_per_day BETWEEN 10 AND 500),
    -- reminder_at — местное время напоминания; NULL означает «выключено».
    -- Даты здесь нет намеренно: настройка одна, а момент напоминания разный
    -- каждый день.
    reminder_at         TIME,
    -- timezone задаёт границу суток пользователя, по которой считаются дневные
    -- лимиты. Лежит рядом с лимитами, а не в users, потому что нужна ровно им.
    timezone            TEXT    NOT NULL DEFAULT 'UTC' CHECK (timezone <> ''),
    quiz_modes          TEXT[]  NOT NULL DEFAULT ARRAY['recall', 'choice', 'typing']
        CHECK (array_length(quiz_modes, 1) >= 1
               AND quiz_modes <@ ARRAY['recall', 'choice', 'typing']::TEXT[]),
    reverse_direction   BOOLEAN NOT NULL DEFAULT FALSE
);

COMMENT ON COLUMN user_settings.timezone IS 'Зона IANA: граница суток для дневных лимитов';

-- ---------- словарь ----------

CREATE TABLE lexemes (
    id            BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    lang_code     TEXT        NOT NULL REFERENCES languages (code),
    term          TEXT        NOT NULL CHECK (term <> '' AND char_length(term) <= 200),
    reading       TEXT        NOT NULL DEFAULT '' CHECK (char_length(reading) <= 200),
    pos           TEXT        NOT NULL DEFAULT '' CHECK (pos IN (
        '', 'noun', 'verb', 'adj', 'adv', 'pron', 'num',
        'adp', 'conj', 'part', 'interj', 'phrase', 'other')),
    freq_rank     INTEGER     NOT NULL DEFAULT 0 CHECK (freq_rank >= 0),
    -- owner_user_id: NULL — встроенное слово, видное всем; иначе личное слово.
    owner_user_id BIGINT      REFERENCES users (id) ON DELETE CASCADE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- COALESCE в уникальном индексе обязателен: в SQL два NULL не равны друг другу,
-- и без него встроенный словарь можно было бы загрузить дважды.
CREATE UNIQUE INDEX lexemes_unique_idx
    ON lexemes (lang_code, term, pos, COALESCE(owner_user_id, 0));

-- Выдача новых слов идёт по частотности внутри языка.
CREATE INDEX lexemes_builtin_freq_idx
    ON lexemes (lang_code, freq_rank) WHERE owner_user_id IS NULL;

CREATE INDEX lexemes_owner_idx
    ON lexemes (owner_user_id) WHERE owner_user_id IS NOT NULL;

CREATE TABLE translations (
    id         BIGINT  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    lexeme_id  BIGINT  NOT NULL REFERENCES lexemes (id) ON DELETE CASCADE,
    lang_code  TEXT    NOT NULL REFERENCES languages (code),
    text       TEXT    NOT NULL CHECK (text <> '' AND char_length(text) <= 300),
    is_primary BOOLEAN NOT NULL DEFAULT FALSE,
    note       TEXT    NOT NULL DEFAULT '' CHECK (char_length(note) <= 500)
);

-- Несколько строк на язык — это разные допустимые значения одного слова,
-- и в режиме ввода текстом засчитывается любое из них.
CREATE INDEX translations_lexeme_lang_idx ON translations (lexeme_id, lang_code);

CREATE UNIQUE INDEX translations_unique_idx ON translations (lexeme_id, lang_code, text);

-- Основное значение на язык может быть только одно: его показывают в карточке.
CREATE UNIQUE INDEX translations_primary_idx
    ON translations (lexeme_id, lang_code) WHERE is_primary;

-- ---------- колоды ----------

CREATE TABLE decks (
    id            BIGINT  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- code — устойчивый слаг встроенной колоды (ko-top-2000). У личных пуст.
    code          TEXT    NOT NULL DEFAULT ''
        CHECK (code ~ '^([a-z0-9]+(-[a-z0-9]+)*)?$' AND char_length(code) <= 64),
    owner_user_id BIGINT  REFERENCES users (id) ON DELETE CASCADE,
    lang_code     TEXT    NOT NULL REFERENCES languages (code),
    title         TEXT    NOT NULL CHECK (title <> '' AND char_length(title) <= 100),
    description   TEXT    NOT NULL DEFAULT '' CHECK (char_length(description) <= 500),
    size          INTEGER NOT NULL DEFAULT 0 CHECK (size >= 0),
    -- Вычисляемая колонка вместо обычной: признак выводится из владельца,
    -- и двух источников правды здесь быть не должно.
    is_builtin    BOOLEAN GENERATED ALWAYS AS (owner_user_id IS NULL) STORED,
    CONSTRAINT decks_code_belongs_to_builtin CHECK (
        (owner_user_id IS NULL AND code <> '') OR (owner_user_id IS NOT NULL AND code = '')
    )
);

CREATE UNIQUE INDEX decks_code_idx ON decks (code) WHERE owner_user_id IS NULL;

-- Личная колода у пользователя одна на язык изучения: своё слово попадает
-- именно в неё.
CREATE UNIQUE INDEX decks_personal_idx
    ON decks (owner_user_id, lang_code) WHERE owner_user_id IS NOT NULL;

CREATE TABLE deck_items (
    deck_id   BIGINT  NOT NULL REFERENCES decks (id) ON DELETE CASCADE,
    lexeme_id BIGINT  NOT NULL REFERENCES lexemes (id) ON DELETE CASCADE,
    -- position задаёт очередь изучения: у встроенных колод это частотность.
    position  INTEGER NOT NULL CHECK (position >= 0),
    PRIMARY KEY (deck_id, lexeme_id)
);

CREATE INDEX deck_items_order_idx ON deck_items (deck_id, position);

-- ---------- курсы и карточки ----------

CREATE TABLE user_courses (
    id               BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id          BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    deck_id          BIGINT      NOT NULL REFERENCES decks (id) ON DELETE CASCADE,
    translation_lang TEXT        NOT NULL REFERENCES languages (code),
    status           TEXT        NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'paused', 'archived')),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Одну колоду можно учить с переводом на разные языки, но только по разу.
    UNIQUE (user_id, deck_id, translation_lang)
);

CREATE INDEX user_courses_active_idx ON user_courses (user_id) WHERE status = 'active';

CREATE TABLE cards (
    id               BIGINT           GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_course_id   BIGINT           NOT NULL REFERENCES user_courses (id) ON DELETE CASCADE,
    lexeme_id        BIGINT           NOT NULL REFERENCES lexemes (id) ON DELETE CASCADE,
    state            TEXT             NOT NULL DEFAULT 'new'
        CHECK (state IN ('new', 'learning', 'review', 'relearning', 'suspended')),
    due_at           TIMESTAMPTZ      NOT NULL,
    interval_days    DOUBLE PRECISION NOT NULL DEFAULT 0   CHECK (interval_days >= 0),
    -- Границы совпадают с доменными (study.MinEaseFactor и MaxEaseFactor):
    -- пол 1.3 не даёт трудному слову схлопнуть интервал в ноль.
    ease_factor      DOUBLE PRECISION NOT NULL DEFAULT 2.5 CHECK (ease_factor BETWEEN 1.3 AND 5.0),
    repetitions      INTEGER          NOT NULL DEFAULT 0 CHECK (repetitions >= 0),
    lapses           INTEGER          NOT NULL DEFAULT 0 CHECK (lapses >= 0),
    learn_step       INTEGER          NOT NULL DEFAULT 0 CHECK (learn_step >= 0),
    introduced_at    TIMESTAMPTZ      NOT NULL DEFAULT now(),
    last_reviewed_at TIMESTAMPTZ,
    UNIQUE (user_course_id, lexeme_id)
);

-- Главный индекс приложения: «взять карточки курса с due_at <= now по
-- возрастанию срока». Отложенные карточки из индекса исключены — они
-- не выдаются никогда, и место в индексе им ни к чему.
CREATE INDEX cards_due_idx ON cards (user_course_id, due_at) WHERE state <> 'suspended';

CREATE TABLE reviews (
    id            BIGINT           GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    card_id       BIGINT           NOT NULL REFERENCES cards (id) ON DELETE CASCADE,
    -- user_id — денормализация ради статистики: иначе каждый агрегат тянул бы
    -- за собой cards и user_courses.
    user_id       BIGINT           NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    rated_at      TIMESTAMPTZ      NOT NULL,
    rating        TEXT             NOT NULL CHECK (rating IN ('again', 'hard', 'good', 'easy')),
    mode          TEXT             NOT NULL CHECK (mode IN ('recall', 'choice', 'typing')),
    answer_raw    TEXT             NOT NULL DEFAULT '',
    is_correct    BOOLEAN          NOT NULL,
    duration_ms   INTEGER          NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    prev_interval DOUBLE PRECISION NOT NULL CHECK (prev_interval >= 0),
    new_interval  DOUBLE PRECISION NOT NULL CHECK (new_interval >= 0),
    prev_ef       DOUBLE PRECISION NOT NULL CHECK (prev_ef > 0),
    new_ef        DOUBLE PRECISION NOT NULL CHECK (new_ef > 0),
    -- Текст ответа бывает только там, где пользователь его печатал.
    CONSTRAINT reviews_answer_only_typing CHECK (answer_raw = '' OR mode = 'typing')
);

COMMENT ON TABLE reviews IS 'Журнал повторений: только вставки, правок и удалений нет';

CREATE INDEX reviews_user_time_idx ON reviews (user_id, rated_at);
CREATE INDEX reviews_card_time_idx ON reviews (card_id, rated_at);

CREATE TABLE daily_counters (
    user_course_id BIGINT  NOT NULL REFERENCES user_courses (id) ON DELETE CASCADE,
    -- day — календарная дата в таймзоне пользователя, а не в UTC.
    day            DATE    NOT NULL,
    new_introduced INTEGER NOT NULL DEFAULT 0 CHECK (new_introduced >= 0),
    reviews_done   INTEGER NOT NULL DEFAULT 0 CHECK (reviews_done >= 0),
    PRIMARY KEY (user_course_id, day)
);

-- ---------- служебное ----------

CREATE TABLE user_sessions (
    user_id    BIGINT      PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    state      TEXT        NOT NULL CHECK (state <> ''),
    payload    JSONB       NOT NULL DEFAULT '{}'::JSONB,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- По этому индексу подчищаются состояния диалогов, брошенных на полуслове.
CREATE INDEX user_sessions_stale_idx ON user_sessions (updated_at);

CREATE TABLE import_jobs (
    id            BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id       BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    file_name     TEXT        NOT NULL,
    status        TEXT        NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'running', 'done', 'failed')),
    rows_total    INTEGER     NOT NULL DEFAULT 0 CHECK (rows_total >= 0),
    rows_imported INTEGER     NOT NULL DEFAULT 0 CHECK (rows_imported >= 0),
    rows_failed   INTEGER     NOT NULL DEFAULT 0 CHECK (rows_failed >= 0),
    error_report  JSONB       NOT NULL DEFAULT '[]'::JSONB,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX import_jobs_user_idx ON import_jobs (user_id, created_at DESC);

CREATE TABLE outbox_notifications (
    id            BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id       BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind          TEXT        NOT NULL CHECK (kind <> ''),
    scheduled_for TIMESTAMPTZ NOT NULL,
    sent_at       TIMESTAMPTZ,
    payload       JSONB       NOT NULL DEFAULT '{}'::JSONB
);

COMMENT ON TABLE outbox_notifications IS
    'Очередь напоминаний: гарантирует, что напоминание не уйдёт дважды';

-- Уникальность и есть та самая гарантия: два инстанса или рестарт посреди
-- рассылки не создадут второе напоминание на тот же момент.
CREATE UNIQUE INDEX outbox_unique_idx
    ON outbox_notifications (user_id, kind, scheduled_for);

CREATE INDEX outbox_pending_idx
    ON outbox_notifications (scheduled_for) WHERE sent_at IS NULL;

-- +goose Down

DROP TABLE outbox_notifications;
DROP TABLE import_jobs;
DROP TABLE user_sessions;
DROP TABLE daily_counters;
DROP TABLE reviews;
DROP TABLE cards;
DROP TABLE user_courses;
DROP TABLE deck_items;
DROP TABLE decks;
DROP TABLE translations;
DROP TABLE lexemes;
DROP TABLE user_settings;
DROP TABLE users;
DROP TABLE languages;
