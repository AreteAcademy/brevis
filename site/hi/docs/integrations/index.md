# इंटीग्रेशन

> क्या किससे जुड़ता है: डेटा कहाँ से आता है, कहाँ उतरता है, कहाँ मॉडल होता है और यह सब कहाँ चलता है।

*https://brevis.sh/hi/docs/integrations/ · brevis.sh docs (hi)*

---

**जो आप पहले से इस्तेमाल करते हैं, और कुछ भी ऐसा नहीं जो अभी मौजूद न हो।** कोई driver यहाँ तभी दिखता है जब रिपॉज़िटरी में उसका कोड आ जाता है — उससे पहले कभी नहीं।

हर पंक्ति बताती है कि कौन-सा हिस्सा उस तक पहुँचता है, किस पैकेज या sink के ज़रिए, और कैसे देखना है यह जानने के लिए कहाँ जाएँ।

## यहाँ से लेता है

|  | वहाँ कैसे पहुँचता है | गाइड |
|---|---|---|
| **HTTP** | SDK: `from.HTTP` | [Go SDK](/hi/docs/sdk/#sources-and-destinations) |
| **Postgres** | SDK: `from/postgres` | [Go SDK](/hi/docs/sdk/#sources-and-destinations) |
| **MySQL** | SDK: `from/mysql` | [Go SDK](/hi/docs/sdk/#sources-and-destinations) |
| **फ़ाइलें** | SDK: `from.Files` — स्थानीय, S3, GCS | [Go SDK](/hi/docs/sdk/#sources-and-destinations) |

## यहाँ उतरता है

|  | वहाँ कैसे पहुँचता है | गाइड |
|---|---|---|
| **BigQuery** | SDK: `to/bigquery` · Gateway: `bigquery` | [Go SDK](/hi/docs/sdk/#sources-and-destinations), [Ingestion sinks](/hi/docs/ingestion-sinks/index.md) |
| **Postgres** | SDK: `to/postgres` · Gateway: `postgres` | [Go SDK](/hi/docs/sdk/#sources-and-destinations), [Ingestion sinks](/hi/docs/ingestion-sinks/index.md) |
| **MySQL** | SDK: `to/mysql` · Gateway: `mysql` | [Go SDK](/hi/docs/sdk/#sources-and-destinations), [Ingestion sinks](/hi/docs/ingestion-sinks/index.md) |
| **Redshift** | SDK: `to/redshift` · Gateway: `redshift` | [Go SDK](/hi/docs/sdk/#sources-and-destinations), [Ingestion sinks](/hi/docs/ingestion-sinks/index.md) |
| **Pub/Sub** | SDK: `to/pubsub` · Gateway: `pubsub` | [Go SDK](/hi/docs/sdk/#sources-and-destinations), [Ingestion sinks](/hi/docs/ingestion-sinks/index.md) |

## फ़ाइलें यहाँ

|  | वहाँ कैसे पहुँचता है | गाइड |
|---|---|---|
| **S3** | SDK: `store/s3` · Gateway: `s3://` पर `files` | [Go SDK](/hi/docs/sdk/#sources-and-destinations), [Ingestion sinks](/hi/docs/ingestion-sinks/index.md) |
| **GCS** | SDK: `store/gcs` · Gateway: `gs://` पर `files` | [Go SDK](/hi/docs/sdk/#sources-and-destinations), [Ingestion sinks](/hi/docs/ingestion-sinks/index.md) |

## यहाँ मॉडल होता है

|  | वहाँ कैसे पहुँचता है | गाइड |
|---|---|---|
| **Postgres** | SQL: `--dialect postgres` (डिफ़ॉल्ट) | [SQL](/hi/docs/sql/index.md) |
| **BigQuery** | SQL: `--dialect bigquery` | [SQL](/hi/docs/sql/index.md) |

## यहाँ चलता है

|  | वहाँ कैसे पहुँचता है | गाइड |
|---|---|---|
| **Docker** | हर हिस्से के लिए एक image: `areteacademy/brevis`, `brevis-gateway`, `brevis-sql` | [इंस्टॉलेशन](/hi/docs/installation/#docker) |
| **Kubernetes** | हर step के लिए एक pod | [Kubernetes](/hi/docs/kubernetes/index.md), [हर step के लिए एक pod](/hi/docs/pod-per-step/index.md) |
| **Helm** | `deployments/helm/brevis` में मौजूद chart | [Kubernetes](/hi/docs/kubernetes/#install-it-with-helm) |

## इनमें लिखा गया

|  | वहाँ कैसे पहुँचता है | गाइड |
|---|---|---|
| **Go** | Core, SDK, Gateway और SQL | [इकोसिस्टम](/hi/docs/ecosystem/index.md) |
| **Python** | `pip install brevis`: Python step में steps के बीच संदर्भ | [Python](/hi/docs/python/index.md) |
| **SQL** | brevis-sql के मॉडल सादी `.sql` फ़ाइलें हैं | [SQL](/hi/docs/sql/index.md) |

## क्लाउड

|  | वहाँ कैसे पहुँचता है | गाइड |
|---|---|---|
| **Google Cloud** | BigQuery, Pub/Sub और GCS, ऊपर | — |
| **AWS** | Redshift और S3, ऊपर | — |
