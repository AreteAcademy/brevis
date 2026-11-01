---
title: Конфигурация
description: Все переменные окружения со значением по умолчанию, форматом и поведением при отсутствии.
group: Справочник
order: 10
slug: configuration
---

Вся конфигурация берётся из **окружения** и читается один раз при старте.
Позже окружение не опрашивается: процесс, чья конфигурация меняется на ходу, —
это процесс, поведение которого никто не сможет воспроизвести.

## Обязательные

| переменная | |
|---|---|
| `BREVIS_DATABASE_URL` | Postgres. Отсутствие = **ошибка при старте** в пяти подкомандах, использующих базу данных |

## Процесс

| переменная | по умолчанию | |
|---|---|---|
| `BREVIS_ENV` | `local` | `local` пишет журнал текстом и оставляет интерфейс открытым; любое другое значение требует учётных данных и пишет журнал в JSON |
| `BREVIS_HTTP_ADDR` | `:8080` | адрес, на котором принимаются соединения |
| `BREVIS_LOG_LEVEL` | `info` | |
| `BREVIS_SHUTDOWN_TIMEOUT_SECONDS` | `15` | целое число; нечисловое значение — ошибка при старте |
| `BREVIS_BRAND_FILE` | `brand.yaml` | брендинг; отсутствие = по умолчанию |
| `BREVIS_UI_URL` | — | основа ссылки на запуск в алертах |
| `BREVIS_METRICS_ADDR` | — | порт метрик Prometheus; пусто = не публикуются. См. [Наблюдаемость](/docs/observability/) |
| `BREVIS_SLACK_WEBHOOK` | — | адресат алертов о сбоях. Пусто = никто не уведомляется |

## Аутентификация

| переменная | |
|---|---|
| `BREVIS_AUTH_USER` | пользователь интерфейса |
| `BREVIS_AUTH_PASSWORD_HASH` | хеш `pbkdf2-sha256$…`, создаётся командой `brevis hash` |
| `BREVIS_AUTH_SECRET` | 32+ байта для подписи сессии (`openssl rand -base64 48`) |

Все три задаются вместе или не задаётся ни одна.

:::danger Наполовину настроенная аутентификация — ошибка при старте
Тот, кто указал имя пользователя, считает, что дверь закрыта. Интерфейс
запускает пайплайны: `POST /workflows/<slug>/trigger` выполняет `dbt build`,
который пишет в хранилище. Открытый в интернет, он становится пультом
дистанционного управления хранилищем, а предупреждение в журнале не
развеивает это убеждение, потому что журнал работающего процесса никто не
читает.
:::

Вне `BREVIS_ENV=local` запуск без учётных данных отклоняется:

```
error: BREVIS_ENV=production requires a credential: set BREVIS_AUTH_USER,
BREVIS_AUTH_PASSWORD_HASH (generate one with `brevis hash`) and BREVIS_AUTH_SECRET
```

:::note Старые имена по-прежнему работают
Четыре переменные были переименованы 10.09.2026, когда последние
португальские имена покинули сторону оператора в проекте:
`BREVIS_AUTH_USUARIO`, `BREVIS_AUTH_SENHA_HASH`, `BREVIS_AUTH_SEGREDO` и
`BREVIS_POD_MANTER_EM_FALHA`. Установка, использующая старое имя, продолжает
работать и пишет предупреждение с новым именем.

Добавьте новое имя, разверните, затем удалите старое — если заданы оба,
побеждает новое. Старые имена будут удалены в мажорной версии.

В SDK по тому же правилу `BREVIS_SDK_LIMITE_INLINE` стала
`BREVIS_SDK_INLINE_LIMIT`.
:::

## Окружение задачи

| переменная | по умолчанию | |
|---|---|---|
| `BREVIS_TASK_ENV` | пусто | что получает каждый шаг |

Задача **не** наследует окружение оркестратора. Причина вполне конкретна:
процесс содержит `BREVIS_DATABASE_URL` с именем пользователя и паролем
Postgres, а workflow — это произвольная команда, написанная кем-то другим;
наследование отдало бы учётные данные базы данных каждому шагу каждого
пайплайна.

То, что нужно задаче, объявляется явно:

```bash
BREVIS_TASK_ENV=GOOGLE_PROJECT_ID,STAGE   # forwards these two from the process
BREVIS_TASK_ENV=STAGE=prod                # sets a literal
BREVIS_TASK_ENV='*'                       # forwards everything EXCEPT BREVIS_*
```

`PATH` и `HOME` включены всегда: без `PATH` ни одна команда не находится, а
ошибка выглядела бы как «не найдено», которая ничего не объясняет.

Отсутствующее имя **не** превращается в пустую строку: `GOOGLE_PROJECT_ID=""`
привело бы к падению dbt с худшим сообщением, чем отсутствие переменной.

## Kubernetes

Решения об **установке**, а не о workflow.

| переменная | по умолчанию | формат |
|---|---|---|
| `BREVIS_PODS` | `auto` | `auto`, `on` или `off`; всё остальное — ошибка при старте |
| `BREVIS_POD_NAMESPACE` | пространство имён кластера | |
| `BREVIS_POD_SERVICE_ACCOUNT` | — | |
| `BREVIS_POD_PULL_SECRETS` | — | список через запятую |
| `BREVIS_POD_ENV_FROM_SECRETS` | — | список через запятую |
| `BREVIS_POD_ENV_FROM_CONFIGMAPS` | — | список через запятую |
| `BREVIS_POD_ALLOWED_SECRETS` | — | список через запятую |
| `BREVIS_POD_CREDENTIAL_PATH` | — | путь, смонтированный в pod |
| `BREVIS_POD_CREDENTIAL_PVC` | — | PVC с учётными данными |
| `BREVIS_POD_NODE_SELECTOR` | — | `key=value,other=value` |
| `BREVIS_POD_TOLERATIONS` | — | `key=value:effect,…`; только оператор `Equal` |
| `BREVIS_POD_KEEP_ON_FAILURE` | `false` | `true` сохраняет pod для изучения |

В списках пустые элементы игнорируются: `a,,b` — это опечатка, а пустое имя
секрета заставило бы сервер отклонить весь pod.

## Тесты

| переменная | |
|---|---|
| `BREVIS_TEST_DATABASE_URL` | база данных для интеграционных тестов; без неё они пропускаются |

## Эндпоинты проверки состояния

| | |
|---|---|
| `GET /health` | liveness — **не** обращается к базе данных |
| `GET /ready` | readiness — обращается и называет зависимость, которая отказала |

Разделение сделано намеренно: liveness-проба, зависящая от внешнего сервиса,
заставляет Kubernetes **убивать pod**, когда база данных даёт сбой, вместо
того чтобы просто убрать его из балансировщика нагрузки.

## Полный пример

```bash
# required
export BREVIS_DATABASE_URL='postgres://brevis:secret@db:5432/brevis?sslmode=require'

# production requires credentials
export BREVIS_ENV=production
export BREVIS_AUTH_USER=operator
export BREVIS_AUTH_PASSWORD_HASH="$(brevis hash < password.txt)"
export BREVIS_AUTH_SECRET="$(openssl rand -base64 48)"

# what the tasks need, and nothing more
export BREVIS_TASK_ENV=GOOGLE_PROJECT_ID,STAGE,DBT_KEYFILE

# pods
export BREVIS_PODS=on
export BREVIS_POD_NAMESPACE=data
export BREVIS_POD_SERVICE_ACCOUNT=brevis-runner
export BREVIS_POD_ENV_FROM_SECRETS=bigquery-cred

# alerts
export BREVIS_SLACK_WEBHOOK='https://hooks.slack.com/services/...'
export BREVIS_UI_URL='https://brevis.example.com'
```
