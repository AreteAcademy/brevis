---
title: Instalasi
description: Cara mendapatkan binary, apa yang dibutuhkannya, dan cara memastikan instalasi sudah benar.
group: Memulai
order: 2
slug: installation
---

brevis.sh dikirim sebagai **satu binary** dan sebagai **image Docker**. Tidak ada
runtime yang perlu dipasang bersamanya: migrasi, aset antarmuka, dan font semuanya
berada di dalam binary.

## Persyaratan

| | |
|---|---|
| **PostgreSQL** | wajib untuk `serve`, `scheduler`, `migrate`, `publish`, dan `backfill` |
| **Go 1.25+** | hanya jika Anda build dari source |
| **Kubernetes** | opsional — tanpa klaster, step berjalan sebagai proses lokal |

`brevis run`, `validate`, `marca`, `hash`, dan `version` **tidak** membutuhkan database.

## Build dari source

Ini adalah jalur yang menghasilkan binary dengan versi dan commit yang tertanam:

```bash
git clone https://github.com/AreteAcademy/brevis.git
cd brevis
make build
./bin/brevis version
```

```
brevis 0.11.2
  commit  bb832ff
  build   2026-09-05T12:00:00Z
  go      go1.25.7 darwin/arm64
```

:::warning Engine belum punya rilis bertag
Setiap tag di repositori milik SDK (`sdk/v*`); modul root tidak punya satu pun.
`go install github.com/AreteAcademy/brevis/cmd/brevis@latest` akan menyelesaikan
ke commit terbaru di branch dan menghasilkan binary yang mengidentifikasi dirinya
sebagai `brevis dev`, tanpa commit dan tanpa tanggal — `-ldflags` hanya masuk
lewat `make build` dan `docker build`. Untuk versi yang dapat dilacak, gunakan
image atau `make build`.
:::

## Docker

Dua image dari binary yang sama, karena kedua perannya punya kebutuhan yang
berlawanan:

| tag | base | alasan |
|---|---|---|
| `:0.11.2` | distroless | API hanya melayani HTTP dan tidak mengeksekusi apa pun — tanpa shell, permukaan minimal |
| `:0.11.2-worker` | alpine + tini | step `run:` membutuhkan shell |

```bash
docker run --rm areteacademy/brevis:latest version
```

Kedua image berada di [hub.docker.com/r/areteacademy/brevis](https://hub.docker.com/r/areteacademy/brevis),
untuk `amd64` dan `arm64`.

## Lingkungan lokal lengkap

Repositori menyertakan `docker-compose.yml` yang menjalankan Postgres, API, dan
scheduler:

```bash
make up      # Postgres + API + scheduler
make smoke   # checks /health and /ready
make logs
make down
```

Antarmuka ada di `http://localhost:8080`.

## Konfigurasi minimum

Hanya satu variabel yang wajib:

```bash
export BREVIS_DATABASE_URL='postgres://brevis:brevis@localhost:5432/brevis?sslmode=disable'
```

Tanpanya, subcommand yang memakai database gagal saat boot — dengan sengaja.
Proses yang naik tanpa kredensial baru menemukan masalahnya pada request
pertama, dan saat itu readiness sudah berbohong kepada orkestrator.

```
$ brevis migrate status
erro: BREVIS_DATABASE_URL e obrigatoria
```

Daftar lengkapnya ada di [Konfigurasi](/docs/configuration/).

## Menerapkan skema

Migrasi tertanam di dalam binary dan diterapkan oleh subcommand tersendiri —
**`serve` tidak pernah mengubah skema**:

```bash
brevis migrate up
brevis migrate status
```

## Memastikan

```bash
brevis version
brevis validate examples/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    daily-report                 chain  3 steps, 2 dependencias  cron 0 2 * * *
  ok    hello                        dag  4 steps, 4 dependencias  (manual)
```

Jika keduanya menjawab, instalasi sudah benar. Lanjutkan ke
[Quickstart](/docs/quickstart/).
