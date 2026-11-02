# Intégrations

> Ce qui se connecte à quoi : d'où viennent les données, où elles atterrissent, où elles sont modélisées et où tout s'exécute.

*https://brevis.sh/fr/docs/integrations/ · brevis.sh docs (fr)*

---

**Ce que vous utilisez déjà, et rien de ce qui n'existe pas encore.** Un driver n'apparaît ici que lorsqu'il a du code dans le dépôt — jamais avant.

Chaque ligne indique quel composant l'atteint, par quel package ou sink, et où aller pour voir comment.

## Lit depuis

|  | comment y accéder | guide |
|---|---|---|
| **HTTP** | SDK : `from.HTTP` | [SDK Go](/fr/docs/sdk/#sources-and-destinations) |
| **Postgres** | SDK : `from/postgres` | [SDK Go](/fr/docs/sdk/#sources-and-destinations) |
| **MySQL** | SDK : `from/mysql` | [SDK Go](/fr/docs/sdk/#sources-and-destinations) |
| **fichiers** | SDK : `from.Files` — local, S3, GCS | [SDK Go](/fr/docs/sdk/#sources-and-destinations) |

## Atterrit dans

|  | comment y accéder | guide |
|---|---|---|
| **BigQuery** | SDK : `to/bigquery` · Gateway : `bigquery` | [SDK Go](/fr/docs/sdk/#sources-and-destinations), [Sinks d'ingestion](/fr/docs/ingestion-sinks/index.md) |
| **Postgres** | SDK : `to/postgres` · Gateway : `postgres` | [SDK Go](/fr/docs/sdk/#sources-and-destinations), [Sinks d'ingestion](/fr/docs/ingestion-sinks/index.md) |
| **MySQL** | SDK : `to/mysql` · Gateway : `mysql` | [SDK Go](/fr/docs/sdk/#sources-and-destinations), [Sinks d'ingestion](/fr/docs/ingestion-sinks/index.md) |
| **Redshift** | SDK : `to/redshift` · Gateway : `redshift` | [SDK Go](/fr/docs/sdk/#sources-and-destinations), [Sinks d'ingestion](/fr/docs/ingestion-sinks/index.md) |
| **Pub/Sub** | SDK : `to/pubsub` · Gateway : `pubsub` | [SDK Go](/fr/docs/sdk/#sources-and-destinations), [Sinks d'ingestion](/fr/docs/ingestion-sinks/index.md) |

## Fichiers dans

|  | comment y accéder | guide |
|---|---|---|
| **S3** | SDK : `store/s3` · Gateway : `files` sur `s3://` | [SDK Go](/fr/docs/sdk/#sources-and-destinations), [Sinks d'ingestion](/fr/docs/ingestion-sinks/index.md) |
| **GCS** | SDK : `store/gcs` · Gateway : `files` sur `gs://` | [SDK Go](/fr/docs/sdk/#sources-and-destinations), [Sinks d'ingestion](/fr/docs/ingestion-sinks/index.md) |

## Modélise dans

|  | comment y accéder | guide |
|---|---|---|
| **Postgres** | SQL : `--dialect postgres` (la valeur par défaut) | [SQL](/fr/docs/sql/index.md) |
| **BigQuery** | SQL : `--dialect bigquery` | [SQL](/fr/docs/sql/index.md) |

## S'exécute sur

|  | comment y accéder | guide |
|---|---|---|
| **Docker** | une image par composant : `brevissh/brevis`, `brevis-gateway`, `brevis-sql` | [Installation](/fr/docs/installation/#docker) |
| **Kubernetes** | un pod par step | [Kubernetes](/fr/docs/kubernetes/index.md), [Un pod par step](/fr/docs/pod-per-step/index.md) |
| **Helm** | le chart dans `deployments/helm/brevis` | [Kubernetes](/fr/docs/kubernetes/#install-it-with-helm) |

## Écrit en

|  | comment y accéder | guide |
|---|---|---|
| **Go** | Core, SDK, Gateway et SQL | [Écosystème](/fr/docs/ecosystem/index.md) |
| **Python** | `pip install brevis` : contexte entre steps dans un step Python | [Python](/fr/docs/python/index.md) |
| **SQL** | les modèles de brevis-sql sont de simples fichiers `.sql` | [SQL](/fr/docs/sql/index.md) |

## Clouds

|  | comment y accéder | guide |
|---|---|---|
| **Google Cloud** | BigQuery, Pub/Sub et GCS, ci-dessus | — |
| **AWS** | Redshift et S3, ci-dessus | — |
