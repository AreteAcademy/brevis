# 集成

> 什么与什么相连：数据从哪里来，落到哪里，在哪里建模，以及这一切在哪里运行。

*https://brevis.sh/zh/docs/integrations/ · brevis.sh docs (zh-CN)*

---

**你已经在用的，以及仅限已经存在的。** 只有当仓库里有了对应的代码，某个驱动才会出现在这里，绝不提前列出。

每一行说明由哪个组成部分、通过哪个包或 sink 连接它，以及去哪里查看具体做法。

## 数据来源

|  | 接入方式 | 指南 |
|---|---|---|
| **HTTP** | SDK：`from.HTTP` | [Go SDK](/zh/docs/sdk/#sources-and-destinations) |
| **Postgres** | SDK：`from/postgres` | [Go SDK](/zh/docs/sdk/#sources-and-destinations) |
| **MySQL** | SDK：`from/mysql` | [Go SDK](/zh/docs/sdk/#sources-and-destinations) |
| **文件** | SDK：`from.Files`，本地、S3、GCS | [Go SDK](/zh/docs/sdk/#sources-and-destinations) |

## 数据落地

|  | 接入方式 | 指南 |
|---|---|---|
| **BigQuery** | SDK：`to/bigquery` · Gateway：`bigquery` | [Go SDK](/zh/docs/sdk/#sources-and-destinations)、[摄取 sink](/zh/docs/ingestion-sinks/index.md) |
| **Postgres** | SDK：`to/postgres` · Gateway：`postgres` | [Go SDK](/zh/docs/sdk/#sources-and-destinations)、[摄取 sink](/zh/docs/ingestion-sinks/index.md) |
| **MySQL** | SDK：`to/mysql` · Gateway：`mysql` | [Go SDK](/zh/docs/sdk/#sources-and-destinations)、[摄取 sink](/zh/docs/ingestion-sinks/index.md) |
| **Redshift** | SDK：`to/redshift` · Gateway：`redshift` | [Go SDK](/zh/docs/sdk/#sources-and-destinations)、[摄取 sink](/zh/docs/ingestion-sinks/index.md) |
| **Pub/Sub** | SDK：`to/pubsub` · Gateway：`pubsub` | [Go SDK](/zh/docs/sdk/#sources-and-destinations)、[摄取 sink](/zh/docs/ingestion-sinks/index.md) |

## 文件存储

|  | 接入方式 | 指南 |
|---|---|---|
| **S3** | SDK：`store/s3` · Gateway：`s3://` 上的 `files` | [Go SDK](/zh/docs/sdk/#sources-and-destinations)、[摄取 sink](/zh/docs/ingestion-sinks/index.md) |
| **GCS** | SDK：`store/gcs` · Gateway：`gs://` 上的 `files` | [Go SDK](/zh/docs/sdk/#sources-and-destinations)、[摄取 sink](/zh/docs/ingestion-sinks/index.md) |

## 建模所在

|  | 接入方式 | 指南 |
|---|---|---|
| **Postgres** | SQL：`--dialect postgres`（默认值） | [SQL](/zh/docs/sql/index.md) |
| **BigQuery** | SQL：`--dialect bigquery` | [SQL](/zh/docs/sql/index.md) |

## 运行环境

|  | 接入方式 | 指南 |
|---|---|---|
| **Docker** | 每个组成部分一个镜像：`areteacademy/brevis`、`brevis-gateway`、`brevis-sql` | [安装](/zh/docs/installation/#docker) |
| **Kubernetes** | 每个 step 一个 pod | [Kubernetes](/zh/docs/kubernetes/index.md)、[每个 step 一个 pod](/zh/docs/pod-per-step/index.md) |
| **Helm** | `deployments/helm/brevis` 中的 chart | [Kubernetes](/zh/docs/kubernetes/#install-it-with-helm) |

## 使用的语言

|  | 接入方式 | 指南 |
|---|---|---|
| **Go** | Core、SDK、Gateway 和 SQL | [生态](/zh/docs/ecosystem/index.md) |
| **Python** | `pip install brevis`：在 Python step 中传递 step 之间的上下文 | [Python](/zh/docs/python/index.md) |
| **SQL** | brevis-sql 的模型就是普通的 `.sql` 文件 | [SQL](/zh/docs/sql/index.md) |

## 云平台

|  | 接入方式 | 指南 |
|---|---|---|
| **Google Cloud** | 上文的 BigQuery、Pub/Sub 和 GCS | — |
| **AWS** | 上文的 Redshift 和 S3 | — |
