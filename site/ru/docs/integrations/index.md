# Интеграции

> Что с чем соединяется: откуда приходят данные, куда они попадают, где моделируются и где всё это работает.

*https://brevis.sh/ru/docs/integrations/ · brevis.sh docs (ru)*

---

**То, чем вы уже пользуетесь, и ничего из того, чего ещё нет.** Драйвер появляется здесь, только когда для него есть код в репозитории, и не раньше.

В каждой строке указано, какой компонент до него дотягивается, через какой пакет или sink, и куда пойти, чтобы посмотреть, как это делается.

## Откуда забирает

|  | как подключается | руководство |
|---|---|---|
| **HTTP** | SDK: `from.HTTP` | [Go SDK](/ru/docs/sdk/#источники-и-места-назначения) |
| **Postgres** | SDK: `from/postgres` | [Go SDK](/ru/docs/sdk/#источники-и-места-назначения) |
| **MySQL** | SDK: `from/mysql` | [Go SDK](/ru/docs/sdk/#источники-и-места-назначения) |
| **файлы** | SDK: `from.Files` — локальные, S3, GCS | [Go SDK](/ru/docs/sdk/#источники-и-места-назначения) |

## Куда записывает

|  | как подключается | руководство |
|---|---|---|
| **BigQuery** | SDK: `to/bigquery` · Gateway: `bigquery` | [Go SDK](/ru/docs/sdk/#источники-и-места-назначения), [Sinks приёма данных](/ru/docs/ingestion-sinks/index.md) |
| **Postgres** | SDK: `to/postgres` · Gateway: `postgres` | [Go SDK](/ru/docs/sdk/#источники-и-места-назначения), [Sinks приёма данных](/ru/docs/ingestion-sinks/index.md) |
| **MySQL** | SDK: `to/mysql` · Gateway: `mysql` | [Go SDK](/ru/docs/sdk/#источники-и-места-назначения), [Sinks приёма данных](/ru/docs/ingestion-sinks/index.md) |
| **Redshift** | SDK: `to/redshift` · Gateway: `redshift` | [Go SDK](/ru/docs/sdk/#источники-и-места-назначения), [Sinks приёма данных](/ru/docs/ingestion-sinks/index.md) |
| **Pub/Sub** | SDK: `to/pubsub` · Gateway: `pubsub` | [Go SDK](/ru/docs/sdk/#источники-и-места-назначения), [Sinks приёма данных](/ru/docs/ingestion-sinks/index.md) |

## Куда складывает файлы

|  | как подключается | руководство |
|---|---|---|
| **S3** | SDK: `store/s3` · Gateway: `files` на `s3://` | [Go SDK](/ru/docs/sdk/#источники-и-места-назначения), [Sinks приёма данных](/ru/docs/ingestion-sinks/index.md) |
| **GCS** | SDK: `store/gcs` · Gateway: `files` на `gs://` | [Go SDK](/ru/docs/sdk/#источники-и-места-назначения), [Sinks приёма данных](/ru/docs/ingestion-sinks/index.md) |

## Где моделирует

|  | как подключается | руководство |
|---|---|---|
| **Postgres** | SQL: `--dialect postgres` (по умолчанию) | [SQL](/ru/docs/sql/index.md) |
| **BigQuery** | SQL: `--dialect bigquery` | [SQL](/ru/docs/sql/index.md) |

## Где работает

|  | как подключается | руководство |
|---|---|---|
| **Docker** | один образ на компонент: `areteacademy/brevis`, `brevis-gateway`, `brevis-sql` | [Установка](/ru/docs/installation/#docker) |
| **Kubernetes** | один pod на шаг | [Kubernetes](/ru/docs/kubernetes/index.md), [Pod на шаг](/ru/docs/pod-per-step/index.md) |
| **Helm** | чарт в `deployments/helm/brevis` | [Kubernetes](/ru/docs/kubernetes/#установка-через-helm) |

## На чём написано

|  | как подключается | руководство |
|---|---|---|
| **Go** | Core, SDK, Gateway и SQL | [Экосистема](/ru/docs/ecosystem/index.md) |
| **Python** | `pip install brevis`: контекст между шагами внутри шага на Python | [Python](/ru/docs/python/index.md) |
| **SQL** | модели brevis-sql — обычные `.sql`-файлы | [SQL](/ru/docs/sql/index.md) |

## Облака

|  | как подключается | руководство |
|---|---|---|
| **Google Cloud** | BigQuery, Pub/Sub и GCS, см. выше | — |
| **AWS** | Redshift и S3, см. выше | — |
