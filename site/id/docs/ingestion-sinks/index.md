# Sink ingestion

> Enam tujuan, cara setiap database menulis record, dan mengapa image hanya membawa apa yang Anda pakai.

*https://brevis.sh/id/docs/ingestion-sinks/ · brevis.sh docs (id)*

---

Enam tujuan, dan daftarnya sengaja dibuat eksplisit — konektor yang
*direncanakan* dan konektor yang *berjalan* adalah dua hal yang berbeda bagi
orang yang sedang memilih ini.

| sink | state | apa itu sebuah penulisan | dibuktikan terhadap |
|---|---|---|---|
| `pubsub` | **berjalan** | satu publish per batch, atribut dan ordering key dari field event itu sendiri | emulator |
| `postgres` | **berjalan** | `COPY FROM STDIN`, atau `INSERT` bertahap dalam satu transaksi | Postgres sungguhan |
| `mysql` | **berjalan** | `INSERT` multi-baris dalam transaksi, atau `INSERT IGNORE` | MySQL sungguhan |
| `files` | **berjalan** | objek NDJSON dalam direktori, `gs://` atau `s3://` | filesystem, dan S3 |
| `bigquery` | **berjalan** | baris dimuat, atau di-stage lalu di-`MERGE` pada `ingestion_id` | **hanya config — tidak ada emulator** |
| `redshift` | **berjalan** | batch ke S3, lalu `COPY`, lalu `MERGE` | **hanya config — tidak ada emulator** |

**Dua dari enam belum terbukti ujung ke ujung**, dan tabel menyatakannya alih-alih
membiarkan kata "berjalan" membawa klaim yang tidak diperiksa siapa pun. BigQuery
dan Redshift tidak punya emulator lokal, sehingga yang diuji adalah config,
penolakan, dan driver di bawahnya — yang sudah dicakup oleh suite SDK sendiri.
Penggunaan production pertama dari keduanya adalah bukti sesungguhnya, dan
sebaiknya pada stream yang taruhannya rendah.

`type:` yang tidak dikenal **ditolak saat startup**, dengan menyebut apa yang
dibawa *binary ini*. Gateway yang start pada sink yang tidak dimilikinya adalah
gateway yang membuang event karena alasan yang tidak terlihat siapa pun.

`gs://` dan `s3://` bukan konektor terpisah: `files` membaca ketiga bentuk path,
sehingga dead letter berupa folder di laptop dan bucket di production tanpa
perubahan apa pun di sini.

## Bagaimana masing-masing menulis

`write` **wajib dan tidak pernah diberi default** pada keempat tujuan yang menulis
ke tabel, karena kedua mode itu benar-benar berbeda dan hanya pemilik tabel yang
tahu mana yang dimaksud:

| | `append` | `merge` |
|---|---|---|
| **postgres** | `COPY FROM STDIN` | `INSERT … ON CONFLICT (ingestion_id) DO NOTHING` |
| **mysql** | `INSERT` multi-baris per blok | `INSERT IGNORE` |
| **bigquery** | baris dimuat (lewat GCS di atas batas inline) | `MERGE … WHEN NOT MATCHED THEN INSERT` |
| **redshift** | `COPY` dari S3 | `COPY` ke tabel staging, lalu `MERGE` |

`merge` berarti **hal yang sama di keempatnya**: insert idempoten, dan
**pengiriman pertama menang**. Pengiriman ulang diabaikan, tidak diterapkan —
yang benar untuk event, yang terjadi sekali dan tidak berubah, dan salah untuk
baris yang membawa state yang dapat berubah.

Postgres dan MySQL **mensyaratkan index `UNIQUE` pada `ingestion_id`** untuk
`merge`, dan menolak tanpanya alih-alih menambahkan secara diam-diam:

```sql
CREATE UNIQUE INDEX CONCURRENTLY ON landing.orders (ingestion_id);
```

Tanpa index, `ON CONFLICT` tidak punya apa-apa untuk dicocokkan, dan setiap
pengiriman ulang akan menduplikasi ke tabel yang pemiliknya meminta sebaliknya.
Penolakan itu bukan batch yang hilang: ia di-retry, lalu dikuburkan di dead letter
dengan alasan dan `CREATE INDEX` pada record — tidak ada yang kehilangan event
selagi seseorang membuat index.

**`upsert` ditolak dengan menyebut namanya.** `ON CONFLICT DO UPDATE`, ketika
pengiriman *terakhir* menang, adalah mode yang nyata yang **belum
diimplementasikan**. Menerima kata itu sambil berperilaku seperti `merge` adalah
persis kegagalan yang dicegah field tersebut, jadi config menyatakannya.

## Masing-masing dengan apa yang hanya benar tentangnya

```yaml
# Postgres: schema-qualified name, and the table has to exist.
sink: {type: postgres, dsn_from: BREVIS_DSN, table: landing.clicks, write: merge}

# MySQL: no COPY, so throughput is an order below Postgres on the same
# hardware. Flush wider here.
sink: {type: mysql, dsn_from: BREVIS_DSN, table: landing.clicks, write: merge}

# BigQuery: no dsn_from. It authenticates with the pod's own credentials, which
# is what a workload identity is for. The name has no dots — the project and
# the dataset are their own fields.
sink: {type: bigquery, project: acme-prod, dataset: landing, table: clicks, write: merge}

# Redshift: two hops, not one. It is columnar, a row-by-row INSERT pays the cost
# of a block, and the only workable load is COPY from S3. So every batch becomes
# an object in the staging prefix and then a COPY — a stream flushing every
# second writes 86,400 objects a day. Flush much wider here.
sink:
  type: redshift
  dsn_from: BREVIS_RS_DSN
  table: landing.clicks
  staging: s3://acme-staging/gateway/
  iam_role: arn:aws:iam::123456789012:role/redshift-copy
  write: merge
```

`dsn_from` menyebut **variabel environment** yang menyimpan connection string,
bukan string-nya: DSN membawa password dan file itu ada di git. Ini pemisahan
yang sama dengan yang dilakukan `secrets:` di workflow.

`iam_role` adalah role dan driver **tidak akan menerima key**: key di URL
`COPY` berakhir di query log klaster, yang banyak orang membacanya.

## Tanpa deklarasi kolom

Gateway tidak mengirim daftar kolom. Pipeline mendeklarasikan kolomnya sendiri
karena barisnya punya satu bentuk yang dikendalikannya; batch gateway adalah apa
pun yang dikirim *N* klien dalam satu jendela flush, dan bentuknya boleh berbeda.

Driver menyelesaikan daftar dari **tabel itu sendiri**, diiriskan dengan apa yang
dibawa batch, **sebelum** menyentuh server. Field yang tidak dimiliki tabel
ditolak dengan pesan yang memperbaikinya, alih-alih gagal di tengah `COPY` dengan
`column "x" of relation "y" does not exist`; field yang dihilangkan satu event
ditulis sebagai `NULL`.

## Daftar import adalah pilihannya

Sink **dikompilasi masuk**, seperti hook, dan binary mendaftarkan apa yang
dimilikinya:

```go
func main() {
    sinks := gateway.NewSinks()
    sinks.MustRegister(postgres.Sink, postgres.New)
    sinks.MustRegister(files.Sink, files.New)
    gateway.Main(nil, gateway.WithSinks(sinks))
}
```

Linker Go memangkas apa yang tidak dirujuk apa pun, sehingga binary yang tidak
pernah mengimpor driver BigQuery tidak membawa BigQuery — atau Arrow, atau
Storage Write API. Begitulah `database/sql` selalu bekerja.

Nilainya sebesar yang terdengar:

| build | paket | binary |
|---|---|---|
| keenam sink, kedua object store, kedua metastore | sekitar 900 | 55,0 MB |
| `postgres` + `files` lokal | sekitar 240 | **10,2 MB** |

Object store adalah registry **kedua**, karena skema bukan tujuan: `files` adalah
satu sink yang menulis ke direktori, ke `gs://`, dan ke `s3://`, dan build yang
hanya menulis secara lokal tidak seharusnya membawa AWS SDK untuk melakukannya.

### Dua image yang dipublikasikan

| image | membawa | pull (amd64) |
|---|---|---|
| `areteacademy/brevis-gateway:0.32.0` | enam sink, S3, GCS, Redis, memcached | 19,1 MB |
| `areteacademy/brevis-gateway:0.32.0-slim` | `postgres`, `files` lokal, `auto_table` | **5,2 MB** |

Keduanya dari satu build atas satu tree, sehingga kedua tag selalu commit yang
sama. Tidak ada `latest-slim` — `latest` sudah merupakan tag yang tidak
seharusnya di-deploy siapa pun.

**Ingin pasangan yang berbeda?** `cmd/gateway-slim` hanya lima belas baris dan
blok import adalah seluruh konfigurasinya. Salin, ubah import, build. Siapa pun
yang menginginkan hook sudah mengompilasi binary-nya sendiri, jadi memilih sink
tidak menambah biaya apa pun.

### Penolakan menyebut build mana yang Anda pegang

```
sink type "bigquery" is not one this binary carries (it has: files, postgres).
Sinks are compiled in, so this is a build that left it out rather than a
destination that does not exist.
```

Daftar yang tetap akan mengirim orang itu mencari kesalahan config yang tidak
dibuatnya. Hal yang sama berlaku untuk object store: dead letter `s3://` pada
binary tanpa backend S3 ditolak **saat startup**, dengan menyebut skemanya,
alih-alih pada batch pertama yang harus dikuburkannya.

## Satu rute, N tabel, tanpa deklarasi

```yaml
sink:
  type: auto_table
  shape: columns
  naming: {pattern: '^(app|svc)_[a-z0-9_]{2,40}$', allow: [app_, svc_], max_new_per_hour: 20}
  metastore: {type: redis, addr_from: BREVIS_METASTORE_ADDR, ttl: 60s}
  into: {type: bigquery, project: acme-prod, dataset: landing, write: merge}
```

`auto_table` **merutekan dan tidak menulis**: `into` yang menulis, dan ia tidak
membawa `table` — tabel berasal dari setiap event. `into` dengan `table` ditolak
saat startup, karena tabel tetap di bawah router akan menang secara diam-diam dan
setiap event akan mendarat di sana.

### Envelope

Body adalah sebuah **envelope**: field kontrol di atas, record di dalam `data`.

```json
{
  "table_name": "app_orders",
  "description": "App orders",
  "operation": "INSERT",
  "unique_key": "id",
  "data": {
    "id": "A-1",
    "total": 150,
    "customer": {"id": 7, "uf": "SP"},
    "items": [{"sku": "X", "qty": 2}]
  }
}
```

| field | | apa itu |
|---|---|---|
| `table_name` | **wajib** | ke mana ini mendarat |
| `data` | **wajib** | record, dan hanya itu |
| `unique_key` | default `id` | field `data` mana yang mengidentifikasi record — **wajib di bawah `merge`** |
| `operation` | default `INSERT` | `INSERT`, `UPDATE`, atau `DELETE` |
| `description` | opsional | **diterima dan belum dipakai** — lihat di bawah |

`table_name` yang berdampingan dengan `total` dan `customer` adalah field
**transport** yang menyamar sebagai field **record**. Memisahkannya adalah cara
setiap format CDC dibentuk — dan itulah yang memungkinkan prefiks dicadangkan,
karena kini ada satu tempat yang hanya ditulis produser.

### Key milik `merge`, bukan milik envelope

`unique_key` opsional, dan apakah ia **wajib** bergantung pada mode tulis —
karena kedua mode menginginkan hal yang berbeda:

| | |
|---|---|
| `write: merge` | **wajib.** Mode ini ada untuk menjaga satu baris per record, dan `brevis_record_key` adalah arti dari "per record": `qualify` yang menyelesaikan versi terkini mempartisi menurutnya. Tabel merge yang penuh baris tanpa menyebut record adalah tabel yang tidak dapat diselesaikan siapa pun. |
| `write: append` | **opsional.** Tabel append adalah log, dan entri log tidak harus *tentang* sebuah record: baris audit, webhook, sampel metrik. Menuntut `id` di sana akan membuat produser mengarangnya, yang lebih buruk daripada `NULL` karena tampak nyata. |

Di bawah `append` kolom dibuat nullable dan baris tanpa key membawa `NULL` —
bukan `""`, yang merupakan fakta lain. Dan `brevis_ingestion_id` tetap ada:
tanpa key, **isinya adalah key-nya**, sehingga dokumen yang sama dua kali adalah
id yang sama.

**Menyebut field yang tidak ada di `data` adalah error di kedua mode.** "Anda
tidak menyebutkan" dan "Anda menyebut sesuatu yang tidak ada" adalah kesalahan
yang berbeda, dan hanya yang pertama yang lolos:

```json
{"accepted":0,"rejected":["event 0: \"unique_key\" names \"pedido_id\" as this
 record's identity and \"data\".\"pedido_id\" is missing or empty. Drop
 \"unique_key\" to fall back to \"id\", or send the field"]}
```

**`description` dibaca dan hari ini tidak ke mana-mana.** Ia ada di kontrak untuk
halaman ingestion console, yang belum ditulis. Dinyatakan di sini karena field
yang diterima dalam diam adalah field yang dipercaya seseorang sedang disimpan.

### Delapan kolom tetap, ditambah apa pun yang dibawa record

| kolom | | |
|---|---|---|
| `brevis_ingestion_id` | `STRING` | identitas, dan key merge |
| `brevis_record_key` | `STRING` | `data[unique_key]` — **record** mana yang dimaksud; `NULL` di bawah `append` |
| `brevis_operation` | `STRING` | `INSERT`, `UPDATE`, atau `DELETE` |
| `brevis_received_at` | `TIMESTAMP` | kedatangan, pada jam **kami** — kolom partisi |
| `brevis_loaded_at` | `TIMESTAMP` | penulisan, di-stempel oleh **tujuan** |
| `brevis_stream` | `STRING` | rute mana yang menulisnya |
| `brevis_gateway` | `STRING` | deployment mana |
| `brevis_received_bytes` | `INT64` | seberapa besar event **tiba**, envelope termasuk |

Kedelapan ini milik **SDK**, bukan milik sink ini: `sdk.LandingSchema`
mendeklarasikannya dan `sdk.LandingID` mencetak identitasnya, sehingga pipeline
dapat mendaratkan tabel yang sama dengan id yang sama. Satu definisi, karena dua
akan menyimpang. Lihat
[layout landing SDK](/id/docs/sdk/#layout-landing).

**`brevis_` dicadangkan.** `data` yang membawa key dengan prefiks itu ditolak
**per event** — kalau tidak, produser memalsukan field kontrol, dan
`brevis_received_at` palsu lebih buruk daripada tidak ada karena tampak nyata.

**`brevis_loaded_at` adalah `DEFAULT` database**, bukan nilai yang dikirim
gateway. Gateway tahu waktu **dispatch**, tujuan tahu waktu **tulis**, dan selisih
antara kedua kolom adalah latensi end-to-end yang sesungguhnya, per baris, tanpa
instrumentasi apa pun.

**`brevis_received_at` adalah kolom partisi, tidak pernah jam klien**: jam
produser bisa salah atau tidak ada, dan kolom partisi yang dikendalikan klien
berarti klien yang dapat menulis ke tahun 2035. Tabel landing tanpa partisi
adalah tagihan query yang hanya bertambah. Clustering menurut
`brevis_record_key`, karena membaca riwayat satu record adalah yang dilakukan
siapa pun dengan tabel seperti ini.

Satu deklarasi melayani setiap tujuan, karena generator DDL SDK menerjemahkan
setiap tipe: `STRING` menjadi `TEXT` di Postgres, `LONGTEXT` di MySQL, dan
`STRING` di BigQuery; `JSON` menjadi `JSONB`, `JSON`, dan `JSON`.

### Volume tanpa infrastruktur untuk mengukur volume

`brevis_received_bytes` itu **gratis**: gateway sudah menghitung byte ini saat
decode untuk menggerakkan `buffer.flush.size`, dan sampai sekarang angka itu mati
di sana. Mengukur record saja akan berbiaya `json.Marshal` per event — 2,0 µs
terhadap 3,1 µs yang sudah dihabiskan parse, **67% lebih banyak pada jalur panas**
— untuk memperhalus angka yang tugasnya adalah tren dan atribusi.

Ia menjawab apa yang **secara struktural tidak bisa** dijawab metrik. Setiap seri
`brevis_gateway_*` adalah per *stream* dan tidak membawa label `table`, dengan
sengaja: dengan `auto_table` satu rute menjadi N tabel dan label itu dipilih
produser. Jadi "tabel mana yang tumbuh, dan sejak kapan" tidak punya jawaban di
mana pun.

```sql
select date(brevis_received_at) day,
       count(*) events,
       sum(brevis_received_bytes)/1e9 gb
from app_orders
group by 1 order by 1 desc
```

Tanpa kolom, pertanyaan yang sama berarti memindai seluruh kolom JSON —
`sum(length(to_json_string(data)))` — sekitar **lima puluh kali byte yang
dipindai**, setiap kali seseorang bertanya. Kolom itu terbayar pada query
pertama.

**Ini ingress, bukan penyimpanan.** Tujuan menyimpan baris dalam bentuk bertipe
dan terkompresi: 400 byte JSON mungkin 80 di disk. Menjumlahkannya memberi apa
yang *tiba*, tidak pernah apa yang ditagih untuk menyimpannya.

Dan **produser tidak dapat menulisnya**. Gateway menstempel nilainya setelah
hook dan menimpa apa pun yang dibawa envelope — klien yang mengirim field itu
akan melaporkan volumenya sendiri, dan menjumlahkannya tidak lagi bermakna.

### Seri volume bersifat opt-in

```bash
BREVIS_INGESTION_METRICS=true
```

```
brevis_gateway_ingested_bytes_total{stream,table}
brevis_gateway_ingested_events_total{stream,table}
```

**Nonaktif secara default**, dan default itulah intinya: `table` adalah label yang
dipilih *produser*. Segala yang lain di file ini diberi label oleh apa yang
ditulis operator di YAML, sehingga jumlah seri diketahui sebelum proses mulai.
Tidak seorang pun seharusnya menemukan tagihan metrik karena melakukan upgrade.

Nilai yang tidak bisa diparse dibaca sebagai **nonaktif**, tidak pernah sebagai
kegagalan: ini observabilitas, dan typo di sini tidak boleh menjadi alasan
endpoint ingestion tidak mau start.

Kolomnya ada dengan atau tanpa ini. Metrik membeli *sekarang*; kolom membeli
*sejak kapan* dan *oleh siapa*.

### Dua bentuk, satu kontrak

`shape` menentukan apa yang disumbangkan **record** ke baris. Kontrak produser
sama dengan cara apa pun — envelope tidak pernah berubah. Yang berubah adalah
seperti apa tabelnya, yang merupakan keputusan operator dan bukan produser.

```yaml
shape: document   # the default
shape: columns
```

Dengan **`document`**, `data` masuk utuh ke satu kolom `JSON`:

```
 brevis_ingestion_id | brevis_record_key | … | data
 d7179bfe-…          | A-1               |   | {"id": "A-1", "total": 150, "customer": {…}}
```

Tidak pernah ada DDL setelah pembuatan. Field baru adalah key baru: tanpa kuota
perubahan skema, tanpa membuka ulang write stream, tanpa race antar replika — dan
bisa di-query pada hari ia tiba. Di situlah pasar mendarat: Fivetran, Airbyte,
dan Snowpipe semuanya mendarat ke layer mentah yang tidak bisa gagal dan
memodelkannya di hilir.

Dengan **`columns`**, setiap field record mendapat kolomnya sendiri:

```
 id   | total | customer              | items
 A-1  | 150   | {"id": 7, "uf": "SP"} | [{"qty": 2, "sku": "X"}]
```

**Skalar menjadi `STRING`, objek dan array menjadi `JSON`, dan tidak ada inferensi
di mana pun.** `150` tiba sebagai angka dan menjadi string `150`. Itu pilihan,
bukan keterbatasan: field yang tiba bulat hari ini dan pecahan besok akan
mengubah tipe kolom tanpa ada yang menulis apa pun, dan baris yang tidak lagi
muat masuk ke dead letter. Memberi tipe adalah jalur **promosi** — ditulis di
YAML dan direview dalam diff.

**`null` menjadi tidak ada kolom sama sekali.** Ia tidak punya bentuk, jadi tidak
ada yang dibaca: field muncul di tabel dari event pertama yang membawa sebuah
NILAI, dengan bentuk nilai itu. Event yang mengirim `null` mendarat sebagai
`NULL` juga, sesuai maksudnya — dan bila kolom sudah ada, karena Anda
mendeklarasikannya atau event lain membuatnya, `null` ditulis ke dalamnya
alih-alih dilewati.

**Dan kolom `JSON` menerima NILAI JSON, tidak pernah string dari nilai itu.**
Terdengar seperti detail dan itulah perbedaan antara tabel yang dapat di-query
dan tabel yang tampak benar:

```sql
SELECT JSON_TYPE(customer) FROM bronze.orders;     -- object
SELECT JSON_VALUE(customer, '$.uf') ...;           -- SP
```

Jika `JSON_TYPE` menjawab `string` dan `JSON_VALUE` menjawab NULL, objek itu
di-encode dua kali — kolom berisi TEKS objek alih-alih objeknya. Versi gateway
hingga dan termasuk **0.15.0** melakukannya di BigQuery, pada **kedua** bentuk;
lihat [changelog gateway](https://github.com/AreteAcademy/brevis/blob/master/gateway/CHANGELOG.md)
untuk pemeriksaan satu baris dan perbaikannya. Postgres dan MySQL tidak pernah
terpengaruh: server mereka mem-parse teksnya.

Nama field harus sesuai aturan BigQuery, yang tersempit dari keempatnya.
Postgres menerima hampir apa pun yang dikutip, dan itulah jebakannya: tabel
dibuat di sana dan rusak pada hari seseorang mengarahkan stream ke BigQuery.

Kedua bentuk punya padanan SDK, dan itu kode yang sama alih-alih yang
sepadan: `sdk.Landing(table, sdk.LandingKey("id"))` mendaratkan `document`, dan
menambahkan [`sdk.LandingColumns()`](/id/docs/sdk/#dua-bentuk-record-utuh-atau-satu-kolom-per-field)
mendaratkan `columns`. Kolom yang sama, nilai yang sama, `brevis_ingestion_id`
yang sama — sehingga stream dapat berpindah antara gateway dan pipeline tanpa
migrasi, dan keduanya dapat dibaca sebagai satu tabel.

Pipeline dapat menumbuhkan kolom yang sama dengan cara yang sama: atur
[`Evolve: sdk.EvolveAdditiveFromPayload`](/id/docs/sdk/#ketika-muncul-field-yang-tidak-dimiliki-tabel)
pada tujuan dan field yang dibawa batch menjadi kolom, dengan aturan ini, lewat
kode ini.


**Prefiks dapat dikonfigurasi, dan ia satu keputusan untuk seluruh proses.**
`BREVIS_LANDING_PREFIX=acme` membuatnya `acme_ingestion_id` dan seterusnya —
setiap stream di gateway ini, sehingga dua sink tidak bisa berselisih tentang
tabel yang mungkin sama-sama mereka tulis. Aturan reserved di atas ikut
berpindah: di bawah `acme_`, produser yang mengirim `acme_region` ditolak dan
`brevis_region` adalah field biasa.

**Pilih sebelum tabel pertama dibuat.** Mengubahnya kemudian menambah delapan
kolom dan meninggalkan delapan — tidak ada yang menghapus kolom — sehingga tabel
yang sudah membawa layout dengan satu prefiks menolak load yang mendeklarasikan
yang lain. Lihat [halaman SDK](/id/docs/sdk/#prefiks-brevis-dapat-anda-ubah)
untuk normalisasi dan jalan keluarnya.

**Field objek bersarang dapat mendapat kolomnya sendiri.**
`BREVIS_NORMALIZE_DATA=true` pada proses gateway meratakan satu level —
`{"name": {"first": "x"}}` mendarat sebagai `name_first` — dan setiap stream
berbagi nilai itu, sehingga dua sink tidak bisa berselisih tentang tabel yang
mungkin sama-sama mereka tulis. Lihat
[halaman SDK](/id/docs/sdk/#satu-kolom-per-field-bersarang) untuk apa yang
di-rename-nya, yaitu bagian yang harus dibaca sebelum mengaktifkannya.

**Di BigQuery, `nationalID` dan `nationalId` adalah satu kolom.** BigQuery
melipat huruf besar-kecil kolom, sehingga flush yang membawa satu ejaan di
`DELETE` dan ejaan lain di `INSERT` membuat **satu** kolom — yang pertama dalam
urutan sortir — alih-alih gagal pada `CREATE`. Satu event yang membawa keduanya
ditolak, dengan menyebut kedua field. Postgres dan MySQL mempertahankannya sebagai
dua kolom, yang benar bagi mereka. Lihat [halaman SDK](/id/docs/sdk/index.md) untuk ejaan
mana yang menang dan apa yang tidak dimigrasikan.

### `UPDATE` dan `DELETE` dicatat, tidak diterapkan

`brevis_operation` adalah sebuah kolom. Tabel landing adalah **riwayat**:
`UPDATE` yang diterapkan di tempat kehilangan versi sebelumnya, dan hari ketika
Anda menginginkan versi itu adalah hari sesuatu rusak. Menyelesaikan "versi
terkini dari setiap record" adalah tugas model di hilir:

```sql
qualify row_number() over (partition by brevis_record_key
                           order by brevis_received_at desc) = 1
   and brevis_operation <> 'DELETE'
```

Itulah yang dilakukan Debezium, Fivetran, dan Airbyte — dan itu berarti CDC
berbiaya **nol** pada jalur tulis: tanpa mode upsert, tanpa lock, tanpa delete.

### Identitas tidak memiliki jam di dalamnya

```
brevis_ingestion_id = uuid5(auto_table | table | data[unique_key] | sha256(canonical(data)))
```

Tidak ada `occurred_at` dalam rumus, dan itu disengaja: kedatangan menjadi
tanggung jawab **kami**, dan jam kami adalah `time.Now()` — berbeda pada setiap
pengiriman. Identitas dengan jam di dalamnya akan membuat setiap pengiriman ulang
menjadi event baru, dan `merge` berhenti menyerap apa pun.

```
same record, same content, twice  →  same id, merge absorbs
same record, content changed      →  a different id, both versions land
```

Catatannya sudah terdokumentasi: dua event yang benar-benar berbeda dengan `data`
yang identik byte demi byte menyatu menjadi satu. Untuk CDC itu **benar** — dua
update identik pada satu record dengan nilai yang sama adalah fakta yang sama.

Sidik jari dihitung atas dokumen dalam bentuk **kanonik** — key diurutkan pada
setiap level, array dibiarkan berurutan, semuanya dikutip. Go mengacak iterasi
map, sehingga hash naif akan berbeda antara dua pengiriman event yang sama, satu
hal yang justru ingin dicegahnya.

### Tabel menumbuhkan kolom sendiri

`data` yang membawa field yang tidak dimiliki tabel membuat kolom itu muncul.
**Aditif, dan hanya aditif**: tidak ada yang dihapus, tidak ada yang dipersempit.

Di BigQuery ini baru benar sejak **0.13.0**. Sebelumnya, `auto_table`
mendeklarasikan `Evolve: additive` pada setiap tujuan dan hanya driver Postgres
dan MySQL yang membaca deklarasi itu — sehingga di sana bentuknya beku sejak
pembuatan dan tidak ada yang menyatakannya.

> **Bila berasal dari 0.11.0 atau 0.12.0**, tabel yang dibuat sebelum `0.11.0`
> membutuhkan migrasi sekali jalan, karena kolom tetap kedelapan masuk ke
> deklarasi dan bukan ke tabel:
>
> ```sql
> ALTER TABLE `<dataset>.<table>` ADD COLUMN IF NOT EXISTS brevis_received_bytes INT64
> ```
>
> Postgres dan MySQL tidak pernah terpengaruh.

Batch yang kalah balapan dengan `ALTER` gagal, dan retry biasa pada pipe
menyelesaikannya — batch yang menunggu DDL dan batch yang menunggu worker adalah
hal yang sama, jadi tidak ada buffer kedua. Lintas replika, metastore memegang
*debounce* satu detik per tabel dan per bentuk, sehingga sepuluh replika yang
melihat field baru yang sama tidak menjadi sepuluh `ALTER` terhadap kuota BigQuery
sebesar lima operasi metadata per tabel per sepuluh detik.

**Debounce dan bukan lock**: tidak ada yang dilepaskan dan tidak ada lease yang
diperpanjang. Yang kalah di-*retry*, bukan diblokir, dan replika yang mati
sambil memegangnya hanya berbiaya satu detik bagi yang lain. Begitulah yang
dilakukan pasar — Delta Lake dan Iceberg melakukan commit secara optimistis lalu
retry, Kafka Connect mendapat serialisasi dari urutan partisi, Fivetran menjaga
satu writer per tabel.

### Nama adalah permukaan serangan

`auto_table` mengubah string dalam payload menjadi DDL, sehingga setiap field di
bawah `naming` adalah penolakan:

- **`pattern`** default ke `^[a-z][a-z0-9_]{2,48}$` — irisan dari apa yang
  diterima Postgres, MySQL, BigQuery, dan Redshift tanpa kutip. Nama yang lolos
  tidak membutuhkan kutip di mana pun dan tidak dapat membawa injeksi.
- **`allow`** mempersempit lebih jauh, menurut prefiks.
- **`max_new_per_hour`** (default 20) membatasi **pembuatan** dalam satu jam
  bergulir, tidak pernah penulisan: tabel yang sudah ada tidak pernah dibatasi
  lajunya. Di bawah `metastore: memory` anggarannya hidup di proses dan empat
  replika mengizinkan empat kali ini; di bawah `redis` atau `memcached` mereka
  berbagi satu counter.

Nama di luar aturan ditolak **per event**, dalam respons, dan setiap event yang
bentuknya benar dalam request yang sama tetap mendarat:

```json
{"accepted": 0, "rejected": ["event 0: the table name \"orders\" does not match ^(app|svc)_[a-z0-9_]{2,40}$"]}
```

Per event dan bukan per batch, dengan sengaja. Menolaknya saat tulis akan
menggagalkan seluruh batch — satu event cacat dari satu produser mengubur event
setiap produser lain dalam jendela flush yang sama, tanpa satu pun diberi tahu.

**`auto_table` ditolak pada endpoint tanpa `listen.auth`**, termasuk di bawah
`BREVIS_ENV=local`. Produser yang bisa menyebut tabel bisa membuatnya, sehingga ia
membutuhkan autentikasi bahkan di tempat stream biasa tidak. NetworkPolicy tidak
mencakupnya: itu membatasi siapa yang menjangkau port, bukan nama tabel apa yang
mereka minta.

### Metastore adalah cache

Ia ada agar penulisan per event tidak menjadi pencarian per event, dan ia
menyimpan tiga hal: tabel mana yang ada, *debounce* DDL, dan counter di balik
`max_new_per_hour`.

Ia adalah cache dan **bukan** sumber kebenaran. Tujuan yang menentukan apakah
tabel ada, dan *N* replika yang berlomba membuatnya adalah kasus normal:
`AlreadyExists` adalah sukses, dan ini hanya mengurangi balapan. TTL adalah
berapa lama ia boleh **salah**.

| backend | kapan |
|---|---|
| `memory` | default, tidak membutuhkan apa pun. Dengan **satu** replika itu jawaban yang tepat |
| `redis` | `addr_from` wajib. `SETNX` adalah klaim, `INCR` adalah counter |
| `memcached` | `addr_from` wajib. `Add` adalah klaim, dan kedaluwarsa berbutir **detik** |

**Redis lebih cepat untuk stream dengan routing key**, dan perbedaannya layak
diberi angka alih-alih kata sifat. Dengan trigger per tabel, jendela meminta satu
klaim per tabel: Redis menjawab 94 di antaranya dalam satu round trip pipeline,
dan setiap backend lain menjawabnya paralel, dibatasi 32. Diukur terhadap backend
25 ms jauhnya — 94 klaim:

| backend | satu per satu | sebagaimana dikirim |
|---|---:|---:|
| `redis` | 2,589 d | **29,8 ms** |
| `memcached` | 2,577 d | **89,4 ms** |

Tiga gelombang melawan satu round trip. Keduanya berada di dalam 500 ms yang
diberikan untuk satu klaim, sehingga memcached adalah pilihan yang lebih lambat
dan bukan yang ditolak.

Dengan beberapa replika, `memory` memberi masing-masing miliknya sendiri:
debounce tidak men-debounce apa pun dan `max_new_per_hour` membatasi satu proses.
Itulah satu-satunya alasan dua lainnya ada.

**Metastore yang mati tidak boleh menjatuhkan penulisan bersamanya.** `Get` yang
error adalah miss, klaim yang error berperilaku sebagai menang, counter yang error
melonggarkan batas. Cache yang bisa menghentikan ingestion lebih buruk daripada
tanpa cache.

**Dan "mati" mencakup "lambat".** Klaim berjalan selagi lock buffer dipegang,
sehingga backend yang butuh dua detik untuk menjawab adalah dua detik ketika
stream itu tidak menerima apa-apa. Setiap panggilan dibatasi **500 ms** dan gagal
terbuka setelahnya. Redis mematuhinya sebagai context pemanggil; `gomemcache`
tidak menerima context sama sekali, sehingga memcached mematuhinya sebagai
deadline socket — batas yang sama dicapai dengan dua cara, karena salah satu dari
kedua klien tidak bisa disuruh berhenti.

**`ttl: 0` berarti tidak pernah**, dan tidak menyebut apa pun tetap memakai
default satu menit — tiga state, bukan dua.

Jam **bukan** yang memulihkan dari entri yang salah. Penulisan yang ditolak
tujuan memanggil `invalidate`, yang menjatuhkan entri seketika dan membiarkan
retry pipe sendiri kembali terhadap cache yang dingin: tabel yang di-drop manual
berbiaya **satu percobaan gagal**, bukan satu menit.

Kedaluwarsa punya harga. `sinkFor` menagih `naming.max_new_per_hour` ketika ia
tidak **mengetahui** sebuah tabel — sehingga miss menagih untuk tabel yang sudah
ada berminggu-minggu, dan pod yang restart semenit setelah event terakhir sebuah
tabel tidak mendapat apa-apa dari backend bersama yang dibayarnya. Dengan tabel
berumur panjang, `ttl: 0` adalah jawabannya.

> Pada `0.12.0` dan `0.13.1` ejaan itu **tidak bisa diparse** — YAML membaca nol
> polos sebagai int, dan `0.13.2` adalah yang pertama menerima keduanya. Pada
> versi tersebut, tulis `ttl: 0s`.

`addr_from` menyebut **variabel environment** yang menyimpan alamat, bukan
alamatnya: ia cukup sering membawa password.

### BigQuery punya lantai flush

**BigQuery mengizinkan 1.500 job load per tabel per hari.** Jendela satu detik
default adalah 86.400 — 57× kuota, habis dalam sekitar dua puluh lima menit. Itu
bukan masalah untuk Pub/Sub, dan angkanya akan tiba di sini *dari* config
Pub/Sub.

Maka stream yang menulis ke BigQuery, langsung atau lewat router, **ditolak saat
load** dengan jendela di bawah 60d. Jawaban sesungguhnya adalah Storage Write
API, yang belum ditulis; sampai ada, config menolak jendela yang tidak dapat
dipenuhinya alih-alih membiarkan deploy pertama menemukannya.

`auto_table` tidak dapat merutekan ke **Redshift**: driver itu tidak membuat
tabel, sehingga setiap nama baru akan gagal saat load. Ditolak dengan menyebut
namanya, saat startup.

## Yang belum ada

`sqlite`, `dynamodb`, `kinesis`, `sqs`, `sns`, `kafka`, dan `rabbitmq`
direncanakan dan belum ada yang ditulis. (`redis` dan `memcached` ada sebagai
**metastore**, yang merupakan hal berbeda: cache dari apa yang diketahui tentang
tabel, tidak pernah tujuan.) Tabel di bagian atas halaman ini menyatakan state
masing-masing, dan diperbarui ketika state berubah — bukan sebelumnya.
