---
title: Ingestion
description: Endpoint HTTP yang mendaratkan data — validasi, identifikasi, batch, kirim. Image-nya sendiri, versinya sendiri.
group: Ingestion
order: 18
slug: ingestion
---

Gateway ingestion adalah **endpoint HTTP yang mendaratkan data**. Klien melakukan
`POST`, mendapat `202`, dan apa yang tiba menjadi record di sebuah topik, tabel,
atau bucket.

![Alur Gateway: sebuah POST ke /v1/clicks didekode, diberi ingestion_id, dijawab dengan 202 seketika dan ditambahkan ke sebuah batch; sebuah worker mengirim batch ke sink, di sini files. Gateway tidak memiliki database.](/assets/flow-gateway.svg)

Ia adalah **binary yang berbeda, modul Go yang berbeda, versi yang berbeda, dan
image yang berbeda**. Engine mengorkestrasi dan tidak pernah menyentuh data
pelanggan; gateway tidak melakukan hal lain. Penomorannya independen dengan
sengaja: engine ada di 0.17 dan gateway di 0.27, yang merupakan pernyataan yang
benar.

Dengan `gateway.yaml` dari bagian di bawah, dan key yang diminta di `keys_from`:

```bash
docker run -p 8080:8080 -e BREVIS_INGEST_KEYS=dev-key \
  -v ./gateway.yaml:/etc/brevis/gateway.yaml:ro \
  areteacademy/brevis-gateway:0.32.0
```

```text
2026/10/09 01:03:01 drain budget 30s; set terminationGracePeriodSeconds >= 40
2026/10/09 01:03:01 connections idle out after 1m30s and are recycled after 1m0s
2026/10/09 01:03:01 listening on :8080
2026/10/09 01:03:01 metrics on :9090/metrics
```

Sebuah `POST` kembali dengan `202` seketika:

```bash
curl -X POST localhost:8080/v1/clicks -H 'Authorization: Bearer dev-key' \
  -d '{"event_id":"e-1","occurred_at":"2026-10-08T12:00:00Z","page":"/pricing"}'
```

```text
{"accepted":1,"rejected":null}
```

Tanpa kredensial Google Cloud, batch di-retry dan masuk ke dead letter — lihat
[Ketika sink menolak](#when-the-sink-refuses).

Image `-slim` hanya membawa sink `auto_table`, `files`, dan `postgres`; `pubsub`
pada contoh ini dan sink lainnya ada di image penuh. Image slim dengan sink yang
tidak dibawanya ditolak saat start, dengan alasannya.

## File adalah kontraknya

Tidak ada kode yang perlu ditulis kecuali hook. Stream, path, format, identitas,
retry, tujuan, dan dead letter — semuanya di YAML, dan setiap field ditolak bila
tidak dapat dipenuhi.

```yaml
name: events_gateway

listen:
  addr: :8080
  max_body: 1MiB
  idle_timeout: 90s                  # the default; it cannot be turned off
  max_conn_age: 60s                  # off unless set — see below
  auth:
    type: bearer
    keys_from: BREVIS_INGEST_KEYS   # the NAME of the variable, never the keys

metrics:
  addr: :9090                        # its own port, never the ingest one

streams:
  - name: clicks
    path: /v1/clicks
    format: json                     # json | array | ndjson — declared, never sniffed

    identity:
      provider: web
      entity: click
      source_key: event_id
      record_ts: occurred_at

    buffer:
      flush: {every: 1s, records: 500}

    sink:
      type: pubsub
      project: acme-prod
      topic: clicks

    dead_letter:
      type: files
      path: /var/dead-letter/
```

`format` dideklarasikan dan tidak pernah ditebak: menebak adalah cara batch
seribu menjadi satu baris yang berisi sebuah array.

## `ingestion_id`, yang merupakan intinya

Setiap record keluar dengan `ingestion_id`: **UUID v5 yang dibekukan** atas
`provider|entity|source_key|record_ts`.

Ia adalah **id yang sama** yang dihitung fetcher batch untuk record yang sama.
Baris yang didaratkan gateway dan baris yang didaratkan pipeline [SDK](/docs/sdk/)
adalah *satu baris*, tanpa rekonsiliasi di antara keduanya.

Dua konsekuensi praktis:

- **`POST` yang di-retry adalah record yang sama.** Klien boleh mengirim lagi
  tanpa takut; tujuan dengan `merge` menyerap retry itu alih-alih menulis baris
  kedua.
- **Rumusnya dibekukan.** Keempat field wajib, dan menghilangkan salah satunya
  menghasilkan id yang *berbeda* alih-alih yang lebih lemah. File menolak
  konfigurasi yang tidak lengkap alih-alih menerimanya.

Tool ingestion memindahkan byte dengan sangat baik dan tidak punya pendapat
tentang apa *itu* sebuah record. Pendapat itulah yang ditambahkan gateway.

## Request tidak pernah menunggu sink

Ia mendekode, menjalankan hook, menghitung identitas, menambahkan ke buffer, dan
kembali. **Hanya itu.** Batch penuh pergi ke worker pool, dan publish, `COPY`,
retry, dan dead letter semuanya terjadi di sana.

Ini bukan kosmetik. Sebelum ada, request yang kebetulan memenuhi batch
menjalankan pengiriman secara inline — sehingga satu pemanggil dari setiap
`flush.records` membayar seluruh round trip, dan ketika sink mati, seluruh jendela
retry. p99 yang dibentuk oleh pemanggil mana yang sial bukan p99 yang bisa
ditindaklanjuti siapa pun.

```yaml
buffer:
  flush:
    every: 1s         # age
    records: 500      # count
    size: 8MiB        # bytes — whichever is crossed FIRST sends the batch
    max_age: 30s      # and this one sends it REGARDLESS
  workers: 4          # batches delivered at once
  queue: 64           # full batches that may wait for a worker
  max_records: 10000  # events held in memory before the gateway says no
  max_bytes: 256MiB   # the same ceiling in bytes
```

### Jam flush adalah milik deployment

`buffer.flush.every` adalah `time.AfterFunc` di **setiap replika**. Maka job load
yang diterima sebuah tabel adalah fungsi dari jumlah replika, bukan trafik:

```
jobs / day / table  =  replicas × 86400 / every
```

Di BigQuery, terhadap 1.500 per tabel per hari, **dua replika pada 60d sudah 192%
dari kuota yang tidak dapat dinaikkan** — sebelum HPA berbuat apa pun. Dan
arahnya berlawanan dengan intuisi: gateway scale out untuk konkurensi HTTP
sementara sink menginginkan konsentrasi.

```yaml
buffer:
  flush:
    every: 60s
    max_age: 300s
    claim: true
```

Dengan `claim`, sebuah replika melakukan set-if-absent untuk stream dan jendela
sebelum flush pada timer — dan timer bangun pada **batas jendela**, sehingga
setiap replika meminta key yang sama. Satu flush per jendela, berapa pun jumlah
replika. Dan ia menyesuaikan diri: replika hanya menunggu ketika ada yang benar-
benar bersaing, sehingga scale in mengembalikan latensi yang lebih rendah tanpa
ada yang mengedit konfigurasi.

Diukur dengan dua replika dan satu Redis, jendela 5d: **8 job load menjadi 6,
satu per jendela, bergantian 3/3** — tidak ada jendela dengan dua.

**Ia membutuhkan backend bersama.** Dengan `memory` setiap replika memenangkan
klaim-nya sendiri dan tidak ada yang berkoordinasi. Dan ia **mensyaratkan
`max_age`**, yang ditolak konfigurasi bila tidak ada: klaim mengubah `every`
menjadi target, dan target tanpa batas atas adalah latensi tanpa apa pun yang
membatasinya.

**Metastore yang tidak terjangkau tetap flush.** Kehilangan koordinasi berbiaya
job load; menolak flush menahan data. Diverifikasi dengan mematikan Redis di
tengah run: 40 dari 40 event mendarat, tidak ada yang tertahan.

**`outcome="unreachable"` adalah satu yang layak diberi alert.** Backend tidak
dapat ditanya, sehingga klaim gagal terbuka: koordinasi mati dan job load
berlipat ganda lagi menurut jumlah replika — secara diam-diam, karena setiap
flush masih tampak seperti flush.

Dan klaim memberi **eksklusivitas, bukan keadilan**: siapa yang meminta lebih dulu
menang. Run dua replika yang nyata mengukur 4/1, bukan pembagian rata. `max_age`
adalah yang mencegah yang sial kelaparan.

Dengan `claim: true` dan `metastore: memory` gateway memperingatkan **sekali saat
boot**: setiap replika memenangkan klaim-nya sendiri, sehingga semuanya tampak
benar dan tidak ada yang berkoordinasi.

### `every` adalah target; `max_age` adalah janji

Ketiga pemicu di atas menentukan kapan batch **mencoba** keluar. `max_age`
menentukan kapan ia keluar **apa pun yang terjadi**.

Hari ini perbedaannya inert, karena tidak ada yang menunda target. Ia mulai
penting begitu jam flush tidak lagi per proses: `buffer.flush.every` adalah
`time.AfterFunc` di setiap replika, sehingga jumlah job load yang diterima tabel
adalah fungsi dari **jumlah replika**, bukan trafik — `replicas × 86400 / every`.
Di BigQuery, terhadap 1.500 per tabel per hari, **dua replika pada 60d sudah 192%
dari kuota**.

Begitu sebuah replika dapat menyerahkan jendela ke yang lain, ia terus mengisi,
dan `max_age` adalah yang mencegah siapa pun menahan data tanpa batas sambil
menunggu giliran.

Ia juga menyelesaikan drain: apa yang dapat **ditahan** buffer saat `SIGTERM`
menjadi dibatasi olehnya, dan `terminationGracePeriodSeconds` yang dicetak
gateway saat boot diturunkan darinya.

Batas atas yang **di bawah** target ditolak saat load — itu berarti target tidak
pernah berlaku.

### Mengapa ada pemicu ukuran

Dua lainnya adalah **hitungan**, dan hitungan tidak bisa membedakan 500 event
2 KB dari 500 event 2 MB — 1 MB melawan 1 GB, angka yang sama di file.
`max_records: 10000` punya titik buta yang sama: 20 MB atau 20 GB, dan tidak ada
di YAML yang bisa menyatakan yang mana.

Produser yang mulai mengirim seluruh dokumen alih-alih id-nya menjatuhkan pod,
dan sampai ini ada **tidak ada field yang dapat mengekspresikan batasnya**.

Ukuran diukur pada apa yang **tiba** — bukan pada berat event di memori
(`map[string]any` beberapa kali lipat JSON-nya) dan bukan setelah hook yang
menggelembungkannya. Sebuah aproksimasi, dan yang tepat: gratis saat decode,
bergerak mengikuti payload, dan yang dijaganya adalah record yang membesar
sepuluh kali lipat.

**Di BigQuery `size` adalah batas atas, bukan target.** Tujuan itu mengizinkan
1.500 job load per tabel per hari, itulah sebabnya `every` punya lantai 60 detik.
Tetapi lantai itu mengatur **timer**: ukuran yang terpicu setiap beberapa detik
langsung melewatinya dan menghabiskan kuota yang sama. Tidak ada yang menolaknya
saat load, karena laju kedatangan tidak dapat diketahui di sana. Yang ada
sebagai gantinya adalah visibilitas:

```
brevis_gateway_flushes_total{stream,trigger}   trigger = time | records | size
```

`trigger="size"` yang terus naik pada stream BigQuery memberi tahu Anda bahwa
jendela bukanlah yang melakukan batching.

Counter itu per stream, dan kuota per **tabel**: dengan `auto_table` satu rute
menjadi banyak tabel, dan 94 tabel yang melaporkan satu angka tidak
mengatakan apa-apa tentang satu pun dari mereka. Di balik
`BREVIS_INGESTION_METRICS` ada satu yang menyebutkannya:

```
brevis_gateway_table_flushes_total{stream,table,trigger}
```

Satu flush tabel adalah satu job load untuknya, jadi ini adalah seri yang harus
diberi alert pada 1.200 dari 1.500 yang diizinkan BigQuery. Separuh yang tak
bersyarat adalah baris saat boot, yang menyatakan berapa biaya `flush.every` per
tabel per hari sebelum event tiba.

**Ia dibatasi, bukan fire-and-forget.** Goroutine per batch akan mengubah
gangguan sink menjadi memori tak terbatas. Ketika antrean dan buffer sama-sama
penuh, jawabannya adalah **`503` dengan `Retry-After`** alih-alih `202` untuk
event yang tidak punya tujuan. Event yang diterima tetapi tidak pernah dikirim
adalah satu-satunya hasil yang justru dihindari layanan ini.

Dan `503` itu **aman di-retry**, dengan cara yang hampir tidak bisa diklaim
layanan lain: `ingestion_id` adalah fungsi beku dari event itu sendiri, sehingga
body yang sama yang dikirim lagi adalah *record yang sama*.

## Koneksi menentukan replika mana yang mendapat beban

Di balik Service `ClusterIP` Kubernetes, kube-proxy memilih backend **sekali per
koneksi TCP**, bukan per request. Sebagian besar klien menjaga koneksi tetap hidup
dan memakainya ulang, sehingga produser dengan segelintir koneksi mengirim
semuanya ke satu atau dua replika yang sama berapa pun yang berjalan — dan
replika yang ditambahkan HPA tidak menerima apa-apa, karena tidak ada yang
menghubunginya.

```yaml
listen:
  idle_timeout: 90s     # closes a connection nobody came back to
  max_conn_age: 60s     # after this, the next response asks the client to redial
```

**`max_conn_age` nonaktif secara default**, dan itu disengaja: berapa lama klien
boleh mempertahankan satu koneksi adalah kebijakan, dan ia menukar satu handshake
dalam klaster per koneksi per usia dengan beban yang tersebar. Request itu sendiri
selalu dilayani — header dipasang pada respons, tidak ada yang di-retry dan tidak
ada batch yang hilang.

**`idle_timeout` punya default dan tidak dapat dimatikan.** Melayani tanpa batas
berarti satu file descriptor per klien terlantar sampai proses di-restart. Atur
yang panjang bila itu yang Anda inginkan; maka ia adalah angka yang dipilih
seseorang.

Keduanya independen: koneksi bertemu batas mana pun yang ditemuinya lebih dulu,
dan koneksi hanya mencapai usia itu dengan dipakai — yang persis produser
berumur panjang yang ingin diseimbangkan ulang oleh usia tersebut.

## Ketika sink menolak

Batch di-retry — empat percobaan dalam sekitar tujuh detik secara default,
berlipat dua dengan jitter — lalu ia masuk ke **dead letter**, dengan alasan pada
setiap record:

```json
{
  "event_id": "dl-9",
  "ingestion_id": "84aaee4b-66af-5cc0-b97b-6856dec95b25",
  "_dead_letter_reason": "rpc error: code = NotFound desc = Topic not found",
  "_dead_letter_sink": "pubsub:acme-prod/clicks",
  "_dead_letter_at": "2026-09-25T02:02:32Z"
}
```

Alasan itu berjalan **pada record** dan bukan hanya di log, karena siapa pun yang
menemukan file itu kemudian memiliki event dan bukan log — dan *mengapa ini ada
di sini* adalah pertanyaan pertama mereka.

**Stream tanpa `dead_letter` ditolak saat load.** Menjadikannya diam sebagai
default akan menaruh keputusan di tempat yang tidak dibuat siapa pun, dan batch
yang ditolak yang hanya menjadi baris log adalah kehilangan data secara diam-diam.

## Hook adalah Go, dikompilasi masuk

```go
hooks := gateway.NewHooks()
hooks.MustRegister("enrich_clicks", enrichClicks)
gateway.Main(hooks, gateway.WithSinks(sinks))

func enrichClicks(e map[string]any) (map[string]any, error) {
    host, _ := e["host"].(string)
    e["tenant"] = strings.Split(host, ".")[0]
    return e, nil     // returning nil DROPS the event, on purpose
}
```

YAML menyebut nama hook; ia tidak membawanya. Diukur sebelum memilih: fungsi Go
berbiaya **93 ns/op** melawan 959 milik Starlark dan 1.281 milik yaegi, tanpa
menambah apa pun pada binary — dan `plugin.Open` sama sekali bukan opsi, karena
di bawah `CGO_ENABLED=0`, cara semua artefak di sini dibangun, ia mengembalikan
`plugin: not implemented`.

Apa harganya, dinyatakan di tempat yang akan dibaca orang: **menambah hook adalah
rebuild dan deploy, bukan perubahan konfigurasi.** Itu pertukaran yang tepat
selama hook ditulis oleh orang yang merilis binary, dan yang salah pada hari
pelanggan harus mengubah satu tanpa rilis.

Error di dalam hook mengirim event *itu* ke jalur dead letter dan tidak pernah
menggagalkan batch di sebelahnya. Panic juga: ia di-recover per event, karena
satu record yang cacat tidak boleh menjatuhkan proses yang melayani setiap stream
lain.

## Ketika event terlalu besar

```yaml
oversize:
  larger_than: 256KiB
  archive: {type: files, path: gs://acme-oversize/clicks/}
  hook: strip_heavy_clicks
```

Event ditulis ke `archive` **secara utuh**, dan versi yang diperkecil melanjutkan
ke dalam stream dengan membawa penunjuk kembali:

```json
{
  "event_id": "big-1",
  "ingestion_id": "bd8ac083-9796-50f5-bab6-77a4e2e57ad0",
  "_oversize": true,
  "_oversize_archive": "files:gs://acme-oversize/clicks/",
  "_oversize_bytes": 2149
}
```

Ini adalah pola **Claim Check**, dan ia mengalahkan `413` polos karena `413`
kehilangan event — dan payload yang terlalu besar biasanya yang paling menarik
yang dimiliki seseorang: itu request dengan seluruh dokumen terlampir, kasus yang
layak di-debug.

`larger_than` mengukur satu **event**, setelah hook. `listen.max_body` adalah hal
yang berbeda: ia membatasi seluruh **request** dan menolak dengan `413` sebelum
satu byte pun diparse.

## Apa yang dihitungnya

Sebelas seri Prometheus, pada **port-nya sendiri** — tidak pernah port ingest, dan
aturan itu lebih penting di sini daripada di engine: port ingest bersifat publik
secara konstruksi, di situlah klien melakukan `POST`, sehingga `/metrics` di sana
akan mempublikasikan setiap nama stream, path, dan tujuan kepada siapa pun yang
menemukan path-nya. Menyebut alamat yang sama untuk keduanya ditolak saat load.

```
brevis_gateway_events_received_total{stream,format}     counter
brevis_gateway_events_rejected_total{stream,reason}     counter  per event, after decode
brevis_gateway_requests_refused_total{stream,reason}    counter  per request: unauthorized|body_too_large|malformed|empty|saturated
brevis_gateway_batches_total{stream,sink,outcome}       counter  delivered|retried|buried
brevis_gateway_reconciled_total{stream,sink}            counter  landed after the send window; also delivered
brevis_gateway_flushes_total{stream,trigger}            counter  time|records|size
brevis_gateway_flush_windows_total{stream,outcome}      counter  won|yielded|ceiling|unreachable
brevis_gateway_ingested_bytes_total{stream,table}        counter  opt-in
brevis_gateway_ingested_events_total{stream,table}      counter  opt-in
brevis_gateway_table_flushes_total{stream,table,trigger} counter  opt-in
process_start_time_seconds                              gauge    unprefixed, on purpose
brevis_gateway_saturated_total{stream}                  counter  the 503s
brevis_gateway_delivery_seconds{stream,sink}            histogram
brevis_gateway_buffer_records{stream}                   gauge
brevis_gateway_queue_batches{stream}                    gauge
```

`process_start_time_seconds` **tidak** diberi prefiks `brevis_`, dan itu
disengaja: collector mencari persis nama itu. Ia menyatakan kapan proses mulai,
dan tanpanya collector yang melakukan penyesuaian waktu mulai — receiver
Prometheus OpenTelemetry, yang menjadi dasar Google Managed Prometheus —
menjangkarkan setiap seri kumulatif pada **scrape pertamanya** dan menghabiskan
nilai scrape itu sebagai baseline. Hasilnya setiap counter terbaca rendah, dengan
delta yang benar dan total yang salah.

Tiga yang terakhir adalah yang tidak diminta siapa pun sampai setelah insiden
pertama. `saturated_total` adalah satu-satunya angka yang menyatakan bahwa klien
disuruh mundur; `buffer_records` terhadap `buffer.max_records` adalah satu-satunya
yang **memprediksinya**.

`reason` adalah himpunan tertutup dan tidak pernah teks error: string error
membawa nama tabel, kolom, kadang baris, dan satu klien yang cacat akan mencetak
seri baru per request. Teksnya tetap di dead letter, pada record.

## `202`, bukan `200`

Gateway telah **menerima** event. Dengan `durability: memory` — satu-satunya tier
yang diimplementasikan — itulah seluruh yang dapat diklaimnya dengan jujur. Tier
yang berhak atas `200` adalah yang sudah menuliskannya, dan itu belum ada.

`disk` ditolak **dengan menyebut namanya**, bukan diterima dan diperlakukan
sebagai memory: orang yang menulis `disk` percaya event-nya bertahan dari crash,
dan menyetujui tanpa memenuhinya adalah persis kegagalan yang dicegah field
tersebut.

Apa yang ada di buffer saat shutdown dikirim, bukan dibuang — lewat jalur yang
sama dengan batch penuh, di-retry dan dikuburkan.

## Apa yang tidak dilakukannya

- **Ia tidak muncul di console.** Ia tidak berbagi database dengan antarmuka
  engine, sehingga tidak ada halaman `/ingestion` — dan evolusi skema yang
  dilakukan `auto_table` juga tidak tampil di sana. Itu langkah berikutnya dan
  belum ditulis.
- **Stream dengan `table` tidak mengevolusikan skema.** Ketika Anda menyebut
  tabelnya, kontraknya adalah milik tabel: field yang tidak dimilikinya ditolak
  dengan pesan yang memperbaikinya, alih-alih mengubah tabel yang sudah direview
  seseorang. Menumbuhkan kolom sendiri adalah yang dilakukan `auto_table`, dan itu
  perbedaan antara keduanya.
- **Stream dengan `table` tidak membuat tabel dan tidak membuat index.** Layanan
  yang membuat tabel mengubah typo menjadi tabel kedua yang tidak dibaca siapa pun
  — itulah sebabnya pembuatan hanya terjadi di bawah `auto_table`, ketika `naming`
  membatasi nama mana yang boleh ada dan `listen.auth` wajib.
- **Image yang dipublikasikan tidak punya hook**, yang merupakan artefak jujur
  untuk desain hook terkompilasi: ia melayani stream yang tidak mendeklarasikan
  `hook:`.

## Satu rute, N tabel

`auto_table` merutekan setiap event ke tabel yang disebut oleh **envelope**,
membuat tabel itu bila belum ada, dan menumbuhkan kolom ketika field baru muncul:

```json
{"table_name": "app_orders", "operation": "INSERT", "unique_key": "id",
 "data": {"id": "A-1", "total": 150, "customer": {"id": 7, "uf": "SP"}}}
```

Field kontrol di atas, record di dalam `data`, dan tujuh kolom pelacakan
`brevis_*` pada setiap tabel. Satu rute, N tabel, tanpa deklarasi apa pun.

Ia ada di [Sink ingestion](/docs/ingestion-sinks/#one-route-n-tables-nothing-declared).

## Ke mana selanjutnya

| jika Anda ingin | buka |
|---|---|
| tujuan dan cara masing-masing menulis | [Sink ingestion](/docs/ingestion-sinks/) |
| `ingestion_id` dari sisi lain | [Go SDK](/docs/sdk/) |
| dashboard dan alert | [Observabilitas](/docs/observability/) |
