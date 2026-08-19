# lexi-bot

Telegram-бот для заучивания слов: выдаёт по N новых слов в день и ведёт интервальное
повторение по алгоритму SM-2 (как в классическом Anki).

Язык изучения и язык перевода выбирает пользователь — жёстких пар в модели данных нет.
Интерфейс на русском и английском.

## Документация

* [docs/PLAN.md](docs/PLAN.md) — архитектура, модель данных, принятые решения
* [docs/TASKS.md](docs/TASKS.md) — бэклог задач с критериями готовности
* [docs/DEPLOY.md](docs/DEPLOY.md) — хостинг, CI/CD, бэкапы, мониторинг

## Требования

* Go 1.25+ (точная версия — в `go.mod`, тулчейн подтянется автоматически)
* Docker и Docker Compose (для Postgres и интеграционных тестов)
* [go-task](https://taskfile.dev) — вместо `make`:

  ```sh
  go install github.com/go-task/task/v3/cmd/task@latest
  ```

## Быстрый старт

```sh
cp .env.example .env     # и вписать BOT_TOKEN от @BotFather
task up                  # поднять Postgres
task migrate             # накатить схему
task run                 # запустить бота
```

`task --list` покажет все доступные задачи.

### Два бота вместо одного

Заведите у [@BotFather](https://t.me/BotFather) **два** бота: отдельный для разработки
и отдельный для прода. Telegram отдаёт апдейты по токену ровно одному потребителю —
если локальный процесс и прод стартуют с одним токеном, `getUpdates` начнёт возвращать
`409 Conflict`, а сообщения теряться то у одного, то у другого.

## Разработка

```sh
task lint             # golangci-lint
task test             # юнит-тесты, Docker не нужен
task test:integration # интеграционные тесты (поднимут контейнер с Postgres)
task ci               # всё то же, что прогоняет GitHub Actions
```

## Структура

```
cmd/bot         точка входа бота
cmd/seeder      загрузка встроенных словарей
internal/domain сущности и правила обучения, без внешних зависимостей
internal/usecase сценарии и порты (интерфейсы репозиториев)
internal/adapter telegram, postgres, i18n, csv
internal/infra   конфигурация, логи, пул БД, планировщик
migrations       SQL-миграции (goose)
locales          переводы интерфейса
seeds            встроенные словари
```

Правило зависимостей: `adapter`/`infra` → `usecase` → `domain`. Обратных стрелок нет.
