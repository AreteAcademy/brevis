---
title: Ecosistema
description: Las cuatro piezas de brevis.sh, qué hace cada una y por dónde pasan los datos.
group: Primeros pasos
order: 0
slug: ecosystem
---

El Core programa, encola y vigila, y nunca toca los datos. Quien los toca es el SDK, que los obtiene; el Gateway, que los recibe; y SQL, que modela lo que ya aterrizó.

## Las cuatro piezas

| pieza | qué hace | cómo obtenerla | guía |
|---|---|---|---|
| **Core** | Programa, encola e inicia un pod por step. Nunca toca los datos. | `areteacademy/brevis` | [Core](/docs/core/) |
| **SDK** | Una biblioteca Go que extrae, da forma y carga, dentro de tu step. | `go get github.com/AreteAcademy/brevis/sdk` | [SDK de Go](/docs/sdk/) |
| **Gateway** | Recibe HTTP POST, responde 202 y escribe en lotes. Sin base de datos propia. | `areteacademy/brevis-gateway` | [Ingesta](/docs/ingestion/) |
| **SQL** | Archivos .sql simples se convierten en tablas y vistas, en orden de dependencia. | `areteacademy/brevis-sql` | [SQL](/docs/sql/) |

## Plano de control, plano de datos

El **plano de control** es el Core: decide cuándo se ejecuta cada step, mantiene la
cola y el estado en Postgres e inicia un pod por step. Los datos nunca
pasan por él.

El **plano de datos** son las otras tres piezas. Los datos vienen de una API, una
base de datos o archivos y llegan a su destino de dos maneras: el **SDK los
obtiene**, dentro de un step propio, o se **envían al Gateway** por
HTTP, desde webhooks y aplicaciones. El destino es una tabla, un topic o archivos en
un bucket. Una vez que los datos aterrizaron, **SQL** modela las tablas allí mismo, en
la base de datos donde aterrizaron.

### Alrededor

Tres piezas de apoyo:

- **[Console](/docs/console/)** — la interfaz web sobre el Core
- **[agent](/docs/pod-per-step/#where-each-step-runs)** — ejecuta un step en un clúster que ya existe
- **[`brevis` para Python](/docs/python/)** — contexto de ejecución para steps en Python

## Usar una sola pieza

Sí se puede. Cada pieza tiene su propio módulo, versión e imagen: el SDK es una biblioteca Go que corre en cualquier programa, el Gateway arranca por sí solo con un archivo YAML y sin base de datos, y brevis-sql se ejecuta como contenedor. El Core orquesta a las demás cuando se quiere; no es requisito de ninguna.

## La misma fila

Un pedido que el SDK obtiene y un pedido que llega al Gateway aterrizan en la misma tabla, con el mismo brevis_ingestion_id. Elegir entre ambos pasa a ser una decisión de despliegue, no de esquema.

Con el SDK, usando `sdk.Landing` (detalles en [El layout de aterrizaje](/docs/sdk/#the-landing-layout)):

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

Con el Gateway, un POST a `/v1/tables` (detalles en [Una ruta, N tablas](/docs/ingestion/#one-route-n-tables)):

```bash
curl -X POST localhost:8080/v1/tables -H 'Authorization: Bearer dev-key' \
    -d '{"table_name":"app_orders","unique_key":"id",
         "data":{"id":"A-1","customer":"acme.example.com","total":150}}'
```

La respuesta:

```text
{"accepted":1,"rejected":null}  202
```

Lo que aterrizó en `app_orders`:

| columna | con el SDK | con el Gateway |
|---|---|---|
| `brevis_ingestion_id` | `af5e2a31-fa3c-576c-a0c8-a91517e51149` | `af5e2a31-fa3c-576c-a0c8-a91517e51149` |
| `brevis_record_key` | `A-1` | `A-1` |
| `brevis_operation` | `INSERT` | `INSERT` |
| `brevis_gateway` | `NULL` | `acme_gateway` |
| `data` | `{"id": "A-1", "total": 150, "customer": "acme.example.com"}` | `{"id": "A-1", "total": 150, "customer": "acme.example.com"}` |

Salida real: ambos caminos corrieron contra el mismo Postgres local, con las imágenes y el SDK publicados. La única diferencia es quién escribió: brevis_gateway está vacío en la fila del SDK.
