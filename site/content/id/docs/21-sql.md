---
title: SQL
description: File .sql biasa menjadi tabel dan view, berurutan sesuai dependensi — sebagai step workflow atau berdiri sendiri.
group: Pemodelan di SQL
order: 21
slug: sql
---

Tanpa templating: model adalah file yang tetap SQL valid, dan urutannya berasal dari membaca SQL itu sendiri. Postgres dan BigQuery, di bawah suite konformitas yang sama.

![Alur SQL: brevis-sql membaca raw.orders, tabel yang didaratkan SDK atau Gateway. Model staging.stg_orders menjadi view di atasnya, dan marts.orders menjadi tabel di atas view itu; urutannya berasal dari membaca SQL. Model yang sama dibangun di Postgres atau BigQuery.](/assets/flow-sql.svg)

## Enam command

| command | fungsinya |
|---|---|
| `brevis-sql compile` | mem-parse setiap model dan menyelesaikan setiap edge, **tanpa terhubung ke apa pun** |
| `brevis-sql graph` | mencetak edge yang disimpulkan, agar yang salah terlihat dan tidak ditemukan belakangan |
| `brevis-sql build` | membuat atau mengganti setiap model, berurutan sesuai dependensi |
| `brevis-sql test` | menjalankan test setiap model; masing-masing adalah SELECT yang tidak boleh menemukan apa-apa |
| `brevis-sql serve` | menjawab preview dan query read-only lewat HTTP, sehingga console tidak membutuhkan kredensial warehouse |
| `brevis-sql generation bump` | mencatat bahwa sebuah tabel dibangun ulang, sehingga jawaban cache `brevis-sql-api` atasnya menjadi basi — `build --generations-from` melakukannya untuk setiap model yang dibangunnya |

`--select orders+` mempersempit salah satunya ke satu model dan semua yang ada di hilirnya.


## `brevis-sql serve` — membaca warehouse tanpa kredensial

Layar Data dan SQL di console menampilkan apa yang mendarat dan apa isinya. Tidak satu pun menjangkau warehouse: mereka bertanya ke layanan ini, yang berjalan di sebelahnya dan memegang kredensial yang sengaja tidak dimiliki engine.

```bash
brevis-sql serve --connections brevis.yaml --addr 127.0.0.1:8088
```

Batas atas bukanlah anggaran: `--max-bytes` membatasi satu query, dan `--budget` membatasi jumlahnya per koneksi per jam — diperiksa terhadap kuotasi, sehingga penolakan datang sebelum uang dibelanjakan. Ia hanya membatasi warehouse yang melaporkan byte, yaitu BigQuery dan bukan Postgres; banner boot menyatakan postur mana yang berlaku, termasuk tidak punya sama sekali.

**Hanya yang dideklarasikan file yang dapat dijangkau**, dan secara default tidak ada yang dilakukannya adalah penulisan: setiap statement diklasifikasikan sebelum berjalan, pembacaan yang akan memindai lebih dari `--max-bytes` dihargai dan ditolak sebelum berbiaya apa pun, dan warehouse sendiri diminta menolak penulisan. Baris audit membawa hash dari statement dan tidak pernah statement-nya.

**Ia tidak lagi membutuhkan service account sendiri.** `brevis-sql build` membuat tabel, sehingga role-nya bisa menulis, dan layanan yang menolak role semacam itu berarti akun kedua untuk dibuat, dijaga, dan dirotasi. Role tidak lagi satu-satunya yang menahan garis: Postgres menjalankan setiap statement di dalam transaksi yang dibuka `READ ONLY` — di mana `SELECT nextval('s')`, penulisan yang tidak dapat dilihat classifier mana pun, kembali ditolak oleh Postgres sendiri — dan BigQuery melaporkan jenis statement pada dry run yang menghargainya, sehingga `CREATE TABLE` atau `EXPORT DATA` ditolak berdasarkan putusan warehouse sendiri. Warehouse yang tidak dapat menawarkan keduanya tetap menolak kredensial yang dapat menulis.

Di luar `BREVIS_ENV=local` token wajib ada dan layanan tidak akan start tanpanya.

### Warehouse pengembangan yang boleh diubah seseorang

```bash
BREVIS_SQL_SERVE_WRITES=warehouse_dev
```

Ia **menyebut koneksi**. Satu layanan menjangkau beberapa warehouse, dan membuka kunci yang Anda kembangkan tidak boleh membuka kunci yang ada di sebelahnya. Pada koneksi yang disebut, setiap lapisan mundur dan layanan ini sekuat kredensialnya — batas baris membatasi satu layar dan batas byte membatasi uang, dan tidak satu pun membatasi apa yang diubah sebuah statement.

Token wajib di mana pun ia diatur, dan `BREVIS_ENV=local` bukan pengecualian. Banner boot menyebut setiap koneksi yang dibuka kuncinya, console menggambar banner di atas editor, dan `brevis_sql_writable_connection` muncul di metrik. **Biarkan tidak diatur di production**; tidak ada manifest di repositori ini yang mengaturnya.

Token dapat **diberi nama**: `BREVIS_SQL_SERVE_TOKEN_CONSOLE=…` mendeklarasikan pemanggil bernama `console`, dan setiap baris audit yang dihasilkan bearer itu membawa `"caller":"console"`. Identitas adalah kredensialnya dan bukan header yang diisi pemanggil, sehingga log tidak bisa mengatakan apa pun yang tidak dipegang oleh pemegang token itu. Token yang ditolak juga meninggalkan baris, dan tidak pernah mencatat apa yang disajikan.

## Model adalah file yang tetap SQL valid

Konfigurasi adalah komentar blok di awal, sehingga file terbuka di editor
dengan syntax highlighting, berjalan di console secara manual, dan `psql -f`
mengeksekusinya.

```sql
/* brevis
materialized: table
tests:
  - not_null: [order_id]
  - unique: [order_id]
  - relationships: {column: customer_id, to: staging.stg_customers, field: customer_id}
*/
select * from staging.stg_orders
```

**Tanpa templating.** `models/<schema>/<name>.sql` adalah `<schema>.<name>`, dan
dependensi berasal dari membaca SQL alih-alih dari pemanggilan fungsi yang harus
Anda tulis. `brevis-sql graph` mencetak apa yang disimpulkan; `depends_on:` di
header adalah override eksplisit untuk kasus langka yang terlewat.

## Dari compile hingga test

Dua model: view yang membersihkan `raw.orders`, dan tabel yang bergantung padanya.

`models/staging/stg_orders.sql`:

```sql
/* brevis
materialized: view
*/
select order_id, customer_id, amount, ordered_at
from raw.orders
where amount is not null
```

`models/marts/orders.sql`:

```sql
/* brevis
materialized: table
tests:
  - not_null: [customer_id]
  - unique: [customer_id]
*/
select customer_id, count(*) as orders, sum(amount) as revenue
from staging.stg_orders
group by customer_id
```

`compile` dan `graph` tidak terhubung ke apa pun:

```bash
docker run --rm -v ./project:/project:ro brevissh/brevis-sql:0.6.0 \
  compile --project /project
```

```text
2 models, 2 to build, 0 function(s)
  staging.stg_orders                 view  0 test(s)
  marts.orders                       table 2 test(s)
```

```bash
docker run --rm -v ./project:/project:ro brevissh/brevis-sql:0.6.0 \
  graph --project /project
```

```text
staging.stg_orders
  └─ raw.orders (source)
marts.orders
  ├─ staging.stg_orders
```

`build` dan `test`, terhadap Postgres:

```bash
docker run --rm -v ./project:/project:ro \
  -e BREVIS_SQL_DSN=postgres://user:pass@host:5432/db brevissh/brevis-sql:0.6.0 \
  build --project /project --dsn-from BREVIS_SQL_DSN
```

```text
  staging.stg_orders                 view  new   4ms
@brevis:{"type":"landed","target":"postgres://postgres/staging/stg_orders"}
  marts.orders                       table new   4ms
@brevis:{"type":"landed","target":"postgres://postgres/marts/orders","rows":2}
2 models built on postgres
```

```bash
docker run --rm -v ./project:/project:ro \
  -e BREVIS_SQL_DSN=postgres://user:pass@host:5432/db brevissh/brevis-sql:0.6.0 \
  test --project /project --dsn-from BREVIS_SQL_DSN
```

```text
2 tests passed on postgres
```

Output nyata, dari image yang dipublikasikan — compile dan graph tidak terhubung ke apa pun. Terhadap Postgres, build dan test berakhir dengan "2 models built" dan "2 tests passed".

### Ketika test gagal

Dengan order yang tidak punya customer di `raw.orders`, `build` lolos dan test `not_null` tidak. Kegagalan menyebut model, kolom, berapa baris, dan **query-nya** — yang Anda tempel ke console dan terus dipersempit:

```text
FAIL  marts.orders                   not_null         customer_id          1 row(s)
      SELECT COUNT(*) FROM (SELECT customer_id FROM marts.orders WHERE customer_id IS NULL) AS v
brevis-sql: 1 of 2 tests failed
```

Proses keluar dengan kode 1, dan step workflow gagal bersamanya.

## Model inkremental dan lookback-nya

`materialized: incremental` membutuhkan `unique_key` dan `watermark`. Setiap build
hanya membaca baris di atas watermark terbesar yang sudah ada di target, lalu
menggabungkannya berdasarkan kunci dengan MERGE. Filter itu ketat, sehingga baris yang
sama dengan watermark tetapi di-commit setelah build, atau yang datang terlambat
dengan timestamp lebih lama, tidak terlihat sampai `--full-refresh`. `lookback`
opsional mengatasinya:

```sql
/* brevis
materialized: incremental
unique_key: [order_id]
watermark: updated_at
lookback: 2h
*/
```

Nilainya bilangan bulat diikuti `s`, `m`, `h`, atau `d`; selain itu ditolak dengan
menyebut nama model. Filter menjadi `updated_at >= max - 2h`, dan MERGE menyerap
baris yang berulang. Watermark harus berupa kolom TIMESTAMP saat lookback diatur.
Tanpa `lookback`, SQL tidak berubah.

## Test adalah SELECT yang mengembalikan baris pelanggar

`not_null`, `unique`, `accepted_values`, `relationships`. Nol baris berarti lolos, dan tidak ada lagi yang perlu ditafsirkan.

## Ia menyatakan apa yang ditulisnya

Setiap model yang dimaterialisasi mencetak baris `@brevis:` yang sudah dibaca
engine Brevis, sehingga ia tampil di `/data` berdampingan dengan yang lain (lihat
output `build` di atas). View melaporkan tanpa jumlah baris alih-alih nol: ia
tidak menyimpan apa-apa, dan nol di sana tidak dapat dibedakan dari tabel yang
memang kosong.

## Dua warehouse, satu suite

PostgreSQL dan BigQuery (`--dialect bigquery`), dan keduanya menjalankan suite
test yang sama — ditulis ketika masih ada satu implementasi, agar yang kedua tidak
diam-diam menyimpang.

BigQuery mengautentikasi dengan Application Default Credentials: pada workload
identity, itu adalah identitas pod sendiri, dan `--dsn-from` menyebut variabel
yang menyimpan project id.

## 13 MB

Distroless, nonroot, tanpa shell. Klien BigQuery resmi berukuran 31 MB dan 526
paket dalam binary kosong, sehingga ini berbicara ke BigQuery lewat endpoint REST-
nya — sekitar 200 baris, dengan test untuk setiap cara ia bisa salah secara
diam-diam. Sebuah gerbang di repositori menolak klien itu dengan menyebut
namanya, sehingga keputusan ini bertahan dari kenaikan dependensi rutin.

## Diverifikasi terhadap dbt

jaffle-shop, dibangun oleh image ini pada data seed yang sama dengan yang dipakai dbt:

|  | hasil |
|---|---|
| baris dan md5 setiap baris, per model | **identik, 13 dari 13** |
| kolom dan tipe, per model | **identik, 13 dari 13** |
| 13 model, build pertama | 1,56 d |

## Yang tidak dilakukannya

**Tidak ada kompatibilitas dbt**, dan itu keputusan, bukan celah. Sekitar sepertiga
model dalam proyek dbt yang nyata tidak memakai apa pun selain `ref`, `source`,
dan `config`; sisanya memakai macro, dan package adalah templating by design.
Setengah kompatibilitas yang tidak dapat diprediksi siapa pun lebih buruk daripada
tidak ada.

## Ke mana selanjutnya

| jika Anda ingin | buka |
|---|---|
| melihat posisi SQL di antara empat bagian | [Ekosistem](/docs/ecosystem/) |
| menjalankan brevis-sql sebagai step terjadwal | [Core](/docs/core/) |
| mendaratkan data yang akan dimodelkannya | [Go SDK](/docs/sdk/) atau [Ingestion](/docs/ingestion/) |
