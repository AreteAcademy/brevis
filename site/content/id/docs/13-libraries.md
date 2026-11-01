---
title: Pustaka klien
description: Bagaimana step dalam bahasa apa pun membaca context dan jam run — dan bahasa mana yang sudah punya pustaka.
group: SDK dan pustaka
order: 13
slug: libraries
---

Sebuah step membutuhkan dua hal dari orkestrator: **apa yang dipublikasikan step
sebelumnya** dan **jam mana yang harus dibaca run ini**. Pustaka klien
menyerahkan keduanya, dan tidak lebih.

## Kontrak datang sebelum pustaka

Yang didefinisikan engine adalah **dua variabel environment dan sebuah file**:

| | |
|---|---|
| `BREVIS_INPUT` | `{"<step id>": {…}}` — apa yang dipublikasikan oleh dependensi step ini |
| `BREVIS_OUTPUT` | path untuk menulis **satu** objek JSON |

Ditambah variabel run dan parameter otomatis, semuanya read-only: `BREVIS_RUN_*`
dan `BREVIS_AUTO_*`. Di Kubernetes `BREVIS_OUTPUT` adalah
`/dev/termination-log`, yang sudah dibaca engine untuk mendapatkan exit code.

Jadi **tidak ada panggilan API, token, atau port**. Step dalam bahasa apa pun
dapat ikut serta dengan satu parse JSON dan satu penulisan file:

```bash
window=$(jq -r '.extract.watermark' <<< "$BREVIS_INPUT")
./publish --until "$window"
jq -n --arg n "$(date -Is)" '{published_at: $n}' > "$BREVIS_OUTPUT"
```

Pustaka di sini tidak membuka kemampuan baru — ia menghemat validasi, penanganan
nilai yang absen, dan pesan error yang sebaiknya tidak ditulis ulang.

## Mana yang sudah ada

| bahasa | | |
|---|---|---|
| **Python** | `pip install brevis` | [tersedia](/docs/python/) — `0.2.1` di PyPI |
| **Go** | `brevis/sdk/context` | tersedia, di dalam modul SDK |
| **Node.js** | — | direncanakan |
| **Rust** | — | direncanakan |
| lainnya | variabel di atas | saat ini |

:::note Go tidak berada di `lib/`
Dukungan context Go berada di modul `sdk/` yang sudah dipublikasikan proyek,
sebagai subpaket yang terpangkas menjadi tidak ada bagi siapa pun yang tidak
mengimpornya. `lib/go-context` berarti modul Go kedua untuk paket yang hanya
satu.

Aturannya adalah **satu direktori per bahasa yang belum memiliki artefak
terpublikasi**.
:::

Setiap pustaka adalah artefak terpisah dengan versi dan tag-nya sendiri: perbaikan
di klien Python tidak memaksa rilis engine.

## Apa mereka, dan apa yang bukan

Mereka adalah **klien tipis di atas kontrak**, bukan port dari Go SDK.

Port dari mesin ETL SDK — driver, pagination, checkpoint, ingestion id — tidak
termasuk di sini dan mungkin tidak di mana pun: setiap bahasa yang dituju sudah
punya tool yang lebih baik untuk itu, dan tugas Brevis adalah menjalankannya.
Itulah sebabnya pustaka Python ada: agar tim dapat memakai pandas, Polars, atau
dbt dan tetap mendapatkan satu hal yang menjadi tujuan orkestrator.

## Metrik

Pustaka **memancarkan** metrik, dan engine yang mengeksposnya:

```python
from brevis import metrics

metrics.set("rows_loaded", 48213)      # a gauge: the last value wins
metrics.inc("vendor_rejected_total")   # a counter: it adds up
```

Metrik keluar dari `/metrics` **engine**, pada port scheduler, dengan label
workflow dan step — lihat [Observabilitas](/docs/observability/).

**Ini tidak membuka port, dan tidak mungkin.** Step berjalan di pod-nya sendiri
selama empat puluh detik lalu keluar; port yang dibukanya tidak akan pernah
di-scrape, atau sekali karena kebetulan. Maka barisnya pergi ke stdout — pipe
yang sama yang sudah dibaca engine untuk menggambar fase-fase pipeline — dan
engine mencatatnya. Tidak ada yang dipasang, tidak ada yang dioperasikan, dan
label-labelnya milik engine karena step tidak bisa mengetahui slug workflow-nya
sendiri.

Detail API ada di [Python](/docs/python/#metrics-only-your-pipeline-knows).

## Menulis klien untuk bahasa lain

Jika Anda sampai ke Node.js atau Rust sebelum kami, inilah yang harus dilakukan
klien dengan benar — dan apa yang sudah dipelajari implementasi Python dengan
cara yang sulit:

1. **Key dikualifikasikan oleh step.** `get("bucket")` tanpa step harus ditolak.
   Dua step boleh mempublikasikan `bucket`, dan memilih satu lewat pencarian
   adalah precedence karena kebetulan.
2. **Gabungkan penulisan.** Dua panggilan dengan key berbeda mempertahankan
   keduanya; key yang sama dua kali mempertahankan yang terakhir.
3. **Flush hanya saat proses keluar.** Step yang mati mendadak tidak
   mempublikasikan apa pun — yang benar, karena output step yang crash
   menggambarkan pekerjaan yang tidak selesai.
4. **Tolak di atas 4096 byte.** Platform **memotong** alih-alih menolak, dan
   objek yang terpotong separuh tiba sebagai JSON tidak valid bagi step
   berikutnya.
5. **Bekerja di luar Brevis.** Tanpa `BREVIS_INPUT`, `get()` mengembalikan
   default dan `set()` menerima lalu membuang, tanpa melempar error: skrip yang
   tidak bisa dijalankan secara manual tidak bisa dikembangkan.

## Langkah berikutnya

- [Python](/docs/python/) — API lengkap
- [Konteks antar step](/docs/context/) — konsep dan batas 4096 byte
- [Parameter](/docs/parameters/) — yang dideklarasikan dan yang otomatis
