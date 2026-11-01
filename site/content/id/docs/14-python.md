---
title: Python
description: pip install brevis — context antar step, jam run, dan metrik milik pipeline Anda sendiri, tanpa dependensi sama sekali.
group: SDK dan pustaka
order: 14
slug: python
---

```bash
pip install brevis
```

```python
from brevis import context, metrics, run

bucket = context.get("extract.bucket")
since, until = run.window()          # the window this run covers

context.set(rows=48213, watermark="2026-09-07T03:00:00Z")
metrics.set("rows_loaded", 48213)    # reaches the engine's /metrics
```

**Tanpa dependensi, selamanya.** Ia membaca satu variabel environment, menulis
satu file, dan mencetak satu baris ke stdout. Membutuhkan Python 3.9+.

Ia bukan port dari Go SDK: tanpa driver, tanpa pagination, tanpa ingestion id. Ia
ada agar tim dapat memakai pandas, Polars, atau dbt dan tetap mendapatkan apa yang
menjadi tujuan orkestrator.

## Membaca context

Key selalu dikualifikasikan oleh step yang mempublikasikannya:

```python
context.get("extract.bucket")            # the value
context.get("extract.nope", default=0)   # absent is not an error
context.of("extract")                    # everything that step published
context.published()                      # what THIS step has published so far
```

`context.get("bucket")` tanpa step **ditolak**, dengan menyebut step yang
mempublikasikannya. Step saling terisolasi — `extract` dan `transform` dapat
sama-sama mempublikasikan `bucket` tanpa salah satunya hilang — dan key polos
membuang itu begitu dua step melakukannya. Mencari lalu memilih satu adalah
precedence karena kebetulan: berfungsi sampai suatu saat diam-diam tidak.

Membaca step yang tidak menjadi dependensi step ini adalah error, dan pesannya
menunjuk ke perbaikannya.

## Menulis

```python
context.set(name="Daniel")
context.set(label="Name")
# both survive: {"name": "Daniel", "label": "Name"}
```

Panggilan **digabungkan**. Key yang sama dua kali mempertahankan tulisan
terakhir; key yang berbeda membiarkan yang pertama. **Tidak ada argumen step** —
satu-satunya namespace yang dapat ditulis sebuah proses adalah miliknya sendiri,
yang membuat isolasi bersifat struktural dan bukan sekadar aturan.

Tidak ada yang sampai ke disk hingga proses keluar, sehingga step yang mati
mendadak tidak mempublikasikan apa pun. Itu benar: output step yang crash
menggambarkan pekerjaan yang tidak selesai.

## Jam yang harus dibaca run ini

Setiap run juga membawa **parameter otomatis**: nilai yang dihitung sendiri oleh
engine. Tidak ada yang mendeklarasikannya, setiap run memilikinya, dan
mereka menjawab pertanyaan yang kalau tidak akan dijawab pipeline dengan
`datetime.now()`.

```python
from datetime import timedelta
from brevis import run

since, until = run.window() or (run.now() - timedelta(days=1), run.now())
df = fetch(since, until)
df.to_parquet(f"/data/{run.auto().date}.parquet")
```

:::warning Mengapa bukan `datetime.now()`
`run.now()` adalah **slot** pada run terjadwal. Fetcher yang membaca
`datetime.now()` dan mengurangi dua jam benar pada run yang mulai tepat waktu dan
salah empat puluh menit pada run yang ditunda antrean — dan empat puluh menit itu
milik **tidak ada run sama sekali**, karena slot berikutnya juga membaca
`now()`-nya sendiri. Tidak ada yang gagal, dan celahnya ditemukan berminggu-minggu
kemudian.
:::

| | |
|---|---|
| `run.now()` | jamnya: slot, atau kapan run dimulai |
| `run.window()` | `(start, end)`, end tidak termasuk — atau `None` |
| `run.auto()` | parameter otomatis, di bawah |
| `run.context()` | id, percobaan, pemicu, params, dan auto |
| `run.params()` · `run.param(name)` | parameter yang dideklarasikan di workflow |
| `run.map_index()` · `run.map_value()` | instance, di bawah `for_each:` |

`window()` berasal dari **cron**, bukan dari riwayat: backfill slot bulan Maret
menghasilkan jendela yang dimiliki Maret. Meminta `[start, end)` tidak pernah
tumpang tindih dan tidak pernah berlubang, seberapa pun terlambatnya run dan
seberapa sering ia di-retry.

Ia mengembalikan `None` alih-alih sepasang waktu nol ketika workflow tidak punya
jadwal — query dari epoch memilih segalanya, dan kegagalan itu tidak boleh
diam-diam. `or` di atas adalah seluruh fallback-nya.

### Parameter otomatis

| field | |
|---|---|
| `date` | `"2026-09-08"` — partisi, folder, `WHERE` |
| `adjusted_at` | jam yang dibaca sebagai pengganti `now()` |
| `scheduled_at` | slot yang diminta cron; `None` pada run manual |
| `started_at` | kapan **percobaan ini** dimulai; retry menggesernya |
| `delay_seconds` | seberapa terlambat percobaan ini; tidak pernah negatif |
| `interval_start` · `interval_end` | jendela, end tidak termasuk |
| `previous_error` | run sebelum ini tidak berhasil |
| `previous_success_at` | slot dari run terakhir yang **berhasil** — yang membuat `previous_error` dapat ditindaklanjuti |

Jalankan skrip secara manual dan `run.now()` **adalah** jam dinding serta
`window()` adalah `None`, sehingga tidak ada cabang yang perlu ditulis untuk
pengembangan lokal.

## Satu instance dari step yang di-map

Di bawah [`for_each:`](/docs/workflows/), engine menjalankan satu proses per
elemen dan memberi tahu masing-masing yang mana yang didapatnya:

```python
partition = run.map_value()
if partition is None:
    raise SystemExit("this step is meant to run under `for_each:`")

load(f"/data/{partition}.csv")
```

String JSON tiba **tanpa tanda kutip** — `for_each` atas `["2026-01"]` memberikan
`2026-01` ke step, bukan `"2026-01"`. Selain itu — objek, angka, list — tiba
sebagai JSON-nya, sehingga `json.loads(run.map_value())`.

Keduanya `None` pada step yang tidak di-map. `None`, dan bukan `-1` atau `""`:
sentinel adalah angka yang suatu saat dihitung oleh seseorang, dan string kosong
tidak dapat dibedakan dari elemen yang *memang* string kosong.

Step yang di-map **tidak mempublikasikan context** ke hilir — empat instance
tidak bisa berbagi satu key. Step yang perlu meneruskan sesuatu menulis file,
atau step sesudahnya menghitung apa yang mendarat.

## Metrik yang hanya diketahui pipeline Anda

Engine sudah mengukur apa yang dapat dilihatnya: durasi, percobaan, kedalaman
antrean. Yang tidak mungkin diketahuinya adalah berapa banyak baris yang mendarat,
berapa yang ditolak vendor, atau berapa byte yang dibuang. Step yang
mengetahuinya, dan pustaka membawanya.

```python
from brevis import metrics

metrics.set("rows_loaded", 48213)          # a gauge: the last value wins
metrics.inc("vendor_rejected_total")       # a counter: it adds up
metrics.inc("bytes_discarded_total", 4096)
```

Metrik keluar dari `/metrics` **engine**, pada port scheduler, dengan label
workflow dan step:

```
brevis_step_rows_loaded{workflow="daily_sales",step="load"} 48213
brevis_step_vendor_rejected_total{workflow="daily_sales",step="load"} 5
```

| | |
|---|---|
| `metrics.set(name, value)` | gauge. Setelah step keluar nilainya bertahan sampai run berikutnya menulis yang lain |
| `metrics.inc(name, by=1)` | counter. Bertambah lintas step dan lintas run |

### Mengapa ini tidak membuka port

Karena **step tidak dapat di-scrape**. `:9090` milik scheduler dan API — proses
berumur panjang dengan alamat stabil, di-scrape setiap lima belas sampai enam
puluh detik. Step mulai di pod-nya sendiri, berjalan empat puluh detik, lalu
keluar: port yang dibukanya tidak akan pernah di-scrape, atau sekali karena
kebetulan.

Tiga jawaban yang dikenal untuk itu adalah Pushgateway (komponen yang harus
dioperasikan, dan seri yang bertahan sampai seseorang menghapusnya), collector
OTLP (komponen **dan** dependensi di pustaka ini, yang tidak punya dan tidak akan
punya), atau membiarkan **orkestrator** membawanya. Brevis sudah membaca stdout
setiap step untuk mencari baris `@brevis:` — begitulah fase-fase SDK sampai ke
graf — dan sudah punya meter serta endpoint Prometheus.

Jadi pustaka menulis **satu baris ke stdout** dan engine mengerjakan sisanya.
Metrik tiba dengan label workflow dan step secara gratis, karena engine tahu apa
yang sedang dijalankannya; lewat Pushgateway pustaka ini harus melabeli dirinya
sendiri, dan akan salah.

Penulisannya adalah `print(flush=True)`, karena engine membaca pipe itu **secara
langsung**. Baris yang di-buffer tiba ketika proses keluar — untuk step yang
berjalan satu jam berarti terlambat satu jam, dan untuk step yang dibunuh, tidak
pernah.

### Apa yang ditolak sebuah metrik

**Nama yang tidak valid ditolak, dengan keras.** Prometheus menerima huruf,
angka, dan underscore, dan karakter pertama tidak boleh angka. Nama yang
ditolaknya tidak menghasilkan metrik yang rusak — ia menghasilkan eksposisi yang
**ditolak untuk diparse**, dan **seluruh scrape** hilang, bersama metrik lainnya.
Maka `rows-loaded` melempar `MetricError` di sini, tempat kesalahan dibuat,
alih-alih tiba dengan nama yang tidak ditulis siapa pun.

| ditolak | karena |
|---|---|
| `rows-loaded`, `rows loaded`, `2rows` | Prometheus tidak akan mem-parse-nya, dan seluruh scrape hilang |
| `metrics.set("ok", True)` | `bool` adalah `int` di Python, dan melaporkan `1` adalah angka yang artinya bergantung pada pengetahuan itu. Laporkan jumlah atau durasi, bukan state |
| `metrics.inc("x", -1)` | counter hanya naik. Setiap `rate()` atasnya mengasumsikannya, dan kerusakannya muncul di grafik berminggu-minggu kemudian |

Jalankan skrip secara manual dan barisnya tetap pergi ke stdout, tempat ia
terbaca sebagai dirinya. Tidak ada yang mengumpulkannya dan tidak ada yang gagal
— sama seperti `context.set()` di laptop.

## Apa yang ditolaknya

| | karena |
|---|---|
| lebih dari 4096 byte | platform **memotong** alih-alih menolak, sehingga objek yang terlalu besar tiba terpotong separuh dan terbaca di hilir sebagai "tidak mempublikasikan apa-apa" |
| nilai yang bukan JSON | `datetime` menjadi `"..."` atau lenyap tergantung siapa yang menserialisasi. Error menyebut key dan menyarankan `.isoformat()` |
| key yang bukan string | `{1: "x"}` kembali sebagai `{"1": "x"}` dan pencarian meleset |

Batas ini adalah angka platform, bukan angka kami. Setiap fitur "context antar
task" gagal dengan cara yang sama — seseorang menaruh **data** di tempat yang
hanya cocok untuk **context**. Empat kilobyte memuat watermark, jumlah, atau
daftar partisi. Publikasikan referensi dan biarkan baris-barisnya di tempat.

:::danger Ini bukan penyimpanan secret
Semua yang dipublikasikan terlihat oleh siapa pun yang dapat melihat run — ia
ada di status pod dan di riwayat run. Publikasikan path, bukan signed URL.
:::

## Menjalankannya secara manual

Di luar Brevis tidak ada input dan tidak ada yang membaca output. `get()`
mengembalikan default, `set()` menerima lalu membuang, dan pustaka menyatakannya
sekali pada level INFO. Ia **tidak melempar error**: skrip yang tidak bisa
dijalankan secara manual tidak bisa dikembangkan. Validasi tetap berjalan,
sehingga nilai yang berfungsi di laptop Anda berfungsi di production.

## Menguji step Anda

Seluruh kontraknya adalah dua variabel environment, jadi tidak ada yang perlu
di-mock:

```python
def test_my_step(monkeypatch, tmp_path):
    monkeypatch.setenv("BREVIS_INPUT", '{"extract": {"rows": 42}}')
    monkeypatch.setenv("BREVIS_OUTPUT", str(tmp_path / "out"))
    assert context.get("extract.rows") == 42
```

## Versi

Pustaka ini diberi versi sendiri, terpisah dari engine dan Go SDK.

:::warning Jangan gunakan `0.2.0`
Ia dikirim hanya sebagai wheel di PyPI. `0.2.1` adalah kode yang sama, lengkap.
:::

`brevis.run` membutuhkan engine **`v0.9.0` atau lebih baru** agar ada yang
dibaca; pada yang lebih lama setiap field kosong dan `run.now()` adalah jam
dinding — sama seperti yang dilakukannya di luar engine.

## Langkah berikutnya

- [Pustaka klien](/docs/libraries/) — kontrak dan bahasa lainnya
- [Konteks antar step](/docs/context/) — konsepnya
- [Parameter](/docs/parameters/) — yang dideklarasikan
