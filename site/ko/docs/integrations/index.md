# 통합

> 무엇이 무엇과 연결되는지: 데이터가 어디서 오고, 어디에 도착하고, 어디서 모델링되고, 전체가 어디서 실행되는지 정리합니다.

*https://brevis.sh/ko/docs/integrations/ · brevis.sh docs (ko)*

---

**이미 쓰고 있는 것은 모두, 아직 존재하지 않는 것은 하나도 없습니다.** 드라이버는 저장소에 코드가 들어온 뒤에야 여기에 나타나며, 그 전에는 나타나지 않습니다.

각 행은 어떤 구성 요소가 어떤 패키지나 sink를 통해 해당 대상에 연결되는지, 그리고 자세한 방법은 어디서 볼 수 있는지 알려 줍니다.

## 데이터 가져오기

|  | 연결 방법 | 가이드 |
|---|---|---|
| **HTTP** | SDK: `from.HTTP` | [Go SDK](/ko/docs/sdk/#sources-and-destinations) |
| **Postgres** | SDK: `from/postgres` | [Go SDK](/ko/docs/sdk/#sources-and-destinations) |
| **MySQL** | SDK: `from/mysql` | [Go SDK](/ko/docs/sdk/#sources-and-destinations) |
| **files** | SDK: `from.Files` — 로컬, S3, GCS | [Go SDK](/ko/docs/sdk/#sources-and-destinations) |

## 데이터 적재 대상

|  | 연결 방법 | 가이드 |
|---|---|---|
| **BigQuery** | SDK: `to/bigquery` · Gateway: `bigquery` | [Go SDK](/ko/docs/sdk/#sources-and-destinations), [수집 sink](/ko/docs/ingestion-sinks/index.md) |
| **Postgres** | SDK: `to/postgres` · Gateway: `postgres` | [Go SDK](/ko/docs/sdk/#sources-and-destinations), [수집 sink](/ko/docs/ingestion-sinks/index.md) |
| **MySQL** | SDK: `to/mysql` · Gateway: `mysql` | [Go SDK](/ko/docs/sdk/#sources-and-destinations), [수집 sink](/ko/docs/ingestion-sinks/index.md) |
| **Redshift** | SDK: `to/redshift` · Gateway: `redshift` | [Go SDK](/ko/docs/sdk/#sources-and-destinations), [수집 sink](/ko/docs/ingestion-sinks/index.md) |
| **Pub/Sub** | SDK: `to/pubsub` · Gateway: `pubsub` | [Go SDK](/ko/docs/sdk/#sources-and-destinations), [수집 sink](/ko/docs/ingestion-sinks/index.md) |

## 파일 적재 대상

|  | 연결 방법 | 가이드 |
|---|---|---|
| **S3** | SDK: `store/s3` · Gateway: `s3://`에 대한 `files` | [Go SDK](/ko/docs/sdk/#sources-and-destinations), [수집 sink](/ko/docs/ingestion-sinks/index.md) |
| **GCS** | SDK: `store/gcs` · Gateway: `gs://`에 대한 `files` | [Go SDK](/ko/docs/sdk/#sources-and-destinations), [수집 sink](/ko/docs/ingestion-sinks/index.md) |

## 모델링 대상

|  | 연결 방법 | 가이드 |
|---|---|---|
| **Postgres** | SQL: `--dialect postgres` (기본값) | [SQL](/ko/docs/sql/index.md) |
| **BigQuery** | SQL: `--dialect bigquery` | [SQL](/ko/docs/sql/index.md) |

## 실행 환경

|  | 연결 방법 | 가이드 |
|---|---|---|
| **Docker** | 구성 요소마다 이미지 하나: `areteacademy/brevis`, `brevis-gateway`, `brevis-sql` | [설치](/ko/docs/installation/#docker) |
| **Kubernetes** | step마다 pod 하나 | [Kubernetes](/ko/docs/kubernetes/index.md), [step당 pod 하나](/ko/docs/pod-per-step/index.md) |
| **Helm** | `deployments/helm/brevis`의 차트 | [Kubernetes](/ko/docs/kubernetes/#install-it-with-helm) |

## 작성 언어

|  | 연결 방법 | 가이드 |
|---|---|---|
| **Go** | Core, SDK, Gateway, SQL | [에코시스템](/ko/docs/ecosystem/index.md) |
| **Python** | `pip install brevis`: Python step에서 step 간 컨텍스트 전달 | [Python](/ko/docs/python/index.md) |
| **SQL** | brevis-sql의 모델은 일반 `.sql` 파일입니다 | [SQL](/ko/docs/sql/index.md) |

## 클라우드

|  | 연결 방법 | 가이드 |
|---|---|---|
| **Google Cloud** | 위의 BigQuery, Pub/Sub, GCS | — |
| **AWS** | 위의 Redshift, S3 | — |
