# Integrationen

> Was womit verbunden ist: woher die Daten kommen, wo sie landen, wo sie modelliert werden und wo alles läuft.

*https://brevis.sh/de/docs/integrations/ · brevis.sh docs (de)*

---

**Was Sie bereits nutzen, und nichts, was es noch nicht gibt.** Ein Treiber erscheint hier erst, wenn er Code im Repository hat – nie früher.

Jede Zeile nennt, welcher Baustein ihn erreicht, über welches Paket oder welche Sink, und wohin Sie gehen, um zu sehen, wie.

## Liest aus

|  | Wie er erreicht wird | Anleitung |
|---|---|---|
| **HTTP** | SDK: `from.HTTP` | [Go-SDK](/de/docs/sdk/#sources-and-destinations) |
| **Postgres** | SDK: `from/postgres` | [Go-SDK](/de/docs/sdk/#sources-and-destinations) |
| **MySQL** | SDK: `from/mysql` | [Go-SDK](/de/docs/sdk/#sources-and-destinations) |
| **Dateien** | SDK: `from.Files` – lokal, S3, GCS | [Go-SDK](/de/docs/sdk/#sources-and-destinations) |

## Landet in

|  | Wie er erreicht wird | Anleitung |
|---|---|---|
| **BigQuery** | SDK: `to/bigquery` · Gateway: `bigquery` | [Go-SDK](/de/docs/sdk/#sources-and-destinations), [Ingestion-Sinks](/de/docs/ingestion-sinks/index.md) |
| **Postgres** | SDK: `to/postgres` · Gateway: `postgres` | [Go-SDK](/de/docs/sdk/#sources-and-destinations), [Ingestion-Sinks](/de/docs/ingestion-sinks/index.md) |
| **MySQL** | SDK: `to/mysql` · Gateway: `mysql` | [Go-SDK](/de/docs/sdk/#sources-and-destinations), [Ingestion-Sinks](/de/docs/ingestion-sinks/index.md) |
| **Redshift** | SDK: `to/redshift` · Gateway: `redshift` | [Go-SDK](/de/docs/sdk/#sources-and-destinations), [Ingestion-Sinks](/de/docs/ingestion-sinks/index.md) |
| **Pub/Sub** | SDK: `to/pubsub` · Gateway: `pubsub` | [Go-SDK](/de/docs/sdk/#sources-and-destinations), [Ingestion-Sinks](/de/docs/ingestion-sinks/index.md) |

## Dateien in

|  | Wie er erreicht wird | Anleitung |
|---|---|---|
| **S3** | SDK: `store/s3` · Gateway: `files` auf `s3://` | [Go-SDK](/de/docs/sdk/#sources-and-destinations), [Ingestion-Sinks](/de/docs/ingestion-sinks/index.md) |
| **GCS** | SDK: `store/gcs` · Gateway: `files` auf `gs://` | [Go-SDK](/de/docs/sdk/#sources-and-destinations), [Ingestion-Sinks](/de/docs/ingestion-sinks/index.md) |

## Modelliert in

|  | Wie er erreicht wird | Anleitung |
|---|---|---|
| **Postgres** | SQL: `--dialect postgres` (der Standard) | [SQL](/de/docs/sql/index.md) |
| **BigQuery** | SQL: `--dialect bigquery` | [SQL](/de/docs/sql/index.md) |

## Läuft auf

|  | Wie er erreicht wird | Anleitung |
|---|---|---|
| **Docker** | ein Image pro Baustein: `areteacademy/brevis`, `brevis-gateway`, `brevis-sql` | [Installation](/de/docs/installation/#docker) |
| **Kubernetes** | ein Pod pro Step | [Kubernetes](/de/docs/kubernetes/index.md), [Pod pro Step](/de/docs/pod-per-step/index.md) |
| **Helm** | das Chart in `deployments/helm/brevis` | [Kubernetes](/de/docs/kubernetes/#install-it-with-helm) |

## Geschrieben in

|  | Wie er erreicht wird | Anleitung |
|---|---|---|
| **Go** | Core, SDK, Gateway und SQL | [Ökosystem](/de/docs/ecosystem/index.md) |
| **Python** | `pip install brevis`: Kontext zwischen Steps in einem Python-Step | [Python](/de/docs/python/index.md) |
| **SQL** | die Modelle von brevis-sql sind einfache `.sql`-Dateien | [SQL](/de/docs/sql/index.md) |

## Clouds

|  | Wie er erreicht wird | Anleitung |
|---|---|---|
| **Google Cloud** | BigQuery, Pub/Sub und GCS, oben | — |
| **AWS** | Redshift und S3, oben | — |
