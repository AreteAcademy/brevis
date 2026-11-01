---
title: Runtime step
description: Apa yang dideklarasikan sebuah step tentang bahasa dan tool yang dijalankannya — dan mengapa Anda jarang perlu melakukannya.
group: Referensi
order: 11
slug: runtime
---

Graf menggambar chip pada setiap step yang menyebutkan bahasanya dan tool yang
dijalankannya:

```
┌──────────────────────────────────────┐
│ ● transform            SDK v0.57.0   │
│   dbt build --select gold            │
│   ⬤ Python  ▢ dbt                    │
│   2m14s · attempt 1                  │
└──────────────────────────────────────┘
```

**Sebagian besar waktu Anda tidak menulis apa-apa.** Engine membaca `run:` dan
`image:` lalu menyimpulkannya.

## Kapan mendeklarasikannya

Dua field, keduanya opsional, keduanya per step:

```yaml
steps:
  - id: fetch
    run: /opt/brevis/bin/fetch-weather
    runtime: go            # a compiled binary; the path says nothing

  - id: transform
    run: dbt build --select gold+
    tools: [dbt]           # what it drives, not what it is
```

| field | |
|---|---|
| `runtime` | bahasa step: `go`, `python`, `node`, `shell`… |
| `tools` | tool yang dijalankan command: `dbt`, `dlt`, `spark`… |

Aturan praktisnya: **deklarasikan ketika inferensi salah.** `run:` yang menunjuk
ke binary terkompilasi tidak punya cara untuk menyatakan bahwa ia Go; wrapper yang
memanggil `dbt` secara internal tidak punya cara untuk menyatakan bahwa ia
menjalankan dbt.

## Mengapa kosakata tertutup

Id di luar kosakata **ditolak saat publish**, dengan menyebutkan apa yang valid.
Alternatifnya adalah chip yang tampil kosong di layar tiga hari kemudian, tanpa
apa pun untuk melacak penyebabnya.

```
workflow "daily": step "transform": `runtime: pyton` is not valid
  (valid: go, python, node, shell, …)
```

## Untuk apa chip itu

Bukan dekorasi. Ketika run gagal pada pukul 3 pagi, pertanyaan pertama adalah
"apa yang dieksekusi step ini?", dan jawabannya biasanya ada di repositori lain.
Chip menaruhnya di graf itu sendiri — berdampingan dengan versi SDK yang dipakai
step, yang membedakan "rusak hari ini" dari "rusak sejak kenaikan versi hari
Selasa".

## Langkah berikutnya

- [Workflows](/docs/workflows/) — setiap field step
- [Pod per step](/docs/pod-per-step/) — di mana step benar-benar berjalan
- [Observabilitas](/docs/observability/) — metrik yang menyertainya
