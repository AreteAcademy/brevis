# Integraciones

> Qué se conecta con qué: de dónde vienen los datos, dónde aterrizan, dónde se modelan y dónde corre todo.

*https://brevis.sh/es/docs/integrations/ · brevis.sh docs (es)*

---

**Lo que ya usas, y nada que todavía no exista.** Un driver aparece aquí cuando recibe código en el repositorio, nunca antes.

Cada fila indica qué pieza llega a él, mediante qué paquete o sink, y adónde ir para ver cómo.

## Extrae de

|  | cómo llega | guía |
|---|---|---|
| **HTTP** | SDK: `from.HTTP` | [SDK de Go](/es/docs/sdk/#sources-and-destinations) |
| **Postgres** | SDK: `from/postgres` | [SDK de Go](/es/docs/sdk/#sources-and-destinations) |
| **MySQL** | SDK: `from/mysql` | [SDK de Go](/es/docs/sdk/#sources-and-destinations) |
| **archivos** | SDK: `from.Files` — local, S3, GCS | [SDK de Go](/es/docs/sdk/#sources-and-destinations) |

## Aterriza en

|  | cómo llega | guía |
|---|---|---|
| **BigQuery** | SDK: `to/bigquery` · Gateway: `bigquery` | [SDK de Go](/es/docs/sdk/#sources-and-destinations), [Sinks de ingesta](/es/docs/ingestion-sinks/index.md) |
| **Postgres** | SDK: `to/postgres` · Gateway: `postgres` | [SDK de Go](/es/docs/sdk/#sources-and-destinations), [Sinks de ingesta](/es/docs/ingestion-sinks/index.md) |
| **MySQL** | SDK: `to/mysql` · Gateway: `mysql` | [SDK de Go](/es/docs/sdk/#sources-and-destinations), [Sinks de ingesta](/es/docs/ingestion-sinks/index.md) |
| **Redshift** | SDK: `to/redshift` · Gateway: `redshift` | [SDK de Go](/es/docs/sdk/#sources-and-destinations), [Sinks de ingesta](/es/docs/ingestion-sinks/index.md) |
| **Pub/Sub** | SDK: `to/pubsub` · Gateway: `pubsub` | [SDK de Go](/es/docs/sdk/#sources-and-destinations), [Sinks de ingesta](/es/docs/ingestion-sinks/index.md) |

## Archivos en

|  | cómo llega | guía |
|---|---|---|
| **S3** | SDK: `store/s3` · Gateway: `files` sobre `s3://` | [SDK de Go](/es/docs/sdk/#sources-and-destinations), [Sinks de ingesta](/es/docs/ingestion-sinks/index.md) |
| **GCS** | SDK: `store/gcs` · Gateway: `files` sobre `gs://` | [SDK de Go](/es/docs/sdk/#sources-and-destinations), [Sinks de ingesta](/es/docs/ingestion-sinks/index.md) |

## Modela en

|  | cómo llega | guía |
|---|---|---|
| **Postgres** | SQL: `--dialect postgres` (el valor por defecto) | [SQL](/es/docs/sql/index.md) |
| **BigQuery** | SQL: `--dialect bigquery` | [SQL](/es/docs/sql/index.md) |

## Corre en

|  | cómo llega | guía |
|---|---|---|
| **Docker** | una imagen por pieza: `areteacademy/brevis`, `brevis-gateway`, `brevis-sql` | [Instalación](/es/docs/installation/#docker) |
| **Kubernetes** | un pod por step | [Kubernetes](/es/docs/kubernetes/index.md), [Pod por step](/es/docs/pod-per-step/index.md) |
| **Helm** | el chart en `deployments/helm/brevis` | [Kubernetes](/es/docs/kubernetes/#install-it-with-helm) |

## Escrito en

|  | cómo llega | guía |
|---|---|---|
| **Go** | Core, SDK, Gateway y SQL | [Ecosistema](/es/docs/ecosystem/index.md) |
| **Python** | `pip install brevis`: contexto entre steps en un step de Python | [Python](/es/docs/python/index.md) |
| **SQL** | los modelos de brevis-sql son archivos `.sql` simples | [SQL](/es/docs/sql/index.md) |

## Nubes

|  | cómo llega | guía |
|---|---|---|
| **Google Cloud** | BigQuery, Pub/Sub y GCS, arriba | — |
| **AWS** | Redshift y S3, arriba | — |
