# Integrasi

> Apa terhubung ke apa: dari mana data berasal, ke mana data mendarat, di mana data dimodelkan, dan di mana semuanya berjalan.

*https://brevis.sh/id/docs/integrations/ · brevis.sh docs (id)*

---

**Apa yang sudah Anda pakai, dan tidak ada yang belum ada.** Sebuah driver muncul di sini hanya ketika sudah punya kode di repositori — tidak pernah sebelumnya.

Setiap baris menyebutkan bagian mana yang menjangkaunya, lewat paket atau sink apa, dan ke mana harus pergi untuk melihat caranya.

## Mengambil dari

|  | cara menjangkaunya | panduan |
|---|---|---|
| **HTTP** | SDK: `from.HTTP` | [Go SDK](/id/docs/sdk/#sources-and-destinations) |
| **Postgres** | SDK: `from/postgres` | [Go SDK](/id/docs/sdk/#sources-and-destinations) |
| **MySQL** | SDK: `from/mysql` | [Go SDK](/id/docs/sdk/#sources-and-destinations) |
| **file** | SDK: `from.Files` — lokal, S3, GCS | [Go SDK](/id/docs/sdk/#sources-and-destinations) |

## Mendarat di

|  | cara menjangkaunya | panduan |
|---|---|---|
| **BigQuery** | SDK: `to/bigquery` · Gateway: `bigquery` | [Go SDK](/id/docs/sdk/#sources-and-destinations), [Sink ingestion](/id/docs/ingestion-sinks/index.md) |
| **Postgres** | SDK: `to/postgres` · Gateway: `postgres` | [Go SDK](/id/docs/sdk/#sources-and-destinations), [Sink ingestion](/id/docs/ingestion-sinks/index.md) |
| **MySQL** | SDK: `to/mysql` · Gateway: `mysql` | [Go SDK](/id/docs/sdk/#sources-and-destinations), [Sink ingestion](/id/docs/ingestion-sinks/index.md) |
| **Redshift** | SDK: `to/redshift` · Gateway: `redshift` | [Go SDK](/id/docs/sdk/#sources-and-destinations), [Sink ingestion](/id/docs/ingestion-sinks/index.md) |
| **Pub/Sub** | SDK: `to/pubsub` · Gateway: `pubsub` | [Go SDK](/id/docs/sdk/#sources-and-destinations), [Sink ingestion](/id/docs/ingestion-sinks/index.md) |

## File masuk ke

|  | cara menjangkaunya | panduan |
|---|---|---|
| **S3** | SDK: `store/s3` · Gateway: `files` pada `s3://` | [Go SDK](/id/docs/sdk/#sources-and-destinations), [Sink ingestion](/id/docs/ingestion-sinks/index.md) |
| **GCS** | SDK: `store/gcs` · Gateway: `files` pada `gs://` | [Go SDK](/id/docs/sdk/#sources-and-destinations), [Sink ingestion](/id/docs/ingestion-sinks/index.md) |

## Dimodelkan di

|  | cara menjangkaunya | panduan |
|---|---|---|
| **Postgres** | SQL: `--dialect postgres` (default) | [SQL](/id/docs/sql/index.md) |
| **BigQuery** | SQL: `--dialect bigquery` | [SQL](/id/docs/sql/index.md) |

## Berjalan di

|  | cara menjangkaunya | panduan |
|---|---|---|
| **Docker** | satu image per bagian: `areteacademy/brevis`, `brevis-gateway`, `brevis-sql` | [Instalasi](/id/docs/installation/#docker) |
| **Kubernetes** | satu pod per step | [Kubernetes](/id/docs/kubernetes/index.md), [Pod per step](/id/docs/pod-per-step/index.md) |
| **Helm** | chart di `deployments/helm/brevis` | [Kubernetes](/id/docs/kubernetes/#install-it-with-helm) |

## Ditulis dengan

|  | cara menjangkaunya | panduan |
|---|---|---|
| **Go** | Core, SDK, Gateway, dan SQL | [Ekosistem](/id/docs/ecosystem/index.md) |
| **Python** | `pip install brevis`: konteks antar step di dalam step Python | [Python](/id/docs/python/index.md) |
| **SQL** | model brevis-sql adalah file `.sql` biasa | [SQL](/id/docs/sql/index.md) |

## Cloud

|  | cara menjangkaunya | panduan |
|---|---|---|
| **Google Cloud** | BigQuery, Pub/Sub, dan GCS, di atas | — |
| **AWS** | Redshift dan S3, di atas | — |
