# Integrations

> What connects to what: where data comes from, where it lands, where it is modelled and where it all runs.

*https://brevis.sh/en/docs/integrations/ · brevis.sh docs (en)*

---

**What you already use, and nothing that does not exist yet.** A driver appears here when it gets code in the repository — never before.

Each row says which piece reaches it, through which package or sink, and where to go to see how.

## Pulls from

|  | how it gets there | guide |
|---|---|---|
| **HTTP** | SDK: `from.HTTP` | [Go SDK](/en/docs/sdk/#sources-and-destinations) |
| **Postgres** | SDK: `from/postgres` | [Go SDK](/en/docs/sdk/#sources-and-destinations) |
| **MySQL** | SDK: `from/mysql` | [Go SDK](/en/docs/sdk/#sources-and-destinations) |
| **files** | SDK: `from.Files` — local, S3, GCS | [Go SDK](/en/docs/sdk/#sources-and-destinations) |

## Lands in

|  | how it gets there | guide |
|---|---|---|
| **BigQuery** | SDK: `to/bigquery` · Gateway: `bigquery` | [Go SDK](/en/docs/sdk/#sources-and-destinations), [Ingestion sinks](/en/docs/ingestion-sinks/index.md) |
| **Postgres** | SDK: `to/postgres` · Gateway: `postgres` | [Go SDK](/en/docs/sdk/#sources-and-destinations), [Ingestion sinks](/en/docs/ingestion-sinks/index.md) |
| **MySQL** | SDK: `to/mysql` · Gateway: `mysql` | [Go SDK](/en/docs/sdk/#sources-and-destinations), [Ingestion sinks](/en/docs/ingestion-sinks/index.md) |
| **Redshift** | SDK: `to/redshift` · Gateway: `redshift` | [Go SDK](/en/docs/sdk/#sources-and-destinations), [Ingestion sinks](/en/docs/ingestion-sinks/index.md) |
| **Pub/Sub** | SDK: `to/pubsub` · Gateway: `pubsub` | [Go SDK](/en/docs/sdk/#sources-and-destinations), [Ingestion sinks](/en/docs/ingestion-sinks/index.md) |

## Files in

|  | how it gets there | guide |
|---|---|---|
| **S3** | SDK: `store/s3` · Gateway: `files` on `s3://` | [Go SDK](/en/docs/sdk/#sources-and-destinations), [Ingestion sinks](/en/docs/ingestion-sinks/index.md) |
| **GCS** | SDK: `store/gcs` · Gateway: `files` on `gs://` | [Go SDK](/en/docs/sdk/#sources-and-destinations), [Ingestion sinks](/en/docs/ingestion-sinks/index.md) |

## Models in

|  | how it gets there | guide |
|---|---|---|
| **Postgres** | SQL: `--dialect postgres` (the default) | [SQL](/en/docs/sql/index.md) |
| **BigQuery** | SQL: `--dialect bigquery` | [SQL](/en/docs/sql/index.md) |

## Runs on

|  | how it gets there | guide |
|---|---|---|
| **Docker** | one image per piece: `areteacademy/brevis`, `brevis-gateway`, `brevis-sql` | [Installation](/en/docs/installation/#docker) |
| **Kubernetes** | one pod per step | [Kubernetes](/en/docs/kubernetes/index.md), [Pod per step](/en/docs/pod-per-step/index.md) |
| **Helm** | the chart in `deployments/helm/brevis` | [Kubernetes](/en/docs/kubernetes/#install-it-with-helm) |

## Written in

|  | how it gets there | guide |
|---|---|---|
| **Go** | Core, SDK, Gateway and SQL | [Ecosystem](/en/docs/ecosystem/index.md) |
| **Python** | `pip install brevis`: context between steps in a Python step | [Python](/en/docs/python/index.md) |
| **SQL** | brevis-sql's models are plain `.sql` files | [SQL](/en/docs/sql/index.md) |

## Clouds

|  | how it gets there | guide |
|---|---|---|
| **Google Cloud** | BigQuery, Pub/Sub and GCS, above | — |
| **AWS** | Redshift and S3, above | — |
