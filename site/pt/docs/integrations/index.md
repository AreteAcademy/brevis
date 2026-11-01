# Integrações

> O que conecta com o quê: de onde o dado vem, onde pousa, onde é modelado e onde tudo roda.

*https://brevis.sh/pt/docs/integrations/ · brevis.sh docs (pt-BR)*

---

**O que você já usa, e nada que ainda não existe.** Um driver aparece aqui quando ganha código no repositório — nunca antes.

Cada linha diz por qual peça e por qual pacote ou destino aquilo é alcançado, e para onde ir para ver como.

## Busca em

|  | como chega | guia |
|---|---|---|
| **HTTP** | SDK: `from.HTTP` | [SDK em Go](/pt/docs/sdk/#sources-and-destinations) |
| **Postgres** | SDK: `from/postgres` | [SDK em Go](/pt/docs/sdk/#sources-and-destinations) |
| **MySQL** | SDK: `from/mysql` | [SDK em Go](/pt/docs/sdk/#sources-and-destinations) |
| **arquivos** | SDK: `from.Files` — local, S3, GCS | [SDK em Go](/pt/docs/sdk/#sources-and-destinations) |

## Pousa em

|  | como chega | guia |
|---|---|---|
| **BigQuery** | SDK: `to/bigquery` · Gateway: `bigquery` | [SDK em Go](/pt/docs/sdk/#sources-and-destinations), [Destinos da ingestão](/pt/docs/ingestion-sinks/index.md) |
| **Postgres** | SDK: `to/postgres` · Gateway: `postgres` | [SDK em Go](/pt/docs/sdk/#sources-and-destinations), [Destinos da ingestão](/pt/docs/ingestion-sinks/index.md) |
| **MySQL** | SDK: `to/mysql` · Gateway: `mysql` | [SDK em Go](/pt/docs/sdk/#sources-and-destinations), [Destinos da ingestão](/pt/docs/ingestion-sinks/index.md) |
| **Redshift** | SDK: `to/redshift` · Gateway: `redshift` | [SDK em Go](/pt/docs/sdk/#sources-and-destinations), [Destinos da ingestão](/pt/docs/ingestion-sinks/index.md) |
| **Pub/Sub** | SDK: `to/pubsub` · Gateway: `pubsub` | [SDK em Go](/pt/docs/sdk/#sources-and-destinations), [Destinos da ingestão](/pt/docs/ingestion-sinks/index.md) |

## Arquivos em

|  | como chega | guia |
|---|---|---|
| **S3** | SDK: `store/s3` · Gateway: `files` em `s3://` | [SDK em Go](/pt/docs/sdk/#sources-and-destinations), [Destinos da ingestão](/pt/docs/ingestion-sinks/index.md) |
| **GCS** | SDK: `store/gcs` · Gateway: `files` em `gs://` | [SDK em Go](/pt/docs/sdk/#sources-and-destinations), [Destinos da ingestão](/pt/docs/ingestion-sinks/index.md) |

## Modela em

|  | como chega | guia |
|---|---|---|
| **Postgres** | SQL: `--dialect postgres` (o padrão) | [SQL](/pt/docs/sql/index.md) |
| **BigQuery** | SQL: `--dialect bigquery` | [SQL](/pt/docs/sql/index.md) |

## Roda em

|  | como chega | guia |
|---|---|---|
| **Docker** | uma imagem por peça: `areteacademy/brevis`, `brevis-gateway`, `brevis-sql` | [Instalação](/pt/docs/installation/#docker) |
| **Kubernetes** | um pod por passo | [Kubernetes](/pt/docs/kubernetes/index.md), [Pod por passo](/pt/docs/pod-per-step/index.md) |
| **Helm** | o chart em `deployments/helm/brevis` | [Kubernetes](/pt/docs/kubernetes/#install-it-with-helm) |

## Escrito em

|  | como chega | guia |
|---|---|---|
| **Go** | Core, SDK, Gateway e SQL | [Ecossistema](/pt/docs/ecosystem/index.md) |
| **Python** | `pip install brevis`: contexto entre passos numa etapa Python | [Python](/pt/docs/python/index.md) |
| **SQL** | os modelos do brevis-sql são arquivos `.sql` comuns | [SQL](/pt/docs/sql/index.md) |

## Nuvens

|  | como chega | guia |
|---|---|---|
| **Google Cloud** | BigQuery, Pub/Sub e GCS, acima | — |
| **AWS** | Redshift e S3, acima | — |
