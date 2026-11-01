# Go SDK

> Menulis fetcher — mengekstrak dari sumber, mentransformasi, dan memuat ke tujuan.

*https://brevis.sh/id/docs/sdk/ · brevis.sh docs (id)*

---

Ketika tugas sebuah step adalah **mengambil data dan memuatnya ke suatu tempat**,
Go SDK memberi Anda seluruh fetcher dalam beberapa baris. Ia adalah modul
terpisah, diberi versi secara independen dari engine.

```bash
go get github.com/AreteAcademy/brevis/sdk@latest
```

Membutuhkan Go 1.23 atau lebih baru — SDK men-stream baris sebagai `iter.Seq2`.

:::warning Jangan gunakan `v0.1.0`
Versi itu dikirim dengan `go.mod` yang menyematkan revisi yang tidak ada, dan
proxy modul Go bersifat immutable. Mulailah dari `v0.1.1`.
:::

## Tiga langkah

```go
import (
	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/to/bigquery"
)

rows, err := sdk.Extract(ctx, sdk.Source{
	From: from.HTTP{URL: "https://api.example.com/v1/events"},
})

rows = sdk.Transform(rows, sdk.Accept("id", "created_at", "amount"))

res, err := sdk.Load(ctx, rows, sdk.Target{
	To:      bigquery.Table{Dataset: "bronze", Name: "events"},
	Columns: []string{"id", "created_at", "amount"},
})
```

`Extract` membaca, `Transform` membentuk ulang, `Load` menulis. Masing-masing
menerima dan mengembalikan sebuah sequence — tidak ada yang dimaterialisasi
sekaligus di memori.

![Alur SDK: sdk.Extract membaca dari API HTTP dengan from.HTTP, sdk.Transform mempertahankan field yang diterima, dan sdk.Load menulis baris ke tabel BigQuery. Ketiga pemanggilan berjalan di dalam step Anda sendiri.](/assets/flow-sdk.svg)

## Driver adalah nilai, bukan pengaturan

`from.HTTP` membawa semua yang dibutuhkan sumber HTTP: URL, header, retry,
pagination, dan arti sebuah respons. `from.Files` membawa path dan format.
Keduanya tidak perlu memberi ruang bagi field yang lain, sehingga tidak ada
struct source yang mengumpulkan empat puluh opsi yang masing-masing driver hanya
membaca enam.

Ia juga menentukan **apa yang Anda kompilasi**. Go memangkas dependensi per
paket yang diimpor, tidak pernah per field yang dipakai:

| yang Anda impor | paket | AWS | Google |
|---|---|---|---|
| `sdk` | 190 | tidak | tidak |
| `sdk` + `from` | 194 | tidak | tidak |
| `sdk` + `from` + `to` (file) | 195 | tidak | tidak |
| `sdk` + `to/bigquery` | 456 | tidak | **ya** |
| `sdk` + `from` + `store/s3` | 265 | **ya** | tidak |

Seluruh pipeline file — baca dan tulis — berbiaya 195 paket dan tanpa SDK cloud
sama sekali. **Driver yang di belakangnya ada SDK vendor berada di paketnya
sendiri**, itulah sebabnya BigQuery adalah `to/bigquery` dan object store adalah
`store/s3` dan `store/gcs`.

## Sumber dan tujuan

| sumber | paket |
|---|---|
| HTTP | `from.HTTP` |
| file (lokal, S3, GCS) | `from.Files` |
| Postgres | `from/postgres` |
| MySQL | `from/mysql` |
| beberapa sumber | `from.Many` |

| tujuan | paket |
|---|---|
| file | `to.Files` |
| BigQuery | `to/bigquery` |
| Postgres | `to/postgres` |
| MySQL | `to/mysql` |
| Redshift | `to/redshift` |
| Pub/Sub | `to/pubsub` |

Skema path menentukan backend, dan `Store` **diteruskan dari luar** alih-alih
dipilih di dalam driver:

```go
from.Files{Path: "s3://bucket/day=1/*.ndjson", Store: s3.New(client)}
to.Files{Path: "gs://bucket/landing/", Store: gcs.New(client)}
```

Itulah yang menjaga program file lokal dari mengompilasi satu baris pun kode AWS
atau Google.

### Memublikasikan ke topik

`to/pubsub` adalah satu-satunya tujuan yang bukan tabel, dan perilakunya berbeda
dengan cara yang layak diketahui sebelum Anda memakainya: **ia tidak menambahkan
apa pun pada yang dikirimnya.**

```go
Target: sdk.Target{
    To: pubsub.Topic{
        Project: "acme-prod",
        Name:    "orders",

        // Nil is the default, and it means NO attributes at all.
        Attributes: func(e sdk.Envelope) map[string]string {
            row, ok := e.Payload.(map[string]any)
            if !ok {
                return nil
            }
            return map[string]string{"orderId": fmt.Sprint(row["order_id"])}
        },
    },
},
```

Subscriber menerima payload Anda, byte demi byte, ditambah persis atribut yang
Anda sebutkan. Tidak ada yang lain — tanpa `ingestion_id`, tanpa `provider`,
tanpa envelope jenis apa pun.

Itu adalah aturan, bukan minimalisme. Tabel dibuat oleh pipeline yang
menulisnya, sehingga SDK boleh menentukan kolomnya. **Topik tidak demikian**: ia
ada sebelum pipeline Anda ada, subscriber-nya ditulis lebih dulu dan filternya
ditulis lebih dulu. Atribut yang ditambahkan sendiri berarti Brevis mengedit
kontrak orang lain, dan subscriber baru akan mengetahuinya pada pukul tiga pagi.

Tiga hal mengikuti dari sini:

- **Ia tidak pernah membuat topik.** Pipeline yang bisa membuatnya bisa membuat
  yang salah, dan tidak seperti tabel yang salah ketik, tidak ada yang
  menyadarinya — pesan pergi ke suatu tempat dan subscriber yang seharusnya
  menerimanya tetap diam.
- **`Schema`, `Dedup`, dan `PartitionBy` ditolak**, dengan menyebut opsinya.
  Semuanya ada untuk tabel: `Schema` adalah cara tujuan membuatnya, deduplikasi
  membutuhkan baris untuk diganti, partisi adalah gagasan tabel. Yang terdekat di
  sini adalah `OrderingKey`, yang berbeda — ia mengelompokkan pesan yang harus
  *tiba* berurutan.
- **Kegagalan bersifat parsial.** Publikasi tidak transaksional, sehingga 48.000
  pesan yang gagal pada 31.000 telah *mengirimkan* 31.000. Hasilnya melaporkan
  apa yang benar-benar terkirim, dan error menyatakan bahwa menjalankan ulang
  berarti pengiriman ulang — subscriber harus idempoten, dan Pub/Sub memang
  at-least-once.

Dapat dijalankan, terhadap emulator:
[`examples/13-pubsub`](https://github.com/AreteAcademy/brevis/tree/master/examples/13-pubsub).

## Fetcher utuh

`sdk.Run` mengurus flag, `-dry-run`, logging, retry, pagination, provenance,
pembuatan tabel, dan exit code. Yang tersisa di file hanyalah yang spesifik
untuk sumber itu:

```go
package main

import (
	"time"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
)

func main() {
	sdk.Run(sdk.Pipeline{
		Source: sdk.Source{
			From: from.HTTP{
				URL:     "https://api.example.com/v1/events",
				Timeout: 15 * time.Second,
			},
			Guard:  sdk.RejectIf("error"),
			Expand: sdk.ArrayAt("results"),
		},
		Target: sdk.Target{
			Provider: "example",
			Entity:   "events",
			Key:      sdk.Key("id"),
			When:     sdk.Field("created_at"),
		},
	})
}
```

```bash
go run ./fetcher -dry-run   # extracts, counts rows and errors, writes nothing
go run ./fetcher
```

## Context run

Ketika fetcher berjalan **sebagai step workflow**, engine menyuntikkan ke
environment apa yang tidak dapat diketahuinya dengan cara lain: apakah ini run
pertama, dengan parameter apa ia dikirim, run yang mana ini. SDK membacanya di
`Pipeline.Run`:

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
	if p.Run.Params["load_full"] == "true" {
		p.Source.From = from.HTTP{URL: base + "?full=1"}
	}
	return nil
},
```

Dijalankan secara manual, `Run` kembali bernilai nol — membacanya opsional, dan
mengabaikannya tidak berbiaya apa pun.

Tanpa riwayat, jawaban atas "apakah ini run pertama?" selalu **tidak**: membuat
tabel tanpa kepastian lebih buruk daripada tidak membuatnya.

### Jam

`p.Run.Auto` adalah apa yang dihitung engine tentang run ini — [auto
params](/id/docs/parameters/#auto-params). `Auto.Now()` adalah jam yang dibaca
sebagai pengganti `time.Now()`: pada run terjadwal ia adalah slot-nya, sehingga
tidak bergeser ketika run terlambat dan tidak bergeser ketika run di-retry tiga
jam kemudian.

```go
Before: func(ctx context.Context, p *sdk.Pipeline) error {
	start, end, ok := p.Run.Auto.Window()
	if !ok { // no schedule: fall back to a fixed window
		start, end = p.Run.Auto.Now().Add(-24*time.Hour), p.Run.Auto.Now()
	}
	p.Source.From = from.HTTP{URL: base +
		"?from=" + start.Format(time.RFC3339) +
		"&to=" + end.Format(time.RFC3339)}
	return nil
},
```

Dijalankan secara manual, `Auto.Now()` *adalah* jam dinding dan `Window()`
mengembalikan `false`, sehingga pengembangan lokal tidak membutuhkan kasus
khusus.

## Jangan dikacaukan dengan pustaka klien

SDK ini adalah mesin ETL dalam Go: driver, pagination, provenance, pembuatan
tabel. [Pustaka klien](/id/docs/libraries/index.md) adalah hal yang berbeda — klien tipis
yang menyerahkan context run dan jam kepada step, di Python saat ini dan di
Node.js dan Rust nanti. Step yang memakai pandas atau dbt menginginkan yang
kedua, bukan ini.

## Parameter yang membangun pipeline

`p.Run.Params` dapat dibaca di dalam pipeline, yang terlambat untuk nilai yang
menentukan pipeline itu **apa** — satu sumber per negara bagian, satu tabel per
tenant. `Pipeline.Flags` diparse di dalam `Run`, sehingga terlambat dengan alasan
yang sama.

```go
func main() {
	sdk.Run(pipeline(sdk.ParamList("ufs")))
}
```

`sdk.Param` dan `sdk.ParamList` membaca environment engine terlebih dahulu dan
`-param name=value` kedua:

```bash
./fetch-stations -param ufs=SP,RJ
```

Environment menang — di bawah engine ia adalah nilainya, dan flag yang tertinggal
di manifest tidak boleh menimpa apa yang diketik operator. Tanpa engine, flag
adalah yang ada, yang lebih baik daripada menulis `BREVIS_RUN_PARAMS` sebagai
JSON pada setiap run.

## Layout landing

Sebuah pipeline dapat mendaratkan **tabel yang sama dengan yang didaratkan
gateway** — kolom yang sama dan `brevis_ingestion_id` yang sama — sehingga
memilih di antara keduanya adalah keputusan deployment dan bukan keputusan
skema. Tim dapat membaca keduanya sekaligus, dan berpindah dari satu ke yang
lain tanpa migrasi.

```go
const table = "landing.orders"

sdk.Run(sdk.Pipeline{
    Source:    sdk.Source{From: from.HTTP(/* ... */)},
    Transform: []sdk.Transformer{sdk.Landing(table, sdk.LandingKey("id"))},
    Target: sdk.Target{
        To:     postgres.Table{DSN: dsn, Name: table, CreateTable: true},
        Schema: sdk.LandingSchema(sdk.LandingOptions{}),
    },
})
```

`sdk.LandingSchema` terdiri dari sembilan kolom: delapan yang dimiliki layout dan
satu milik produser. `sdk.Landing` mengisinya dan menaruh record, utuh, di
`data`. Tambahkan kolom Anda sendiri ke skema jika mau, atau ambil
`sdk.LandingControlColumns` dan beri field record kolomnya masing-masing.

### Dua bentuk: record utuh, atau satu kolom per field

Yang di atas adalah bentuk **document** — record masuk ke `data` sebagai JSON,
dan tabelnya sama apa pun isi record. Tambahkan `sdk.LandingColumns()` dan setiap
field record mendapat kolomnya sendiri:

```go
sdk.Landing(table, sdk.LandingKey("source_key"), sdk.LandingColumns())
```

Tabel itu kemudian adalah `sdk.LandingControlColumns` ditambah kolom Anda —
**bukan** `sdk.LandingSchema`, yang membawa `data`:

```go
Target{
    To: postgres.Table{DSN: dsn, Name: table, CreateTable: true},
    Schema: append(sdk.LandingControlColumns(sdk.LandingOptions{Keyed: true}),
        sdk.Column{Name: "source_key", Type: sdk.TypeString},
        sdk.Column{Name: "series", Type: sdk.TypeString},
        sdk.Column{Name: "data", Type: sdk.TypeString},
        sdk.Column{Name: "valor", Type: sdk.TypeString},
    ),
}
```

**Anda yang mendeklarasikan kolom-kolom itu.** SDK tidak pernah menyimpulkan
skema, dan opsi ini tidak mengubahnya: ia menentukan bagaimana *baris* dibangun,
tidak pernah seperti apa tabelnya. Gateway menyimpulkan karena produsernya adalah
orang asing yang mengirim apa pun yang mereka punya; di sini penulis pipeline
tahu bentuknya dan menuliskannya, dalam diff yang direview seseorang.

Ini bentuk yang sama dengan yang didaratkan gateway sebagai
[`shape: columns`](/id/docs/ingestion-sinks/#dua-bentuk-satu-kontrak), lewat kode
yang sama — kolom yang sama, nilai yang sama, `brevis_ingestion_id` yang sama.

**Nama field harus bisa menjadi nama kolom**: huruf atau underscore, lalu huruf,
angka, dan underscore. Itu aturan BigQuery, yang tersempit dari keempat tujuan,
sehingga nama yang lolos di sini berfungsi di mana saja. Record yang membawa
`my-field` ditolak dengan menyebut namanya, sebelum apa pun ditulis. Bentuk
document tidak punya aturan itu — tanda hubung adalah key JSON yang sah.

**Secara default ia tidak meratakan.** Objek bersarang menjadi SATU kolom `JSON`
di bawah key yang memegangnya: `customer` berisi `{"id": 7, "uf": "SP"}`, bukan
`customer_id` dan `customer_uf`. Array juga `JSON`. Record yang menginginkan satu
baris *per elemen array* membutuhkan `sdk.ArrayAt` di `Expand` milik source — ia
berjalan sebelum `Landing`, dan merupakan operasi lain dengan nama lain.
[`BREVIS_NORMALIZE_DATA`](#satu-kolom-per-field-bersarang) mengubah paruh pertama
itu untuk seluruh deployment; array tetap `JSON` dengan cara apa pun:

```go
Source: sdk.Source{From: from.HTTP(/* ... */), Expand: sdk.ArrayAt("results")},
```

**`null` tetap `NULL`**, tidak pernah `""`. Field yang dikirim null dan field
yang dikirim kosong adalah fakta yang berbeda, dan kolom tidak dapat
membedakannya sesudahnya.

**`data` menjadi milik Anda.** Dalam bentuk ini layout tidak punya kolom dengan
nama itu, sehingga produser dengan field bernama `data` — persis yang dikirim
seri Bacen — mendapat kolom miliknya sendiri dengan nilainya sendiri. Di bawah
`document`, miliknya adalah satu key di dalam JSON layout.

### Prefiks `brevis_` dapat Anda ubah

```
BREVIS_LANDING_PREFIX=acme
```

dan delapan kolom layout menjadi `acme_ingestion_id`, `acme_record_key`, dan
seterusnya. Bila tidak diatur, ia tetap seperti yang selalu ada.

**Pilih saat tabel dibuat, karena mengubahnya kemudian bukanlah rename.** Tidak
ada apa pun di SDK ini yang pernah menghapus kolom, sehingga stream yang berganti
prefiks tidak me-rename delapan — ia menambah delapan dan meninggalkan delapan.
Setiap baris sesudahnya memiliki NULL pada set lama, dan

```sql
qualify row_number() over (partition by brevis_record_key
                           order by brevis_received_at desc) = 1
```

mempartisi pada kolom yang tidak lagi ditulis siapa pun. Baris terus masuk,
dashboard mendatar, dan tidak ada yang mencatat apa-apa.

Maka tabel yang sudah membawa layout dengan satu prefiks **menolak** load yang
mendeklarasikan yang lain, sebelum extract, dengan menyebut keduanya. Arahkan ke
tabel baru, atau ganti nama delapan kolom itu sendiri terlebih dahulu.

**Apa pun yang Anda ketik dinormalisasi**, sehingga hanya ada satu ejaan yang
bisa salah, bukan empat:

| Anda mengatur | Anda mendapat |
|---|---|
| tidak ada, atau kosong | `brevis_` |
| `_NAME` | `name_` |
| `_NAME_` | `name_` |
| `NAME` | `name_` |

Pangkas `_` dari kedua ujung, ubah ke huruf kecil, tambahkan tepat satu. Dua
nilai ditolak alih-alih ditebak: hanya underscore tanpa yang lain, karena itu
akan menyisakan `ingestion_id` — persis kolom yang ditulis
`sdk.IngestionID()` untuk setiap pipeline biasa — dan apa pun yang tidak dapat
memulai nama kolom. **Proses dengan nilai yang tidak dapat dipakai tidak
mulai**, satu-satunya hasil yang jujur: alternatifnya adalah tabel yang
dinamai menurut prefiks yang sedang Anda gantikan.

**Ia berlaku per PROSES, dan itu intinya, bukan keterbatasan.** Setiap stream
dalam satu gateway berbagi nilai itu, sehingga dua sink tidak bisa berselisih —
begitu pula skema yang mendeklarasikan tabel dan transformer yang mengisinya.

**Aturan reserved ikut berpindah.** Di bawah `acme_`, produser yang mengirim
`acme_region` ditolak, karena orang yang bisa memalsukan kolom kontrol akan
memalsukan yang tampak asli. Dan `brevis_region` menjadi field biasa.

`data` tidak pernah membawa prefiks. Itu kolom milik produser, bukan milik
layout.

### Satu kolom per field bersarang

```
BREVIS_NORMALIZE_DATA=true
```

dan field dari objek bersarang mendapat kolomnya sendiri, seperti
`json_normalize` milik pandas — satu level:

```python
data = [
    {"id": 1, "name": {"first": "Coleen", "last": "Volk"}},
    {"name": {"given": "Mark", "family": "Regner"}},
    {"id": 2, "name": "Faye Raker"},
]
```

```
id  name_first  name_last  name_given  name_family  name
```

**Pilih sebelum tabel pertama, karena ia tidak hanya MENAMBAH kolom.** Ia
mengganti nama kolom, dan tidak ada apa pun di SDK ini yang pernah menghapus
satu pun — sehingga nama lama tetap bersama baris yang sudah ada sementara semua
yang sesudahnya mendarat di nama baru, dan query yang ditulis seseorang terhadap
nama lama terus berjalan dan berhenti melihat baris baru. Dua penggantian nama
terjadi:

- `userName` menjadi `username`, entah ada yang bersarang atau tidak
- `name`, yang berisi objek, digantikan oleh `name_first` dan `name_last`

Tabel yang sudah membawa salah satu bentuk **menolak** load dengan opsi ini
aktif, sebelum extract, dengan menyebut kedua kolom. Arahkan ke tabel baru, atau
ganti nama kolomnya sendiri terlebih dahulu.

**`name` dapat muncul sekaligus rata dan utuh**, dan contoh di atas
menunjukkannya: record ketiga membawa `name` sebagai string, sehingga tabel
memiliki `name_first`, `name_last` DAN `name`. Field yang berupa objek di satu
record dan skalar di record lain menghasilkan keduanya.

**Satu level, dan array adalah nilai.** `{"a": {"b": {"c": 1}}}` adalah `a_b`
yang berisi `{"c": 1}` — kolom `JSON`, bukan `a_b_c`. `{"a": [1, 2]}` adalah `a`,
juga `JSON`. Record yang menginginkan satu baris per elemen array tetap
membutuhkan `sdk.ArrayAt`.

**Digabung dan diubah ke huruf kecil, tidak dipecah pada camelCase.** `userName`
menjadi `username` dan bukan `user_name`, karena pembacaan lain tidak punya
jawaban yang disepakati untuk akronim — `HTTPStatus`, `userID` — dan setiap
pustaka berselisih tentang hal itu.

**Tabrakan ditolak, tidak pernah diselesaikan.** `{"name_first": 1, "name":
{"first": 2}}` akan membuat satu kolom dari dua field, dan iterasi map di Go
diacak: membiarkan salah satunya menang akan memberi batch yang sama tabel yang
berbeda pada run yang berbeda. Penolakan menyebut keduanya.

**Prefiks reserved diperiksa pada kolom, bukan field.** Produser yang mengirim
`{"brevis": {"stream": "x"}}` akan membuat `brevis_stream` lewat pintu yang tidak
dijaga pemeriksaan field. Itu ditolak, dengan menyebut `brevis.stream` agar
mereka dapat menemukan field-nya.

**Ia berlaku per PROSES**, seperti prefiks, dan dengan alasan yang sama: setiap
stream dalam satu gateway berbagi nilai itu, sehingga dua sink tidak bisa
berselisih tentang tabel yang mungkin sama-sama mereka tulis. Ia hanya berlaku
untuk bentuk `columns` — di bawah `document` record masuk utuh ke satu kolom
`JSON` dan tidak ada yang bisa diratakan.

### Teks yang sudah berupa JSON

Kolom yang dideklarasikan `sdk.TypeJSON` menerima **nilai** JSON — objek, array,
angka. Di BigQuery baris di-encode utuh, sehingga `string` Go di sana menjadi
literal string JSON dan kolom akhirnya berisi TEKS objek Anda alih-alih objeknya:

```sql
SELECT JSON_TYPE(payload) ...;            -- "string", not "object"
SELECT JSON_VALUE(payload, '$.a') ...;    -- NULL
```

Kolomnya bertipe benar dan data di dalamnya tidak terjangkau, yang terburuk dari
ketiga hasil karena tidak ada yang gagal. **Tujuan BigQuery menolaknya** sejak
`v0.74.0`, dengan menyebut kolomnya.

Berikan objeknya sendiri bila Anda memilikinya. Bila yang Anda pegang sudah
berupa teks JSON — kolom yang dibaca dari database lain, body respons yang tidak
ingin Anda parse ulang — nyatakan demikian:

```go
row["payload"] = sdk.JSONText(body)
```

`json.RawMessage` dari `encoding/json` juga berfungsi, dengan alasan yang sama:
keduanya menulis JSON yang mereka pegang alih-alih string berkutip darinya.

**Postgres dan MySQL tidak terpengaruh dan tidak ditolak.** Server mereka
mem-parse string menjadi kolom JSON, dan selalu begitu.

### Ketika muncul field yang tidak dimiliki tabel

Produser menambahkan field dan load berhenti, menyebut namanya: baris membawa
kolom yang tidak dideklarasikan siapa pun, dan menulisnya ke tujuan yang tidak
pernah menyebutnya akan membuangnya secara diam-diam. Penolakan itu benar secara
default — tetapi pada tabel landing itu adalah satu-satunya perubahan yang tidak
dapat merusak pembaca, dan tujuan dapat melakukannya:

```go
postgres.Table{DSN: dsn, Name: table, Evolve: sdk.EvolveAdditiveFromPayload}
```

Batch kemudian **melengkapi** deklarasi Anda: field yang dibawanya dan tidak
dideklarasikan menjadi kolom, `STRING` kecuali field itu objek atau array, lalu
`JSON`. Itu aturan yang sama dan kode yang sama yang dipakai gateway mendaratkan
`shape: columns`, sehingga keduanya menghasilkan tabel yang sama.

Dua nilai lain, dan perbedaan di antaranya adalah dari mana kolom berasal:

| `Evolve` | menambahkan |
|---|---|
| `sdk.EvolveNone` (default) | tidak ada. Setiap perbedaan ditolak, dengan menyebut kedua sisi |
| `sdk.EvolveAdditive` | kolom yang dimiliki **deklarasi** Anda dan tidak dimiliki tabel |
| `sdk.EvolveAdditiveFromPayload` | itu, ditambah kolom yang dibawa **batch** |

**Hanya `ADD COLUMN`.** Kolom yang menghilang dari sumber berhenti ditulis dan
tetap di tabel; tidak ada yang di-rename, tidak ada yang dihapus, dan tidak ada
tipe yang pernah dipersempit. Itu bukan celah — tabel landing adalah riwayat, dan
satu-satunya perubahan yang tidak bisa merusak orang yang membacanya adalah
kolom nullable baru.

**Ia melengkapi deklarasi; ia tidak menggantikannya.** Tanpa deklarasi apa pun ia
ditolak sebelum extract, dengan menyebut tabelnya: membuat seluruh tabel dari
payload adalah keputusan lain, dan SDK tidak membuatnya. Pada layout landing
kerugiannya konkret — `brevis_received_at` akan keluar sebagai teks alih-alih
timestamp, dan `brevis_loaded_at` tidak pernah ikut dalam baris sama sekali,
sehingga pengukuran latensi end-to-end akan hilang begitu saja.

**SDK tetap tidak menyimpulkan.** Tipe berasal dari BENTUK field, tidak pernah
dari nilainya: `21129` dan `8.89` sama-sama mendeklarasikan `STRING`, sehingga
pada hari seri itu menerbitkan bilangan bulat tidak ada yang berubah. Tipe yang
dibaca dari batch pertama adalah kegagalan yang dicegah aturan ini.

**Dan `null` tidak menentukan apa-apa — tetapi tetap ada di baris.** Itu dua
pertanyaan yang berbeda, dan menjawabnya dengan satu jawaban membuat v0.77.0
berbiaya.

Null tidak punya bentuk, sehingga **skema** tidak punya apa-apa untuk dibaca:
field yang tiba `null` tidak mendeklarasikan kolom, dan kolom muncul dari record
pertama yang membawa sebuah NILAI, dengan bentuk yang dimiliki nilai itu.
Sebaliknya, null akan menjadi `STRING` karena tidak ada jawaban, dan array
pertama sesudahnya akan membuat tujuan menolak seluruh batch — tanpa migrasi
`STRING` ke `JSON` untuk membatalkannya.

**Baris** membawa apa yang dikirim produser, termasuk null. Harus demikian:
kolom yang Anda DEKLARASIKAN di `Target.Schema` yang tiba kosong harus ada di
baris, atau pemeriksaan yang membandingkan deklarasi dengannya membaca rantai
yang berhenti menghasilkannya — dan menolak load. Itulah yang dilakukan v0.77.0,
dan yang dibatalkan 0.78.0.

Tujuan menyelesaikan sisanya: field yang null di SETIAP record dalam batch,
tanpa kolom di tabel, tidak ditulis di mana pun dan tidak menolak apa pun. Null
dan field yang tidak ada mendarat sebagai `NULL` yang sama, sehingga tidak ada
yang hilang.

**Field yang berubah bentuk ditolak, tidak diserap.** Jika `valor` tiba sebagai
skalar dan membuat kolom teks, objek yang tiba kemudian adalah perubahan jenis
dan batch berhenti — dengan komentar kolom itu sendiri di pesan, sehingga Anda
bisa melihat bahwa tidak ada yang mendeklarasikannya. Ganti nama kolom, atau
migrasikan sendiri.

**Di BigQuery, dua ejaan dari satu nama adalah satu kolom.** BigQuery melipat
huruf besar-kecil kolom: batch yang membawa `nationalID` di satu record dan
`nationalId` di record lain mendeklarasikan **satu** kolom, bukan dua, dan ejaan
yang menang adalah yang pertama dalam urutan sortir — `nationalID`, karena `D`
terurut sebelum `d`. Bukan yang pertama tiba: batch yang sama yang diputar ulang
tidak boleh membuat kolom dengan ejaan berbeda. Deklarasi yang menyebut
`createdat` terhadap tabel yang kolomnya `createdAt` juga kolom yang sama,
sehingga tidak ada yang ditambahkan dan tidak ada yang ditolak.

**Satu record yang membawa kedua ejaan ditolak, dengan menyebut keduanya.** Tujuan
punya satu kolom dan salah satu dari kedua nilai akan menang secara diam-diam,
sehingga load berhenti: ganti nama salah satunya di `Transform`, atau petakan
keduanya ke satu field. Dua *record* yang masing-masing membawa satu ejaan tidak
masalah — itu trafik CDC biasa, dan itulah yang ingin didaratkan aturan ini.

**Tidak ada yang dimigrasikan.** Tabel yang sudah memiliki dua kolom yang hanya
berbeda huruf besar-kecil mempertahankan keduanya; tidak ada kolom yang diganti
namanya dan tidak ada yang dihapus.

**Postgres dan MySQL tidak melipat.** Identifier berkutip di sana berbeda,
sehingga dua ejaan adalah dua kolom — dengan sengaja, dan fetcher yang sama yang
diarahkan ke BigQuery akan mendaratkannya dalam satu.

**Setiap kolom yang dibuat batch menyatakannya, di dalam tabel.** Komentar di
Postgres dan MySQL, deskripsi field di BigQuery, dengan tanggalnya:

```
brevis: added from a batch on 2026-09-29; the type is the landing rule
(scalar text, object and array json), not a decision
```

Enam bulan kemudian itulah satu-satunya yang tersisa yang menjawab "kapan kolom
ini muncul, dan siapa yang memutuskan". Baris log sudah dirotasi pada saat itu.

**Ini bukan sebuah mode.** `Target.Schema` tetap Anda yang mendeklarasikan
seperti apa tabel itu — ini menyerahkan layout yang sudah ada alih-alih membuat
Anda mengetiknya. Tidak ada di SDK yang berperilaku berbeda karena Anda
memakainya, dan pipeline yang mendeklarasikan skemanya sendiri tidak terpengaruh.

**Tulis nama tabel sekali.** Ia masuk ke `Landing`, karena merupakan slot kedua
dari id, dan ke tujuan, karena di sanalah baris pergi. **Tidak ada yang
memeriksa bahwa keduanya sama**: `Writer` mengetahui tabelnya sendiri tetapi
mengeksposnya hanya lewat `Describe`, yang merupakan nama untuk log dan error,
dan id yang dibangun atasnya akan mengikat setiap id yang sudah ditulis pada
string log. Jika keduanya tidak selaras, baris mendarat dengan benar dengan id
yang dicetak untuk tabel yang tidak ditulis siapa pun. Tampaknya baik-baik saja.
Id itu tidak akan cocok dengan milik gateway.

**Tiga kolom bernilai `NULL`, dan itu jawaban yang jujur.** `brevis_stream` dan
`brevis_gateway` menyebut hal yang tidak dimiliki pipeline —
`brevis_gateway IS NULL` adalah cara membedakan baris pipeline dari baris
gateway. `brevis_received_bytes` juga `NULL`: milik gateway menghitung apa yang
tiba di kabel, envelope termasuk, dan JSON record itu sendiri sekitar 30% lebih
kecil. Satu kolom dengan dua arti akan membuat penjumlahan lintas baris dari
kedua jalur salah sebesar porsi yang berasal dari masing-masing. `length(data)`
menjawab versi pipeline dengan tepat.

**Merge membutuhkan `DedupKey`, dan tanpanya tidak ada merge sama sekali.**
Setiap driver mencocokkan pada `ingestion_id` kecuali diberi tahu lain, dan
kolom identitas layout ini adalah `brevis_ingestion_id`:

```go
Target{
    To:       postgres.Table{DSN: dsn, Name: table, CreateTable: true},
    Schema:   sdk.LandingSchema(sdk.LandingOptions{UniqueID: true, Keyed: true}),
    Dedup:    sdk.DedupMerge,
    DedupKey: sdk.LandingColumnID,
}
```

`UniqueID` adalah yang memasang constraint UNIQUE pada id, dan Postgres serta
MySQL sama-sama menolak merge tanpanya — `ON CONFLICT` tidak punya apa-apa untuk
dicocokkan. `DedupKey` yang tidak dideklarasikan `Schema` Anda ditolak sebelum
apa pun berjalan, karena merge pada kolom yang tidak dimiliki tabel tidak
mencocokkan apa-apa, dan merge yang tidak mencocokkan apa-apa tampak persis
seperti yang mencocokkan semua yang seharusnya.

**Per tujuan, dan saat ini MySQL tidak bisa.** BigQuery tidak punya constraint
unik dan menolak deklarasinya sama sekali. `string` MySQL adalah `LONGTEXT`, dan
MySQL tidak mau membuat key atas `LONGTEXT` tanpa panjang — sehingga tabel landing
yang melakukan merge tidak dapat *dibuat* di sana sama sekali:

```
Error 1170 (42000): BLOB/TEXT column 'brevis_ingestion_id' used in key
specification without a key length
```

Postgres berfungsi, karena `string`-nya adalah `TEXT`. Di MySQL, mendaratlah
dengan `DedupNone` dan selesaikan versi terkini di hilir, atau buat tabel itu
sendiri dengan `CreateSQL` dan kolom id yang berukuran.

**Id bersifat content-addressed.** Record yang sama menghasilkan id yang sama dari
kedua jalur, dan record yang berubah menghasilkan yang baru — itulah yang
membuat run ulang menjadi no-op alih-alih duplikat.

## Referensi

- [pkg.go.dev](https://pkg.go.dev/github.com/AreteAcademy/brevis/sdk) — API lengkap
- [`examples/`](https://github.com/AreteAcademy/brevis/tree/master/examples) — dua belas contoh yang dapat dijalankan
- [`CHANGELOG.md`](https://github.com/AreteAcademy/brevis/blob/master/CHANGELOG.md) — riwayat versi demi versi
