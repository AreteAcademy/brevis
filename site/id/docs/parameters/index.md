# Parameter

> Apa yang berubah di antara dua run workflow yang sama, tanpa mengedit file.

*https://brevis.sh/id/docs/parameters/ · brevis.sh docs (id)*

---

Sebuah workflow dapat mendeklarasikan **parameter run**: nilai yang berubah di
antara dua pemicu tanpa file berubah.

## Mendeklarasikan

```yaml
params:
  - name: load_full
    type: boolean
    default: "false"

  - name: start_date
    type: string
    pattern: '^\d{4}-\d{2}-\d{2}$'

steps:
  - id: run
    run: dbt build --vars '{"load_full":"{{ .load_full }}"}' --select bronze+
```

| field | | |
|---|---|---|
| `name` | **wajib** | nama yang dipakai di template dan di `--param` |
| `type` | | `string`, `boolean`, `integer`, atau `list|<type>` untuk banyak nilai |
| `default` | | dipakai ketika pemicu tidak memberikan nilai |
| `pattern` | | regular expression yang harus dipenuhi nilai |
| `title` | | label yang ditampilkan form, hingga 60 karakter; jika tidak ada, namanya |
| `group` | | mengelompokkan parameter di form, hingga 60 karakter |
| `warn` | | biaya dari nilai selain default, ditampilkan di bawah field, hingga 200 karakter |
| `uses` | | key konektor yang dituju parameter (`bigquery`, `postgres`), untuk logonya |

Parameter **tanpa `default`** bersifat wajib: pemicu yang menghilangkannya gagal
sebelum menjalankan step apa pun.

`title`, `group`, `warn`, dan `uses` adalah cara form menampilkan parameter, dan
**tidak satu pun mengubah nilai apa yang boleh diberikan**. Masing-masing satu
baris; `uses` berupa huruf kecil, angka, dan `-`. Publish menolak yang lain,
dengan menyebut parameternya.

```yaml
params:
  - name: load_full
    type: boolean
    default: "false"
    title: Full load
    group: Load
    warn: reprocesses the whole table; it costs
    uses: bigquery
```

## Daftar nilai

`list|<type>` mendeklarasikan parameter yang membawa banyak nilai — tabel yang
akan dimuat, layer yang akan dibangun ulang, jendela yang akan di-backfill:

```yaml
params:
  - name: tables
    type: list|string
    default: "users,orders"

  - name: layers
    type: list|string
    enum: [bronze, silver, gold]     # an enum restricts each ITEM
    default: "bronze"

  - name: days
    type: list|integer
    default: "1,7,30"
```

Elemennya dapat berupa `string`, `integer`, atau `boolean`. Setiap item
divalidasi sendiri-sendiri, sehingga `list|integer` yang diberi `1,x,30` gagal
dengan menyebut **item 2** alih-alih seluruh nilai.

Di mana pun ia adalah **satu string yang dipisahkan koma**: di database, di
`BREVIS_RUN_PARAMS`, dan di command. Itulah yang membuatnya bekerja di sebuah
step tanpa kasus khusus sama sekali —

```yaml
run: dbt build --select {{ .tables }}      # dbt build --select users,orders
```

— dan itulah mengapa koma tidak boleh muncul di dalam item. Item kosong dan item
berulang juga ditolak: pengulangan diam-diam menggandakan apa pun yang dilakukan
step per item.

### Sebagai array, di dalam step

```go
for _, table := range p.Run.ParamList("tables") {
    load(table)
}
```

```python
from brevis import run

for table in run.param_list("tables"):
    load(table)
```

Keduanya mengembalikan `[]` untuk parameter yang tidak diisi siapa pun, alih-alih
list berisi satu string kosong: mengiterasi "tidak ada yang dikerjakan"
seharusnya tidak melakukan apa-apa. Item dikembalikan sebagai string — termasuk
`list|integer` — karena step lebih tahu daripada pustaka apa yang harus
dilakukan dengan angka.

### Di antarmuka

List dengan `enum` dirender sebagai checkbox, yang tidak membutuhkan JavaScript
dan yang dikirim browser satu nilai per kotak yang dicentang. Tanpa `enum` ia
adalah field teks yang itemnya menjadi chip yang dapat dihapus, dan dengan
JavaScript nonaktif ia tetap menjadi field berisi `users,orders` — persis yang
diharapkan server.

### Di command line

```bash
brevis run wf.yaml --param tables=events,sessions --param days=1,30
```

:::note Engine yang lebih lama tidak akan membacanya
Tipe disimpan bersama workflow, dan engine dari sebelum fitur ini menolak
`list|string` saat publish sebagai tipe yang tidak dikenal. Upgrade engine
sebelum mempublikasikan workflow yang memakainya.
:::

## Memakainya di command

Nilai masuk ke `run` lewat template, dalam kurung kurawal ganda dengan titik:

```yaml
run: ./load.sh --since {{ .start_date }}
```

## Memberikannya di command line

```bash
brevis run wf.yaml --param load_full=true
brevis run wf.yaml --param load_full=true --param start_date=2026-01-01
```

`--param` dapat diulang. Input tanpa `=` adalah **error**:

```
erro: --param "load_full": use chave=valor
```

Itu disengaja. `--param load_full`, dengan lupa memberi nilai, akan berjalan
dengan default — dan operator akan mengira backfill terjadi dengan parameter yang
dimaksudkannya.

## Di backfill

Nilai berlaku untuk **setiap** slot dalam rentang. Ini kasus penggunaan utama:
"proses ulang seluruh Januari dengan `load_full=true`".

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

## Di antarmuka

Workflow dengan `params` mendapat **form** sebagai pengganti tombol pemicu
sederhana. Field berasal dari deklarasi, dan `pattern` divalidasi sebelum
pengiriman.

## Di environment step

Parameter run juga masuk ke **environment** setiap step, dengan prefiks. Ini ada
agar fetcher yang ditulis dengan [SDK](/id/docs/sdk/index.md) dapat melihatnya tanpa
menerima apa pun sebagai argumen:

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
    if p.Run.Params["load_full"] == "true" {
        p.Source.URL += "&full=1"
    }
    return nil
},
```

## Auto params

Setiap run juga membawa **auto params**: nilai yang dihitung sendiri oleh engine.
Tidak ada yang mendeklarasikannya, setiap run memilikinya, dan semuanya ada di
layar run.

Yang terpenting adalah `adjusted_at`, dan bug yang dihilangkannya layak disebut.
Sebuah fetcher membaca `now()`, mengurangi jendelanya, dan meminta vendor data dua
jam terakhir. Pada run yang mulai tepat waktu itu benar. Pada run yang ditunda
antrean selama empat puluh menit, itu salah empat puluh menit — dan empat puluh
menit itu milik **tidak ada run sama sekali**, karena slot berikutnya juga
membaca `now()`-nya sendiri. Tidak ada yang gagal; datanya begitu saja hilang,
dan baru ditemukan berminggu-minggu kemudian.

Maka engine menyerahkan sebuah jam. Baca `adjusted_at` di tempat Anda biasanya
membaca `now()`:

| param | | |
|---|---|---|
| `adjusted_at` | selalu | jam yang seharusnya dibaca run ini: slot bila ada, jika tidak waktu mulai |
| `date` | selalu | `adjusted_at` sebagai `YYYY-MM-DD`, UTC — partisi, folder, `WHERE` |
| `delay_seconds` | selalu | seberapa terlambat percobaan ini terhadap slot-nya; `0` pada run manual tanpa slot |
| `previous_error` | selalu | run sebelum ini tidak berhasil |
| `scheduled_at` | dengan slot | slot yang diminta cron |
| `started_at` | | kapan percobaan ini dimulai; retry menggesernya |
| `interval_start` | dengan slot | slot **sebelumnya** |
| `interval_end` | dengan slot | slot ini, **tidak termasuk** |
| `previous_success_at` | | slot dari run terakhir yang berhasil |

Sebuah run punya slot ketika jadwal yang membuatnya, ketika backfill yang
membuatnya, atau ketika dipicu secara manual **untuk slot masa lalu**: field
`slot` pada pemicu (`2026-10-27T04:00:00Z`) menjalankan slot itu lagi, dengan
param slot tersebut. Slot yang tidak pernah dipicu cron ditolak, dengan
menyebutkan slot terdekat sebelumnya.

**Setiap timestamp di sini adalah UTC**, ditulis sebagai RFC 3339 dengan `Z`, dan
layar run juga menampilkannya dalam UTC — sehingga halaman dan
`$BREVIS_AUTO_ADJUSTED_AT` tidak pernah berbeda. `date` adalah **hari UTC**, yang
untuk workflow yang slot-nya melintasi tengah malam UTC bukanlah hari lokal:
`0 22 * * *` di `America/Sao_Paulo` memiliki `date` hari berikutnya. `ds` milik
Airflow berperilaku sama, dengan alasan yang sama — slot adalah sebuah instan,
dan sebuah instan baru punya satu hari setelah zona waktu dipilih.

`interval_start` dan `interval_end` berasal dari **cron**, bukan dari riwayat:
backfill slot bulan Maret menghasilkan jendela yang dimiliki Maret, bukan jendela
yang kebetulan digambarkan oleh run workflow ini hari ini. Pipeline yang meminta
`[start, end)` tidak pernah tumpang tindih dan tidak pernah berlubang, seberapa
pun terlambatnya ia berjalan dan seberapa sering ia di-retry.

Nilai-nilai itu adalah **snapshot**, dihitung ketika run mulai dan disimpan pada
run tersebut. `previous_error` adalah fakta tentang saat run ini dimulai;
menghitung ulang besok — setelah run sebelumnya di-retry dan lolos — akan
menjawab pertanyaan yang berbeda dengan nama yang sama.

### Membacanya

Di step shell, satu variabel per nilai, sehingga tidak ada yang perlu diparse:

```yaml
steps:
  - id: extract
    run: |
      curl "https://api.example.com/events?since=$BREVIS_AUTO_INTERVAL_START&until=$BREVIS_AUTO_INTERVAL_END" \
        > "/data/$BREVIS_AUTO_DATE.json"
```

Seluruh set juga ada di `$BREVIS_AUTO_PARAMS` sebagai satu objek JSON, untuk step
yang lebih suka mem-parse sekali.

Di Go, [SDK](/id/docs/sdk/index.md) membacakannya untuk Anda:

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
    start, end, ok := p.Run.Auto.Window()
    if !ok { // no schedule: fall back to a fixed window
        start, end = p.Run.Auto.Now().Add(-24*time.Hour), p.Run.Auto.Now()
    }
    p.Source.URL += "&from=" + start.Format(time.RFC3339) +
        "&to=" + end.Format(time.RFC3339)
    return nil
},
```

`Auto.Now()` adalah jam yang dibaca sebagai pengganti `time.Now()`. Di luar
engine — fetcher yang dijalankan seseorang secara manual — ia *adalah* jam dinding,
sehingga tidak ada yang perlu diperlakukan khusus untuk pengembangan lokal.

Di Python, `pip install brevis` membaca nilai yang sama:

```python
from datetime import timedelta
from brevis import run

since, until = run.window() or (run.now() - timedelta(days=1), run.now())
df = fetch(since, until)
df.to_parquet(f"/data/{run.auto().date}.parquet")
```

`window()` mengembalikan `None` ketika workflow tidak punya jadwal, alih-alih
sepasang waktu nol — query dari epoch memilih segalanya, dan kegagalan itu tidak
boleh diam-diam. `or` di atas adalah seluruh fallback-nya.

### Di dlt, tidak ada yang perlu dibaca

[dlt](https://dlthub.com) menyimpan kursornya sendiri, dalam state yang
ditulisnya ke tujuan. State itu hanya bergerak **maju**, yang tidak masalah sampai
Anda menjalankan ulang slot lama: kursor masih menunjuk hari ini, sehingga
backfill membaca jendela hari ini, dan jawaban dlt sendiri untuk itu adalah
membuang state.

Jendela yang diserahkan Brevis berasal dari cron, sehingga run Maret menghasilkan
jendela Maret seberapa pun seringnya di-retry. dlt akan menerimanya — namanya
milik dlt sendiri, dibaca sebelum ia mencari Airflow, dan engine
menyetelnya pada setiap run terjadwal:

```python
@dlt.resource(primary_key="id")
def events(
    updated_at=dlt.sources.incremental(
        "updated_at",
        initial_value=datetime(1970, 1, 1, tzinfo=timezone.utc),
        allow_external_schedulers=True,   # this line is the whole integration
    )
):
    yield from api.events(since=updated_at.start_value, until=updated_at.end_value)
```

`allow_external_schedulers=True` adalah seluruhnya. `$DLT_INTERVAL_START` dan
`$DLT_INTERVAL_END` menjadi `initial_value` dan `end_value` milik dlt, setengah
terbuka di kedua sisi — baris yang tepat berada di awal masuk, yang tepat berada
di akhir tidak, dan itulah yang membuat slot berurutan tidak berlubang maupun
tumpang tindih.

Incremental dengan `end_value` **tidak menyentuh state yang dipersistenkan**.
Itulah bagian yang penting: menjalankan ulang satu slot bersifat idempoten, dan
backfill bulan Maret tidak menggeser kursor yang diandalkan run malam.

Dua hal yang perlu diketahui sebelum ini berfungsi:

- **Kursor harus berupa datetime, bukan string.** dlt tidak akan memaksa kursor
  `text` untuk perbandingan dan menolak bergabung dengan scheduler sama sekali,
  dengan `JoinSchedulerError` yang tidak jelas mengatakannya. Konversikan pada
  resource — `add_map` — atau beri tipe pada `initial_value` seperti di atas.
- **Workflow tanpa jadwal tidak menyerahkan apa pun**, dan dlt lalu melempar
  `ExternalSchedulerNotAvailable` alih-alih diam-diam kembali ke kursornya. Jika
  sebuah pipeline harus bekerja dengan kedua cara, biarkan
  `allow_external_schedulers` mati dan serahkan jendelanya sendiri dari
  `run.window()`.

Mendeklarasikan `tools: [dlt]` pada step adalah hal terpisah, dan hanya
menggambar chip di graf — jendela diserahkan dengan atau tanpanya.

## Snapshot

Parameter disimpan **di dalam run**, tidak dibaca dari workflow saat eksekusi. Run
membawa input yang memicunya, dan log eksekusi Januari terus menampilkan nilai
Januari bahkan setelah default berubah.

## Langkah berikutnya

- [Konfigurasi](/id/docs/configuration/index.md) — variabel environment proses
- [SDK](/id/docs/sdk/index.md) — bagaimana fetcher membaca context run
- [Konteks antar step](/id/docs/context/index.md) — apa yang dipublikasikan sebuah step untuk step berikutnya
