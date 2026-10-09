---
title: Ecosystem
description: The four brevis.sh pieces, what each one does, and where the data goes through.
group: Getting started
order: 0
slug: ecosystem
---

The Core schedules, queues and watches — and never touches the data. What touches it is the SDK, which fetches; the Gateway, which receives; and SQL, which models where the data has already landed.

## The four pieces

| piece | what it does | how you get it | guide |
|---|---|---|---|
| **Core** | Schedules, queues and starts one pod per step. Never touches the data. | `areteacademy/brevis` | [Core](/docs/core/) |
| **SDK** | A Go library that extracts, shapes and loads, inside your step. | `go get github.com/AreteAcademy/brevis/sdk` | [Go SDK](/docs/sdk/) |
| **Gateway** | Takes HTTP POST, answers 202 and writes in batches. No database of its own. | `areteacademy/brevis-gateway` | [Ingestion](/docs/ingestion/) |
| **SQL** | Plain .sql files become tables and views, in dependency order. | `areteacademy/brevis-sql` | [SQL-hub.md](https://github.com/AreteAcademy/brevis/blob/master/docs/SQL-hub.md) |

## Control plane, data plane

The **control plane** is the Core: it decides when each step runs, keeps the
queue and the state in Postgres and starts one pod per step. The data never
goes through it.

The **data plane** is the other three pieces. Data comes from an API, a
database or files and reaches its destination one of two ways: the **SDK
fetches** it, inside a step of yours, or it is **pushed to the Gateway** over
HTTP, from webhooks and apps. The destination is a table, a topic or files in
a bucket. Once the data has landed, **SQL** models the tables right there, in
the database it landed in.

### Around it

Three supporting pieces:

- **Console** — the web interface over the Core
- **[agent](/docs/pod-per-step/#where-each-step-runs)** — runs a step in a cluster you already have
- **[`brevis` for Python](/docs/python/)** — run context for Python steps

## Using only one piece

Yes. Each piece has its own module, version and image: the SDK is a Go library that runs in any program, the Gateway starts on its own with a YAML file and no database, and brevis-sql runs as a container. The Core orchestrates the others when you want it to — it is a prerequisite for none of them.

## The same row

An order the SDK fetches and an order that arrives at the Gateway land in the same table, with the same brevis_ingestion_id. Choosing between the two becomes a deployment decision, not a schema one.

Through the SDK, with `sdk.Landing` (details in [The landing layout](/docs/sdk/#the-landing-layout)):

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

Through the Gateway, a POST to `/v1/tables` (details in [One route, N tables](/docs/ingestion/#one-route-n-tables)):

```bash
curl -X POST localhost:8080/v1/tables -H 'Authorization: Bearer dev-key' \
    -d '{"table_name":"app_orders","unique_key":"id",
         "data":{"id":"A-1","customer":"acme.example.com","total":150}}'
```

The answer:

```text
{"accepted":1,"rejected":null}  202
```

What landed in `app_orders`:

| column | through the SDK | through the Gateway |
|---|---|---|
| `brevis_ingestion_id` | `af5e2a31-fa3c-576c-a0c8-a91517e51149` | `af5e2a31-fa3c-576c-a0c8-a91517e51149` |
| `brevis_record_key` | `A-1` | `A-1` |
| `brevis_operation` | `INSERT` | `INSERT` |
| `brevis_gateway` | `NULL` | `acme_gateway` |
| `data` | `{"id": "A-1", "total": 150, "customer": "acme.example.com"}` | `{"id": "A-1", "total": 150, "customer": "acme.example.com"}` |

Real output: both paths ran against the same local Postgres, with the published images and SDK. The only difference is who wrote it — brevis_gateway is empty on the SDK’s row.
