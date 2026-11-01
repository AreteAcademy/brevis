---
title: Observabilitas
description: Metrik Prometheus dari kedua proses, apa yang dipantau, dan apa yang sengaja tidak ada.
group: Operasi
order: 16
slug: observability
---

Brevis mengekspos metrik Prometheus pada port tersendiri, dari **kedua** proses.

```
GET http://<pod>:9090/metrics
```

Port berasal dari `BREVIS_METRICS_ADDR`. Ia terpisah dari port HTTP dengan
sengaja: metrik tidak melewati autentikasi antarmuka, dan melayani keduanya dari
satu tempat akan memaksa memilih antara scrape yang terautentikasi dan dashboard
yang terbuka.

## Dua proses, dua endpoint

Inilah bagian yang paling sering salah dalam banyak setup, dengan hanya
men-scrape API.

| | melayani | melaporkan |
|---|---|---|
| `brevis serve` | UI dan API HTTP | kedalaman antrean |
| `brevis scheduler` | tidak ada lewat HTTP | kedalaman antrean, latensi klaim, slot, orphan, run, step |

Scheduler-lah yang mengeksekusi, sehingga hampir semuanya berasal darinya.
Dashboard yang dibangun hanya dari API menampilkan antrean yang bertambah dan
**tidak ada** tentang apa yang menguraskannya.

## Metrik yang dipublikasikan sebuah step

Di luar apa yang diukur engine, step dapat mempublikasikan metriknya sendiri —
berapa baris yang mendarat, berapa yang ditolak vendor:

```python
from brevis import metrics
metrics.set("rows_loaded", 48213)
```

Metrik itu muncul di `/metrics` scheduler yang **sama**, dengan prefiks dan label
dari engine:

```
brevis_step_rows_loaded{workflow="daily_sales",step="load"} 48213
```

Step tidak membuka port: ia menulis satu baris ke stdout dan engine mencatatnya —
pod yang hidup empat puluh detik tidak dapat di-scrape. Lihat [Python](/docs/python/).

## Apa yang dipantau

| sinyal | alasan |
|---|---|
| kedalaman antrean naik tanpa turun | scheduler mati, atau `--concurrency` terlalu rendah untuk bebannya |
| latensi klaim bertambah | perebutan di database, atau antrean terlalu besar untuk interval |
| orphan > 0 | run yang diklaim proses yang mati sebelum selesai |
| slot tidak termaterialisasi | loop scheduler berhenti, dan jadwal ikut berhenti |

Dua yang pertama saja tidak membedakan "beban tinggi" dari "proses mati" — itulah
sebabnya slot penting. Antrean naik **dengan** slot yang dibuat adalah beban;
antrean naik **tanpa** slot adalah loop yang berhenti.

## Kubernetes

```yaml
env:
  - name: BREVIS_METRICS_ADDR
    value: ":9090"
ports:
  - name: metrics
    containerPort: 9090
```

Dengan Prometheus Operator, satu `PodMonitor` per peran — dan memang harus satu
per peran, karena kedua Deployment membawa label yang berbeda dan melaporkan set
yang berbeda.

## Apa yang sengaja tidak ada

**Trace.** Belum ada OpenTelemetry. Step berjalan sebagai pod-nya sendiri, dan
yang penting tentangnya — mulai, selesai, percobaan, exit code, log — sudah ada di
database dan di layar. Trace yang berguna adalah yang ada di dalam step, dan itu
milik kode step, bukan milik orkestrator.

Itu berlaku selama step adalah unitnya. Pada hari step memanggil layanan lain dan
pertanyaannya menjadi "ke mana perginya 40 detik itu", trace mulai sepadan dengan
biayanya.

## Langkah berikutnya

- [Kubernetes](/docs/kubernetes/) — men-deploy kedua proses
- [Konfigurasi](/docs/configuration/) — `BREVIS_METRICS_ADDR` dan lainnya
