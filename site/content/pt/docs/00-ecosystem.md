---
title: Ecossistema
description: As quatro peças do brevis.sh, o que cada uma faz, e por onde o dado passa.
group: Começar
order: 0
slug: ecosystem
---

O Core agenda, enfileira e observa — e nunca toca o dado. Quem toca é o SDK, que busca; o Gateway, que recebe; e o SQL, que modela onde o dado já pousou.

## As quatro peças

| peça | o que faz | como chega | guia |
|---|---|---|---|
| **Core** | Agenda, enfileira e sobe um pod por etapa. Nunca toca o dado. | `areteacademy/brevis` | [Core](/docs/core/) |
| **SDK** | Biblioteca Go que extrai, molda e carrega, dentro da sua etapa. | `go get github.com/AreteAcademy/brevis/sdk` | [SDK em Go](/docs/sdk/) |
| **Gateway** | Recebe HTTP POST, responde 202 e grava em lote. Sem banco próprio. | `areteacademy/brevis-gateway` | [Ingestão](/docs/ingestion/) |
| **SQL** | Arquivos .sql viram tabelas e views, em ordem de dependência. | `areteacademy/brevis-sql` | [SQL](/docs/sql/) |

## Plano de controle, plano de dados

O **plano de controle** é o Core: ele decide quando cada etapa roda, guarda a
fila e o estado no Postgres e sobe um pod por etapa. O dado nunca passa por
ele.

O **plano de dados** são as outras três peças. O dado vem de uma API, de um
banco ou de arquivos e chega ao destino por um de dois caminhos: o **SDK
busca**, dentro de uma etapa sua, ou o dado **chega ao Gateway** por HTTP,
vindo de webhooks e apps. O destino é uma tabela, um tópico ou arquivos num
bucket. Depois que o dado pousou, o **SQL** modela as tabelas ali mesmo, no
banco onde ele pousou.

### Ao redor

Três peças de apoio:

- **[Console](/docs/console/)** — a interface web do Core
- **[agent](/docs/pod-per-step/#onde-cada-passo-roda)** — roda a etapa num cluster que já é seu
- **[`brevis` para Python](/docs/python/)** — contexto de execução para etapas em Python

## Usar só uma peça

Pode. Cada peça tem módulo, versão e imagem próprios: o SDK é uma biblioteca Go que roda em qualquer programa, o Gateway sobe sozinho com um YAML e sem banco, e o brevis-sql roda como um container. O Core orquestra as outras quando você quer — não é pré-requisito de nenhuma.

## A mesma linha

Um pedido que o SDK busca e um pedido que chega ao Gateway pousam na mesma tabela, com o mesmo brevis_ingestion_id. Escolher entre os dois vira decisão de deploy, não de schema.

Pelo SDK, com `sdk.Landing` (detalhes em [O layout de aterrissagem](/docs/sdk/#o-layout-de-aterrissagem)):

```go
sdk.Execute(ctx, &sdk.Pipeline{
	Source:    sdk.Source{From: from.Files{Path: "order.json"}},
	Transform: []sdk.Transformer{sdk.Landing("app_orders", sdk.LandingKey("id"))},
	Target: sdk.Target{
		To:     postgres.Table{DSN: dsn, Name: "app_orders"},
		Schema: sdk.LandingSchema(sdk.LandingOptions{}),
	},
}, nil)
```

Pelo Gateway, um POST em `/v1/tables` (detalhes em [Uma rota, N tabelas](/docs/ingestion/#uma-rota-n-tabelas)):

```bash
curl -X POST localhost:8080/v1/tables -H 'Authorization: Bearer dev-key' \
    -d '{"table_name":"app_orders","unique_key":"id",
         "data":{"id":"A-1","customer":"acme.example.com","total":150}}'
```

A resposta:

```text
{"accepted":1,"rejected":null}  202
```

O que ficou em `app_orders`:

| coluna | pelo SDK | pelo Gateway |
|---|---|---|
| `brevis_ingestion_id` | `af5e2a31-fa3c-576c-a0c8-a91517e51149` | `af5e2a31-fa3c-576c-a0c8-a91517e51149` |
| `brevis_record_key` | `A-1` | `A-1` |
| `brevis_operation` | `INSERT` | `INSERT` |
| `brevis_gateway` | `NULL` | `acme_gateway` |
| `data` | `{"id": "A-1", "total": 150, "customer": "acme.example.com"}` | `{"id": "A-1", "total": 150, "customer": "acme.example.com"}` |

Saída real: os dois caminhos rodaram contra o mesmo Postgres local, com as imagens e o SDK publicados. A única diferença é quem escreveu — brevis_gateway fica vazio na linha do SDK.
