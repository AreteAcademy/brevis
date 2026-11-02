---
title: Ekosistem
description: Empat bagian brevis.sh, fungsi masing-masing, dan jalur yang dilewati data.
group: Memulai
order: 0
slug: ecosystem
---

Core menjadwalkan, mengantrekan, dan memantau — dan tidak pernah menyentuh data. Yang menyentuh data adalah SDK, yang mengambilnya; Gateway, yang menerimanya; dan SQL, yang memodelkan data yang sudah mendarat.

## Empat bagian

| bagian | fungsi | cara mendapatkannya | panduan |
|---|---|---|---|
| **Core** | Menjadwalkan, mengantrekan, dan memulai satu pod per step. Tidak pernah menyentuh data. | `brevissh/brevis` | [Core](/docs/core/) |
| **SDK** | Pustaka Go yang mengekstrak, membentuk, dan memuat data di dalam step Anda. | `go get github.com/AreteAcademy/brevis/sdk` | [Go SDK](/docs/sdk/) |
| **Gateway** | Menerima HTTP POST, menjawab 202, dan menulis secara batch. Tanpa database sendiri. | `brevissh/brevis-gateway` | [Ingestion](/docs/ingestion/) |
| **SQL** | File .sql biasa menjadi tabel dan view, berurutan sesuai dependensi. | `brevissh/brevis-sql` | [SQL](/docs/sql/) |

## Control plane, data plane

**Control plane** adalah Core: ia menentukan kapan setiap step berjalan, menjaga
antrean dan state di Postgres, dan memulai satu pod per step. Data tidak pernah
melewatinya.

**Data plane** adalah tiga bagian lainnya. Data berasal dari API, database, atau
file dan sampai ke tujuannya lewat salah satu dari dua jalur: **SDK
mengambilnya**, di dalam step Anda, atau data **didorong ke Gateway** lewat
HTTP, dari webhook dan aplikasi. Tujuannya adalah tabel, topik, atau file di
bucket. Setelah data mendarat, **SQL** memodelkan tabel-tabel itu di tempat,
di database tempat data tersebut mendarat.

### Di sekitarnya

Tiga bagian pendukung:

- **[Console](/docs/console/)** — antarmuka web di atas Core
- **[agent](/docs/pod-per-step/#where-each-step-runs)** — menjalankan step di klaster yang sudah Anda miliki
- **[`brevis` untuk Python](/docs/python/)** — konteks run untuk step Python

## Memakai satu bagian saja

Bisa. Setiap bagian punya modul, versi, dan image sendiri: SDK adalah pustaka Go yang berjalan di program mana pun, Gateway dapat dijalankan sendiri dengan file YAML tanpa database, dan brevis-sql berjalan sebagai container. Core mengorkestrasi yang lain bila Anda menghendakinya — ia bukan prasyarat bagi satu pun dari mereka.

## Baris yang sama

Sebuah order yang diambil SDK dan sebuah order yang tiba di Gateway mendarat di tabel yang sama, dengan brevis_ingestion_id yang sama. Memilih di antara keduanya menjadi keputusan deployment, bukan keputusan skema.

Lewat SDK, dengan `sdk.Landing` (detail di [Layout landing](/docs/sdk/#the-landing-layout)):

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

Lewat Gateway, sebuah POST ke `/v1/tables` (detail di [Satu rute, N tabel](/docs/ingestion/#one-route-n-tables)):

```bash
curl -X POST localhost:8080/v1/tables -H 'Authorization: Bearer dev-key' \
    -d '{"table_name":"app_orders","unique_key":"id",
         "data":{"id":"A-1","customer":"acme.example.com","total":150}}'
```

Jawabannya:

```text
{"accepted":1,"rejected":null}  202
```

Yang mendarat di `app_orders`:

| kolom | lewat SDK | lewat Gateway |
|---|---|---|
| `brevis_ingestion_id` | `af5e2a31-fa3c-576c-a0c8-a91517e51149` | `af5e2a31-fa3c-576c-a0c8-a91517e51149` |
| `brevis_record_key` | `A-1` | `A-1` |
| `brevis_operation` | `INSERT` | `INSERT` |
| `brevis_gateway` | `NULL` | `acme_gateway` |
| `data` | `{"id": "A-1", "total": 150, "customer": "acme.example.com"}` | `{"id": "A-1", "total": 150, "customer": "acme.example.com"}` |

Output nyata: kedua jalur dijalankan pada Postgres lokal yang sama, dengan image dan SDK yang dipublikasikan. Satu-satunya perbedaan adalah siapa yang menulisnya — brevis_gateway kosong pada baris dari SDK.
