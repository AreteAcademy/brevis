# Установка

> Как получить бинарный файл, что для него нужно и как убедиться, что установка корректна.

*https://brevis.sh/ru/docs/installation/ · brevis.sh docs (ru)*

---

brevis.sh поставляется как **один бинарный файл** и как **образ Docker**.
Устанавливать рядом с ним какой-либо рантайм не нужно: миграции, ресурсы
интерфейса и шрифты находятся внутри бинарного файла.

## Требования

| | |
|---|---|
| **PostgreSQL** | нужен для `serve`, `scheduler`, `migrate`, `publish` и `backfill` |
| **Go 1.25+** | только если вы собираете из исходников |
| **Kubernetes** | необязателен — без кластера шаги запускаются как локальные процессы |

`brevis run`, `validate`, `marca`, `hash` и `version` **не требуют** базы данных.

## Сборка из исходников

Этот путь даёт бинарный файл с вшитыми версией и коммитом:

```bash
git clone https://github.com/AreteAcademy/brevis.git
cd brevis
make build
./bin/brevis version
```

```
brevis 0.11.2
  commit  bb832ff
  build   2026-09-05T12:00:00Z
  go      go1.25.7 darwin/arm64
```

:::warning У engine пока нет тегированного релиза
Все теги в репозитории принадлежат SDK (`sdk/v*`); у корневого модуля их нет.
Команда `go install github.com/AreteAcademy/brevis/cmd/brevis@latest`
разрешается в последний коммит ветки и даёт бинарный файл, который
представляется как `brevis dev`, без коммита и даты: `-ldflags` попадают в
сборку только через `make build` и `docker build`. Для прослеживаемой версии
используйте образ или `make build`.
:::

## Docker

Два образа одного и того же бинарного файла, потому что у двух ролей
противоположные потребности:

| тег | основа | зачем |
|---|---|---|
| `:0.11.2` | distroless | API только отдаёт HTTP и ничего не исполняет — без оболочки, минимальная поверхность |
| `:0.11.2-worker` | alpine + tini | шагам `run:` нужна оболочка |

```bash
docker run --rm areteacademy/brevis:latest version
```

Оба образа находятся на [hub.docker.com/r/areteacademy/brevis](https://hub.docker.com/r/areteacademy/brevis)
для `amd64` и `arm64`.

## Полное локальное окружение

В репозитории есть `docker-compose.yml`, который поднимает Postgres, API и
планировщик:

```bash
make up      # Postgres + API + scheduler
make smoke   # checks /health and /ready
make logs
make down
```

Интерфейс доступен по адресу `http://localhost:8080`.

## Минимальная конфигурация

Обязательна одна переменная:

```bash
export BREVIS_DATABASE_URL='postgres://brevis:brevis@localhost:5432/brevis?sslmode=disable'
```

Без неё подкоманды, использующие базу данных, падают при запуске — и это
сделано намеренно. Процесс, поднявшийся без учётных данных, обнаружит проблему
только на первом запросе, а к тому времени проверка готовности уже солжёт
оркестратору.

```
$ brevis migrate status
erro: BREVIS_DATABASE_URL e obrigatoria
```

Полный список приведён в разделе [Конфигурация](/ru/docs/configuration/index.md).

## Применение схемы

Миграции встроены в бинарный файл и применяются отдельной подкомандой —
**`serve` никогда не изменяет схему**:

```bash
brevis migrate up
brevis migrate status
```

## Проверка

```bash
brevis version
brevis validate examples/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    daily-report                 chain  3 steps, 2 dependencias  cron 0 2 * * *
  ok    hello                        dag  4 steps, 4 dependencias  (manual)
```

Если обе команды отвечают, установка корректна. Переходите к
[Быстрому старту](/ru/docs/quickstart/index.md).
