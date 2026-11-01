---
title: Ökosystem
description: Die vier Bausteine von brevis.sh, was jeder davon tut und worüber die Daten laufen.
group: Erste Schritte
order: 0
slug: ecosystem
---

Der Core plant, reiht ein und überwacht – und berührt die Daten nie. Die Daten berühren das SDK, das abruft, das Gateway, das empfängt, und SQL, das dort modelliert, wo die Daten bereits gelandet sind.

## Die vier Bausteine

| Baustein | Was er tut | Bezug | Anleitung |
|---|---|---|---|
| **Core** | Plant, reiht ein und startet einen Pod pro Step. Berührt die Daten nie. | `areteacademy/brevis` | [Core](/docs/core/) |
| **SDK** | Eine Go-Bibliothek, die innerhalb Ihres Steps extrahiert, formt und lädt. | `go get github.com/AreteAcademy/brevis/sdk` | [Go-SDK](/docs/sdk/) |
| **Gateway** | Nimmt HTTP POST entgegen, antwortet mit 202 und schreibt in Batches. Hat keine eigene Datenbank. | `areteacademy/brevis-gateway` | [Ingestion](/docs/ingestion/) |
| **SQL** | Einfache .sql-Dateien werden in Abhängigkeitsreihenfolge zu Tabellen und Views. | `areteacademy/brevis-sql` | [SQL](/docs/sql/) |

## Control Plane, Data Plane

Die **Control Plane** ist der Core: Er entscheidet, wann jeder Step läuft, hält
die Queue und den Zustand in Postgres und startet einen Pod pro Step. Die Daten
laufen nie über ihn.

Die **Data Plane** sind die übrigen drei Bausteine. Die Daten stammen aus einer
API, einer Datenbank oder aus Dateien und erreichen ihr Ziel auf einem von zwei
Wegen: Das **SDK ruft sie ab**, innerhalb eines Ihrer Steps, oder sie werden per
HTTP **an das Gateway gepusht**, von Webhooks und Anwendungen. Das Ziel ist eine
Tabelle, ein Topic oder Dateien in einem Bucket. Sobald die Daten gelandet sind,
modelliert **SQL** die Tabellen genau dort, in der Datenbank, in der sie
gelandet sind.

### Drumherum

Drei unterstützende Bausteine:

- **[Console](/docs/console/)** – die Weboberfläche über dem Core
- **[agent](/docs/pod-per-step/#where-each-step-runs)** – führt einen Step in einem bereits vorhandenen Cluster aus
- **[`brevis` für Python](/docs/python/)** – Run-Kontext für Python-Steps

## Nur einen Baustein verwenden

Ja. Jeder Baustein hat ein eigenes Modul, eine eigene Version und ein eigenes Image: Das SDK ist eine Go-Bibliothek, die in jedem Programm läuft, das Gateway startet allein mit einer YAML-Datei und ohne Datenbank, und brevis-sql läuft als Container. Der Core orchestriert die anderen, wenn Sie es möchten – er ist für keinen von ihnen Voraussetzung.

## Dieselbe Zeile

Eine Bestellung, die das SDK abruft, und eine Bestellung, die am Gateway eingeht, landen in derselben Tabelle, mit derselben brevis_ingestion_id. Die Wahl zwischen beiden wird zu einer Deployment-Entscheidung, nicht zu einer des Schemas.

Über das SDK, mit `sdk.Landing` (Details unter [Das Landing-Layout](/docs/sdk/#the-landing-layout)):

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

Über das Gateway, ein POST auf `/v1/tables` (Details unter [Eine Route, N Tabellen](/docs/ingestion/#one-route-n-tables)):

```bash
curl -X POST localhost:8080/v1/tables -H 'Authorization: Bearer dev-key' \
    -d '{"table_name":"app_orders","unique_key":"id",
         "data":{"id":"A-1","customer":"acme.example.com","total":150}}'
```

Die Antwort:

```text
{"accepted":1,"rejected":null}  202
```

Was in `app_orders` gelandet ist:

| Spalte | über das SDK | über das Gateway |
|---|---|---|
| `brevis_ingestion_id` | `af5e2a31-fa3c-576c-a0c8-a91517e51149` | `af5e2a31-fa3c-576c-a0c8-a91517e51149` |
| `brevis_record_key` | `A-1` | `A-1` |
| `brevis_operation` | `INSERT` | `INSERT` |
| `brevis_gateway` | `NULL` | `acme_gateway` |
| `data` | `{"id": "A-1", "total": 150, "customer": "acme.example.com"}` | `{"id": "A-1", "total": 150, "customer": "acme.example.com"}` |

Echte Ausgabe: Beide Wege liefen gegen dieselbe lokale Postgres, mit den veröffentlichten Images und dem veröffentlichten SDK. Der einzige Unterschied ist, wer geschrieben hat – brevis_gateway ist in der Zeile des SDK leer.
