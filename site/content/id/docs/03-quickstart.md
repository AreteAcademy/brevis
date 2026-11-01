---
title: Quickstart
description: Dari nol hingga DAG hidup di antarmuka, dalam empat langkah.
group: Memulai
order: 3
slug: quickstart
---

Panduan ini menjalankan brevis.sh, mempublikasikan sebuah workflow, dan
menjalankannya. Di akhir, Anda melihat graf dengan state langsung dari setiap
step.

Prasyarat: PostgreSQL yang dapat dijangkau dan variabel `BREVIS_DATABASE_URL`.

## 1. Terapkan skema

```bash
export BREVIS_DATABASE_URL='postgres://brevis:brevis@localhost:5432/brevis?sslmode=disable'
brevis migrate up
```

## 2. Tulis sebuah workflow

Buat `hello.yaml`. Contoh ini hanya memakai shell, sehingga berjalan di mana saja:

```yaml
name: hello
type: dag
tags: [example]

steps:
  - id: prepare
    run: sh -c 'echo preparing; sleep 1'

  # The next two depend on the same step, so they run IN PARALLEL.
  - id: extract
    run: sh -c 'sleep 2; echo 42 extracted'
    depends_on: [prepare]

  - id: validate
    run: sh -c 'sleep 1; echo validated'
    depends_on: [prepare]

  - id: publish
    run: echo published
    depends_on: [extract, validate]
```

Periksa sebelum mempublikasikan — ini tidak menyentuh database:

```bash
brevis validate hello.yaml
```

```text
  ok    hello                        dag  4 steps, 4 dependencies  (manual)
```

## 3. Jalankan sekarang, tanpa antrean

Jalur terpendek untuk melihat graf bekerja:

```bash
brevis run hello.yaml
```

```text
workflow hello (dag, 4 steps) in .
  ▶ prepare
    prepare | preparing
  ✓ prepare
  ▶ extract
  ▶ validate
    validate | validated
  ✓ validate
    extract | 42 extracted
  ✓ extract
  ▶ publish
    publish | published
  ✓ publish

workflow hello finished
```

`extract` dan `validate` mulai bersamaan karena keduanya bergantung pada step
yang sama. Runner menelusuri graf **per level**: semua yang berada dalam satu
level berjalan paralel, dan level berikutnya baru mulai ketika level
sebelumnya selesai seluruhnya.

:::note `run` bersifat lokal secara alami
`brevis run` hanya beroperasi dengan `BREVIS_ENV=local` (kosong dihitung lokal) dan
mengeksekusi step sebagai proses di instance itu sendiri — bahkan yang
mendeklarasikan `image:`. Pod dibuat oleh `scheduler`.
:::

## 4. Publikasikan dan operasikan

Agar workflow ada di database, tampil di antarmuka, dan mengikuti jadwalnya:

```bash
brevis publish hello.yaml
```

```text
  published  hello                      (manual)
```

Jalankan antarmuka dan scheduler di dua terminal:

```bash
brevis serve
```

```bash
brevis scheduler --interval 5s --concurrency 4
```

Buka `http://localhost:8080`, cari `hello` di daftar workflow, lalu klik ▶. Graf
menampilkan setiap step berganti state secara langsung.

## Apa yang terjadi

| | |
|---|---|
| `migrate up` | membuat tabel |
| `validate` | memeriksa YAML tanpa menyentuh database |
| `run` | dieksekusi di instance itu sendiri, tanpa antrean |
| `publish` | menyimpan workflow dan jadwalnya |
| `serve` | menjalankan API dan antarmuka |
| `scheduler` | mematerialisasi jadwal dan menguras antrean |

## Langkah berikutnya

- [Workflows](/docs/workflows/) — format YAML lengkap
- [Parameter](/docs/parameters/) — apa yang berubah di antara dua run
- [Pod per step](/docs/pod-per-step/) — bagaimana ini menjadi Kubernetes
