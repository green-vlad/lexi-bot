# lexi-bot — хостинг, CI/CD и эксплуатация

Дополнение к [PLAN.md](./PLAN.md) §12. Здесь — где всё это живёт и как туда попадает код.

---

## 1. Что именно нужно хостить

Прежде чем сравнивать площадки, зафиксируем требования — они отсекают половину вариантов:

| Требование | Следствие |
|---|---|
| Long polling — процесс постоянно висит на `getUpdates` | **Не подходят** платформы со scale-to-zero и лимитом времени выполнения (Vercel, Cloud Functions, Deno Deploy). Нужен обычный always-on процесс |
| Публичный HTTPS не нужен | Домен, TLS и реверс-прокси не требуются вообще. `/healthz` и `/metrics` слушаем на локальном интерфейсе |
| Исходящие соединения к `api.telegram.org` | Площадка должна иметь стабильный доступ к Telegram API |
| Postgres на 100 пользователей | Объём данных — сотни мегабайт вместе со словарями. Любой самый дешёвый тариф избыточен |
| Один инстанс | Два процесса с одним токеном ломают друг друга (см. §6). Горизонтальное масштабирование не нужно и вредно |

Итог: нужен один маленький always-on контейнер и одна маленькая база. Это самая
дешёвая и скучная конфигурация из возможных — важно не переплатить за то, что не нужно.

---

## 2. Сравнение вариантов

Цены — порядок величины на начало 2026 года, перед выбором стоит свериться с актуальными.

| Вариант | ~ в месяц | Плюсы | Минусы |
|---|---|---|---|
| **VPS (Hetzner CX22) + Postgres в Docker на том же хосте** | ~€4–5 | Дешевле всего, полный контроль, нет вендор-лока, база рядом с приложением (нулевая сетевая задержка) | ОС и бэкапы на вас, деплой надо настроить руками (один раз) |
| **Render / Railway (приложение + управляемый Postgres)** | ~$12–15 | Ноль администрирования, деплой из GitHub «из коробки», бэкапы БД включены | Дороже втрое, вендор-лок, меньше контроля над версиями |
| **Fly.io + Neon/управляемый Postgres** | ~$8–15 | Хорош для длительно живущих процессов, удобный CLI, регионы | Дороже VPS, сложнее модель биллинга; у Neon — лимит соединений и autosuspend |
| **VPS + внешний управляемый Postgres (Neon free / Supabase)** | ~€4 + $0 | База с бэкапами и без администрирования, бесплатный тариф покрывает наш объём | Сетевая задержка до БД на каждом запросе, лимиты бесплатного тарифа, две площадки вместо одной |

### Рекомендация

**VPS Hetzner CX22 (2 vCPU / 4 ГБ / 40 ГБ) + Postgres в Docker Compose на том же хосте,
образ приложения из GHCR, деплой по SSH из GitHub Actions.**

Почему именно так:

1. **Цена честно отражает нагрузку.** ~€4,5/мес против ~$14 у PaaS за ресурсы, которые
   мы всё равно не используем. Разница за год — стоимость самого VPS на три года вперёд.
2. **База на том же хосте — это преимущество, а не компромисс.** Учебная сессия делает
   несколько запросов подряд на каждое нажатие кнопки; через unix-сеть внутри Docker это
   доли миллисекунды, через интернет до управляемой БД — десятки миллисекунд на запрос.
3. **Нет вендор-лока.** Всё описано в `docker-compose.prod.yml`; переезд на другую площадку —
   это `scp` файла и восстановление дампа.
4. **Администрирования почти нет.** Unattended-upgrades для ОС, автоперезапуск контейнеров,
   бэкап по cron. Полчаса настройки один раз.

Что вы отдаёте взамен: бэкапы и обновления ОС ваша ответственность (§8 описывает, как это
автоматизировать), и нет кнопки «откатить базу на вчера» из веб-панели.

**Если администрировать сервер не хочется вообще** — берите Render: приложение как
Background Worker (не Web Service, публичный порт нам не нужен) + их управляемый Postgres.
Меняется при этом ровно одна вещь — job `deploy` в workflow (см. §10). Всё остальное,
включая Dockerfile и compose для локальной разработки, остаётся как есть.

**Про размещение:** берите европейский регион (Hetzner — Nuremberg/Helsinki). Доступ
к `api.telegram.org` оттуда стабилен; с российских площадок к Telegram API возможны
перебои, и отлаживать их в проде — худшее место для сюрпризов.

---

## 3. Топология

```
GitHub (репозиторий)
   │  push в main
   ▼
GitHub Actions ── lint ── test ── govulncheck ── build
   │                                              │
   │                                    push образа в GHCR
   ▼                                              │
 SSH-деплой ──────────────────────────────────────┘
   │
   ▼
VPS (Hetzner, Docker Compose)
 ├─ lexi-bot        ← образ из ghcr.io, long polling, миграции при старте
 ├─ postgres:16     ← том с данными, порт наружу НЕ открыт
 └─ backup          ← ежедневный pg_dump → объектное хранилище (R2/B2)
```

Наружу открыт только SSH (`ufw`: 22/tcp). Порт Postgres не публикуется на хост вообще —
контейнеры общаются по внутренней сети Docker.

---

## 4. GitHub Actions: один workflow, четыре гейта

Один файл `.github/workflows/ci.yml`. Причина: деплой должен зависеть от результатов
линта и тестов, а `needs:` работает только внутри одного workflow. Связывать два файла
через `workflow_run` можно, но это лишняя хрупкость.

```yaml
name: CI

on:
  pull_request:
  push:
    branches: [main]

concurrency:
  group: ci-${{ github.ref }}
  cancel-in-progress: true

env:
  LINT_VERSION: v2.12.2

jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod   # одна точка правды с локальной разработкой
          cache: true
      - name: gofmt
        run: test -z "$(gofmt -l .)" || (gofmt -l . && exit 1)
      - uses: golangci/golangci-lint-action@v8
        with:
          version: ${{ env.LINT_VERSION }}   # та же версия, что в Taskfile.yml

  test:
    runs-on: ubuntu-latest
    services:
      postgres:
        image: postgres:16-alpine
        env:
          POSTGRES_USER: postgres
          POSTGRES_PASSWORD: postgres
          POSTGRES_DB: lexi_test
        ports: ["5432:5432"]
        options: >-
          --health-cmd pg_isready
          --health-interval 5s
          --health-timeout 5s
          --health-retries 10
    env:
      TEST_DATABASE_URL: postgres://postgres:postgres@localhost:5432/lexi_test?sslmode=disable
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod   # одна точка правды с локальной разработкой
          cache: true
      - name: Юнит-тесты
        run: go test -race -covermode=atomic -coverprofile=coverage.out ./...
      - name: Интеграционные тесты
        run: go test -race -tags=integration ./...
      - name: Покрытие
        run: go tool cover -func=coverage.out | tail -n 1
      - uses: actions/upload-artifact@v4
        with:
          name: coverage
          path: coverage.out

  vuln:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version-file: go.mod, cache: true }
      # govulncheck подключён директивой tool в go.mod — версия зафиксирована.
      - run: go tool govulncheck ./...

  image:
    needs: [lint, test, vuln, build]
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write
    steps:
      - uses: actions/checkout@v4
      - uses: docker/setup-buildx-action@v3
      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - uses: docker/metadata-action@v5
        id: meta
        with:
          images: ghcr.io/${{ github.repository }}
          tags: |
            type=sha,format=long
            type=raw,value=latest
      # Сначала сборка в локальный демон: образ нужно взвесить до публикации.
      - uses: docker/build-push-action@v6
        with:
          context: .
          load: true
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}
          cache-from: type=gha
          cache-to: type=gha,mode=max
      - name: Размер образа
        run: |
          size=$(docker image inspect ghcr.io/${{ github.repository }}:latest --format '{{.Size}}')
          [ "$size" -le "$IMAGE_MAX_BYTES" ] || { echo "образ вырос за бюджет: $size" >&2; exit 1; }
      # Второй вызов целиком попадает в кеш от первого.
      - uses: docker/build-push-action@v6
        if: github.ref == 'refs/heads/main'
        with:
          context: .
          push: true
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}
          cache-from: type=gha

  deploy:
    needs: image
    if: github.ref == 'refs/heads/main'
    runs-on: ubuntu-latest
    environment: production      # даёт возможность включить ручное подтверждение
    concurrency:
      group: deploy-production   # два деплоя не пересекутся
      cancel-in-progress: false
    steps:
      - uses: appleboy/ssh-action@v1
        with:
          host: ${{ secrets.DEPLOY_HOST }}
          username: ${{ secrets.DEPLOY_USER }}
          key: ${{ secrets.DEPLOY_SSH_KEY }}
          script: |
            set -euo pipefail
            cd /opt/lexi-bot
            echo "${{ secrets.GITHUB_TOKEN }}" | docker login ghcr.io -u ${{ github.actor }} --password-stdin
            docker compose -f docker-compose.prod.yml pull bot
            docker compose -f docker-compose.prod.yml up -d bot
            docker image prune -f
      - name: Проверка здоровья после деплоя
        uses: appleboy/ssh-action@v1
        with:
          host: ${{ secrets.DEPLOY_HOST }}
          username: ${{ secrets.DEPLOY_USER }}
          key: ${{ secrets.DEPLOY_SSH_KEY }}
          script: |
            for i in $(seq 1 30); do
              if curl -fsS http://127.0.0.1:8080/healthz > /dev/null; then
                echo "OK"; exit 0
              fi
              sleep 2
            done
            echo "healthz не ответил, смотрите логи"; docker compose logs --tail=50 bot; exit 1
```

Важные детали, которые легко упустить:

* **`cancel-in-progress: true` для CI, но `false` для деплоя.** Прерванный на середине
  деплой оставит систему в непонятном состоянии.
* **Интеграционные тесты идут в `services:`, а не в testcontainers.** Поднимать Docker
  внутри раннера ради того, что раннер и так умеет, — лишние 20–30 секунд на каждый прогон.
  Чтобы одни и те же тесты работали и локально, и в CI, хелпер `test/pgtest` читает
  `TEST_DATABASE_URL`: если переменная задана — использует эту БД, если нет — поднимает
  testcontainer. Это правка в T-013.
* **Проверка `/healthz` после деплоя** превращает «workflow зелёный» в «бот реально работает».
  Без неё упавший при старте контейнер выглядит как успешный деплой.
* **Гейт на ветку.** `lint`/`test`/`vuln` идут на каждый PR, `deploy` — только с `main`.
  Образ собирается тоже на каждом PR (сломанный `Dockerfile` должен падать до мержа,
  а не после), но публикуется в GHCR и уезжает на прод только с `main`.
  На `main` стоит включить branch protection с обязательными проверками.

### Образ

`Dockerfile` двухстадийный: тулчейн Go остаётся в стадии сборки, в образ едет только
результат. Решения, которые стоит помнить:

* **`CGO_ENABLED=0`.** Статический бинарник ничего не требует от базового образа —
  ни libc, ни динамического загрузчика. Именно это позволяет взять distroless static.
* **`gcr.io/distroless/static-debian12:nonroot`.** Ни шелла, ни пакетного менеджера:
  в контейнер, куда нечем зайти, незачем и лезть. Из полезного там ровно то, что нужно
  боту, — корневые сертификаты для HTTPS к Bot API. База таймзон не нужна, она вшита
  в бинарник (`import _ "time/tzdata"`), как и словари, переводы и миграции.
* **Два бинарника, `bot` и `seeder`.** Словари загружаются на каждом выкате отдельным
  запуском того же образа (`docker compose run --rm seeder`); отдельная сборка ради
  десяти мегабайт была бы лишней сущностью.
* **`HTTP_ADDR=0.0.0.0:8080` в образе.** Значение по умолчанию (`127.0.0.1:8080`)
  внутри контейнера означает «недоступен вообще»: loopback у контейнера свой. Наружу
  порт открывает не образ, а публикация в compose — и только на `127.0.0.1` хоста.
* **Бюджет размера — 30 МБ**, около 28 из них уже заняты. Гейт стоит и в CI
  (`IMAGE_MAX_BYTES`), и локально (`task image:size`): распухший образ — верный признак,
  что в сборку заехало что-то лишнее, и заметить это лучше до публикации.

Кеш сборки — `type=gha`, `mode=max` (сохраняются и промежуточные слои). Пока `go.mod`
и `go.sum` не менялись, модули в CI не выкачиваются заново.

### Taskfile и CI

Сборочные команды живут в `Taskfile.yml` ([go-task](https://taskfile.dev)), а не в Makefile.
Чтобы CI и локальная разработка не разъезжались, шаги workflow вызывают те же задачи:

```yaml
      - uses: arduino/setup-task@v2
        with:
          version: 3.x
          repo-token: ${{ secrets.GITHUB_TOKEN }}
      - run: task test
```

Тогда `task ci` локально прогоняет ровно то же, что и GitHub Actions, и «у меня всё
проходило» перестаёт быть отдельным жанром отладки.

Одно исключение — линт: в CI оставляем `golangci-lint-action`, потому что он сам кеширует
и линтер, и его результаты. Задача `task lint` для локального запуска при этом никуда
не девается — просто в workflow она не вызывается.

### Dependabot

`.github/dependabot.yml` на два экосистемы — `gomod` и `github-actions`, еженедельно.
Второе важнее, чем кажется: устаревшие action'ы — типовой источник поломок CI.

---

## 5. Секреты

| Где | Что |
|---|---|
| GitHub → Settings → Secrets → Actions | `DEPLOY_HOST`, `DEPLOY_USER`, `DEPLOY_SSH_KEY` |
| VPS, `/opt/lexi-bot/.env`, права `600` | `BOT_TOKEN`, `POSTGRES_PASSWORD`, `ADMIN_CHAT_ID`, `BACKUP_*` |
| Локально | `.env` из `.env.example`, в `.gitignore` |

`BOT_TOKEN` в GitHub Secrets класть **не нужно** — CI его не использует, а лишний секрет —
лишняя поверхность. Токен живёт только на сервере.

SSH-ключ для деплоя — отдельный, не ваш личный: заводим пользователя `deploy` с доступом
только к `/opt/lexi-bot` и к группе `docker`, ключ без пароля, в `authorized_keys`
ограничиваем `from=` по возможности.

---

## 6. Два бота: dev и prod

Telegram отдаёт апдейты по токену ровно одному потребителю. Если локальный процесс и
прод поднимутся с одним токеном, `getUpdates` начнёт возвращать `409 Conflict`, а сообщения —
теряться то у одного, то у другого. Отладить это тяжело, потому что выглядит как
«бот иногда не отвечает».

Поэтому: **два бота у @BotFather** — `@lexi_dev_bot` для локальной разработки и тестов,
`@lexi_bot` для прода. Разные токены, разные базы. Это не необязательная гигиена,
а условие того, что локальная разработка вообще возможна.

---

## 7. Миграции при деплое

Миграции накатываются приложением при старте (goose берёт собственную блокировку в БД).
При одном инстансе гонок нет.

Правило, которое делает откат безопасным: **миграции должны быть совместимы с предыдущей
версией кода.** Не переименовываем и не удаляем колонку в том же релизе, где меняется код —
сначала добавляем новую и пишем в обе, следующим релизом убираем старую. Тогда откат —
это просто `docker compose up -d` с предыдущим тегом образа, без возни с базой.

Откат: образы тегируются полным SHA коммита, поэтому
`docker compose -f docker-compose.prod.yml up -d` с `IMAGE_TAG=<sha>` в `.env` возвращает
любую предыдущую версию за секунды.

---

## 8. Бэкапы

Контейнер `backup` в том же compose: ежедневный `pg_dump -Fc` → `rclone` в объектное
хранилище (Cloudflare R2 или Backblaze B2, на нашем объёме — центы в месяц).

* Расписание: ежедневно в 04:00 UTC.
* Хранение: 14 ежедневных + 8 недельных.
* Локальная копия последнего дампа остаётся на диске — быстрый откат без сети.
* **Раз в месяц — учебное восстановление** в отдельную базу и запуск интеграционных тестов
  против неё. Бэкап, который ни разу не разворачивали, бэкапом не является.
* Об ошибке бэкапа бот пишет в админский чат (см. §9) — молчаливо падающий бэкап хуже, чем никакого.

---

## 9. Мониторинг

Держим на минимальном уровне, соответствующем масштабу:

* **`/healthz`** (пул БД + время последнего успешного цикла polling) — внешний пинг
  через UptimeRobot / BetterStack, бесплатных тарифов хватает.
* **Алерты в Telegram.** У нас уже есть бот — он и сообщает в админский чат (`ADMIN_CHAT_ID`)
  о панике, ошибках миграций, провале бэкапа и о серии ошибок Telegram API. Никакой
  отдельной инфраструктуры оповещений разворачивать не нужно.
* **`/metrics`** — Prometheus-формат; на старте достаточно смотреть их вручную,
  Grafana Cloud (бесплатный тариф) подключается позже, если понадобится.
* **Логи** — `docker compose logs` плюс ротация json-драйвера (`max-size: 10m`, `max-file: 3`),
  иначе диск однажды кончится.

---

## 10. Что меняется при выборе PaaS

Если вместо VPS берём Render/Railway/Fly, из workflow **удаляется job `deploy`**
(деплой по push делает сама платформа), а `image` остаётся или тоже удаляется, если
платформа собирает образ у себя. Джобы `lint`, `test`, `vuln` не меняются вообще.

На стороне приложения меняется одно: `DATABASE_URL` указывает на управляемую БД, и к нему
добавляется `sslmode=require`. Так как строка подключения и так приходит из окружения,
правок в коде нет.

Отдельно для Render: сервис заводится как **Background Worker**, а не Web Service.
Web Service требует слушать публичный порт и будет считаться нездоровым, ведь наш бот
никакого публичного HTTP не отдаёт.
