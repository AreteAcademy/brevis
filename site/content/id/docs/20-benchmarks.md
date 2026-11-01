---
title: Benchmark
description: Bagaimana kami mengukur gateway, apa kata angka-angkanya, dan tiga saksi yang harus sepakat.
group: Ingestion
order: 20
slug: benchmarks
---

Satu command, dan ia mengukur seluruh gateway:

```bash
BREVIS_BENCH_IMAGE=areteacademy/brevis-gateway:0.11.0 ./bench/run.sh
```

Ia menjalankan Postgres, memulai gateway, membebaninya dengan [k6], **menunggu
buffer terkuras**, men-scrape `/metrics` milik gateway sendiri, dan menulis
`bench/results/REPORT.md`.

## Tiga saksi, dan yang ketiga adalah yang terpenting

k6 tahu apa yang dilihat produser. Gateway tahu apa yang dilakukannya. Uji beban
yang hanya membawa yang pertama tidak dapat membedakan gateway yang cepat dari
yang menjawab `202` dan membuang apa yang diterimanya.

Maka laporan menaruh keduanya berdampingan — dan menghitung baris di Postgres:

| saksi | event |
|---|---:|
| k6 diberi tahu `accepted` | 5.431.000 |
| gateway menghitung diterima | 5.431.000 |
| baris di Postgres | 5.431.000 |
| record di dead letter | 0 |

Dua yang pertama sama-sama aritmetika gateway sendiri — satu dari respons yang
ditulisnya, satu dari counter yang ditambahnya — dan bug yang menghilangkan event
bisa menjaga keduanya konsisten **satu sama lain**. Database adalah satu-satunya
yang tidak mempercayai kata-kata kami.

**Run ketika ketiganya berbeda dianggap gagal**, sebagus apa pun persentilnya.

## Run di atas

Run 2026-09-26 di `bench/results/REPORT.md`:
`areteacademy/brevis-gateway:0.11.0`, Darwin arm64, 11 CPU, Postgres 17 di dalam
container pada kernel yang sama, `auto_table[columns]` dengan `write: append`:

| | |
|---|---:|
| event diterima | **5.431.000** (135.768/d) |
| body terkirim | 1.156,7 MiB dalam 40d |
| `503` (backpressure) | 0 |
| p50 / p95 / p99 | 5,9 ms / 16,0 ms / 22,7 ms |
| batch terkirim | 1.087, tidak ada yang dikuburkan |

## Threshold adalah intinya

k6 keluar dengan kode bukan nol ketika salah satunya gagal, sehingga ini adalah
**gerbang** dan bukan grafik:

| threshold | alasan |
|---|---|
| `p95 < 500ms` | jalur accept harus tetap terlepas dari latensi sink. Request yang menunggu `COPY` adalah cacat yang ditentang oleh pipe asinkron. |
| `http_req_failed < 1%` | `503` tidak masalah; timeout atau 5xx tidak. |
| `events accepted > 1000` | rangkaian threshold lolos terhadap gateway yang tidak menjawab apa-apa. |

Sengaja dibuat longgar: ini berjalan di mesin apa pun yang diberikan CI. Mereka
menangkap **satu orde besaran**, yang seperti itulah regresi tampak, bukan satu
persen.

**`503` bukan kegagalan.** Itu adalah gateway menolak event yang tidak punya
tempat untuk ditaruh, dan aman di-retry — `ingestion_id` adalah fungsi beku dari
event, sehingga body yang sama yang dikirim lagi adalah record yang sama.
Menghitungnya sebagai error akan membuat backpressure tampak seperti bug.

## Yang tidak diukur

- **Bukan BigQuery.** Tujuannya adalah Postgres di dalam container. Kuota job
  load warehouse, latensi commit, dan konkurensi DDL tidak ada dalam angka ini
  dan tidak mungkin ada.
- **Bukan durabilitas.** `durability: memory` adalah satu-satunya tier, sehingga
  `202` berarti *diterima ke RAM*. Ini mengukur seberapa cepat itu terjadi,
  tidak pernah apa yang bertahan dari `SIGKILL`.
- **Bukan hardware Anda.** Bacalah sebagai bentuk dan rasio.

## Cloud lokal, menurut profil

```bash
docker compose -f docker-compose.drivers.yml --profile gcp up -d
docker compose -f docker-compose.drivers.yml --profile all up -d
```

Satu file dengan profil, bukan satu per cloud: dua file adalah dua nama proyek,
dan `postgres` di keduanya pernah menggantikan database pengembangan engine.

### floci mencakup lebih sedikit BigQuery daripada kelihatannya

Diukur, bukan diasumsikan:

| | |
|---|---|
| membuat dataset dan tabel | ✅ |
| `timePartitioning` dan `clustering` dipertahankan | ✅ — bentuk bug `0.9.1` |
| `PATCH` sebuah kolom | ✅ — evolusi milik `auto_table` |
| `insertAll` | ✅ |
| query | ✅, hanya dengan `/var/run/docker.sock` di-mount |
| **job load** | ❌ `Only QUERY jobs are supported by the floci BigQuery emulator` |

SDK menulis ke BigQuery **hanya** lewat job load. Jadi floci menguji setiap baris
`auto_table` yang mengubah payload menjadi DDL — dan **tidak satu pun penulisan
Brevis**. Itu tetap separuh yang hari ini tidak punya test lokal, dan layak
dimiliki. Tetapi suite hijau terhadap floci tidak berarti BigQuery berfungsi.

[k6]: https://k6.io
