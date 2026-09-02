# syntax=docker/dockerfile:1

# Многостадийная сборка: тулчейн Go остаётся в первой стадии, в образ едет
# только статический бинарник. Здесь закреплена мажорная версия Go; точный
# патч подтянет директива toolchain из go.mod — тот же механизм, что и при
# локальной сборке, так что образ и разработка не разъезжаются.
FROM golang:1.25 AS build

WORKDIR /src

# Зависимости — отдельным слоем: пока go.mod и go.sum не менялись, модули
# берутся из кеша и не выкачиваются заново на каждую правку кода.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# CGO выключен: без него получается статический бинарник, которому не нужен
# ни glibc, ни динамический загрузчик, — только такой и запустится на distroless
# static. Флаги те же, что в `task build`: -trimpath убирает пути сборочной
# машины, -s -w выбрасывают таблицу символов и DWARF (около трети размера).
# Словари, переводы и миграции вшиты через embed, поэтому копировать в образ
# нечего: всё уже внутри бинарников.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bot ./cmd/bot && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/seeder ./cmd/seeder

# distroless static: ни шелла, ни пакетного менеджера, ни libc — поверхность
# атаки сведена к самому приложению. Из полезного там ровно то, что нужно боту:
# корневые сертификаты для HTTPS к Bot API и запись nonroot в /etc/passwd.
# База таймзон не нужна — она вшита в бинарник (см. import time/tzdata).
FROM gcr.io/distroless/static-debian12:nonroot

# Бинарник seeder лежит рядом с ботом намеренно: словари загружаются на каждом
# выкате отдельным запуском того же образа (docker compose run --rm seeder),
# и таскать ради этого вторую сборку было бы странно.
COPY --from=build /out/bot /out/seeder /usr/local/bin/

# По умолчанию HTTP_ADDR слушает петлевой интерфейс, но внутри контейнера это
# означает «недоступен вообще»: у контейнера свой loopback. Наружу порт
# открывает не образ, а публикация в compose — на 127.0.0.1 хоста.
ENV HTTP_ADDR=0.0.0.0:8080
EXPOSE 8080

# Явно, хотя тег :nonroot уже переключает пользователя: строка ниже — часть
# контракта образа, и её не должно унести случайной сменой базового тега.
USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/bot"]
