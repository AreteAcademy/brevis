# インテグレーション

> 何が何につながるか。データの取得元、着地先、モデリングの場所、そして全体が動く場所。

*https://brevis.sh/ja/docs/integrations/ · brevis.sh docs (ja)*

---

**すでに使っているものだけを載せ、まだ存在しないものは載せません。** ドライバがここに載るのは、リポジトリにコードが入った時点からです。それ以前には載りません。

各行は、どの構成要素が、どのパッケージまたは sink を通じてそこに到達するか、そして詳細をどこで確認できるかを示します。

## 取得元

|  | 接続方法 | ガイド |
|---|---|---|
| **HTTP** | SDK: `from.HTTP` | [Go SDK](/ja/docs/sdk/#sources-and-destinations) |
| **Postgres** | SDK: `from/postgres` | [Go SDK](/ja/docs/sdk/#sources-and-destinations) |
| **MySQL** | SDK: `from/mysql` | [Go SDK](/ja/docs/sdk/#sources-and-destinations) |
| **ファイル** | SDK: `from.Files` — ローカル、S3、GCS | [Go SDK](/ja/docs/sdk/#sources-and-destinations) |

## 着地先

|  | 接続方法 | ガイド |
|---|---|---|
| **BigQuery** | SDK: `to/bigquery` · Gateway: `bigquery` | [Go SDK](/ja/docs/sdk/#sources-and-destinations)、[取り込みの sink](/ja/docs/ingestion-sinks/index.md) |
| **Postgres** | SDK: `to/postgres` · Gateway: `postgres` | [Go SDK](/ja/docs/sdk/#sources-and-destinations)、[取り込みの sink](/ja/docs/ingestion-sinks/index.md) |
| **MySQL** | SDK: `to/mysql` · Gateway: `mysql` | [Go SDK](/ja/docs/sdk/#sources-and-destinations)、[取り込みの sink](/ja/docs/ingestion-sinks/index.md) |
| **Redshift** | SDK: `to/redshift` · Gateway: `redshift` | [Go SDK](/ja/docs/sdk/#sources-and-destinations)、[取り込みの sink](/ja/docs/ingestion-sinks/index.md) |
| **Pub/Sub** | SDK: `to/pubsub` · Gateway: `pubsub` | [Go SDK](/ja/docs/sdk/#sources-and-destinations)、[取り込みの sink](/ja/docs/ingestion-sinks/index.md) |

## ファイルの着地先

|  | 接続方法 | ガイド |
|---|---|---|
| **S3** | SDK: `store/s3` · Gateway: `s3://` 上の `files` | [Go SDK](/ja/docs/sdk/#sources-and-destinations)、[取り込みの sink](/ja/docs/ingestion-sinks/index.md) |
| **GCS** | SDK: `store/gcs` · Gateway: `gs://` 上の `files` | [Go SDK](/ja/docs/sdk/#sources-and-destinations)、[取り込みの sink](/ja/docs/ingestion-sinks/index.md) |

## モデリングの場所

|  | 接続方法 | ガイド |
|---|---|---|
| **Postgres** | SQL: `--dialect postgres`(デフォルト) | [SQL](/ja/docs/sql/index.md) |
| **BigQuery** | SQL: `--dialect bigquery` | [SQL](/ja/docs/sql/index.md) |

## 実行環境

|  | 接続方法 | ガイド |
|---|---|---|
| **Docker** | 構成要素ごとに 1 つのイメージ: `brevissh/brevis`、`brevis-gateway`、`brevis-sql` | [インストール](/ja/docs/installation/#docker) |
| **Kubernetes** | ステップごとに 1 つの pod | [Kubernetes](/ja/docs/kubernetes/index.md)、[ステップごとの pod](/ja/docs/pod-per-step/index.md) |
| **Helm** | `deployments/helm/brevis` にあるチャート | [Kubernetes](/ja/docs/kubernetes/#install-it-with-helm) |

## 実装言語

|  | 接続方法 | ガイド |
|---|---|---|
| **Go** | Core、SDK、Gateway、SQL | [エコシステム](/ja/docs/ecosystem/index.md) |
| **Python** | `pip install brevis`: Python ステップでステップ間のコンテキストを扱います | [Python](/ja/docs/python/index.md) |
| **SQL** | brevis-sql のモデルは通常の `.sql` ファイルです | [SQL](/ja/docs/sql/index.md) |

## クラウド

|  | 接続方法 | ガイド |
|---|---|---|
| **Google Cloud** | 上記の BigQuery、Pub/Sub、GCS | — |
| **AWS** | 上記の Redshift と S3 | — |
