---
title: CLI
description: Dua belas subcommand engine, dengan flag, argumen, dan apa yang dibutuhkan masing-masing.
group: Referensi
order: 8
slug: cli
---

Engine adalah satu binary dengan subcommand, bukan beberapa binary: API,
scheduler, dan worker adalah peran dari sistem yang sama, dan satu binary
menjaga satu versi, satu image, dan satu jalur build.

```
brevis [command] [flags]
```

| command | database | kredensial di luar `local` | peran |
|---|---|---|---|
| [`serve`](#serve) | **ya** | **wajib** | API + antarmuka |
| [`scheduler`](#scheduler) | **ya** | **wajib** | kedua loop |
| [`migrate`](#migrate) | **ya** | **wajib** | skema |
| [`publish`](#publish) | **ya** | **wajib** | menyimpan workflow dan jadwal |
| [`backfill`](#backfill) | **ya** | **wajib** | memproses ulang suatu rentang |
| [`cancel`](#cancel) | **ya** | **wajib** | menghentikan sebuah run |
| [`run`](#run) | tidak | — | menjalankan sekarang, di instance itu sendiri |
| [`validate`](#validate) | tidak | — | memvalidasi YAML workflow |
| [`brand`](#brand) | tidak | — | memvalidasi YAML branding |
| [`hash`](#hash) | tidak | — | membuat hash password |
| [`version`](#version) | tidak | — | versi, commit, build |

Enam yang pertama memuat konfigurasi, yang **gagal saat boot** tanpa
`BREVIS_DATABASE_URL`. Lima lainnya tidak — itulah sebabnya `validate` bekerja
di CI, tempat tidak ada Postgres.

Keenam command yang sama itu mewarisi aturan kredensial: dengan `BREVIS_ENV`
selain `local`, naik tanpa tiga variabel autentikasi adalah **error boot**.

---

## serve

Menjalankan API HTTP dan antarmuka. Tanpa flag — semuanya berasal dari
environment.

```bash
brevis serve
```

Berhenti dengan anggun pada `SIGINT`/`SIGTERM`, sehingga deploy tidak memotong
request yang sedang berjalan. Proses ini **tidak mematerialisasi jadwal**: itu
membutuhkan `brevis scheduler` di sampingnya.

`CMD` dari image `api` (distroless, tanpa shell).

---

## scheduler

Kedua loop: scheduler mematerialisasi jadwal menjadi run, dispatcher menarik
dari antrean dan mengeksekusi.

```bash
brevis scheduler --interval 5s --concurrency 4 --max-pods 10
```

| flag | tipe | default | |
|---|---|---|---|
| `--interval` | duration | `10s` | selang antar siklus |
| `--concurrency` | int | `5` | **run** simultan |
| `--max-pods` | int | `5` | total **step** simultan |

`CMD` dari image `worker` (alpine dengan shell, karena step `run:`
membutuhkannya).

---

## alert

Mengirim alert yang dicatat scheduler. Proses ketiga di samping `serve` dan
`scheduler`.

```bash
brevis alert --interval 1s --max-attempts 6
```

| flag | tipe | default | |
|---|---|---|---|
| `--interval` | duration | `1s` | selang antar siklus pengiriman |
| `--max-attempts` | int | `6` | percobaan sebelum alert dicatat sebagai tidak terkirim |

**Ia ada karena tempat alert ditulis, bukan karena pengiriman layak punya
proses.** Scheduler mencatat alert dalam transaksi yang sama dengan kegagalan yang
membenarkannya — entah run kehabisan percobaan dan alert ada, atau keduanya tidak
terjadi. Lalu sesuatu harus menguras tabel itu, dan ia harus bertahan dari
gangguan Slack maupun restart-nya sendiri. Sebelum ini, webhook yang gagal berarti
satu baris log dan alert yang hilang begitu saja.

Ia menolak berjalan tanpa channel terkonfigurasi (`BREVIS_SLACK_WEBHOOK`).
Proses pengiriman tanpa tujuan akan menguras outbox menjadi "tidak terkirim"
secepat ia terisi, dan baris-baris itu tampak persis seperti ditolak Slack.

Alert yang sudah ia tinggalkan **dipertahankan**: "diangkat, tidak terkirim, 4 percobaan,
403 dari Slack" adalah baris yang penting, karena itulah kasus ketika seseorang
menunggu pesan yang tidak akan datang.

`CMD` dari image `worker`.

---

## report

Mengirim ringkasan berkala tentang apa yang berjalan.

```bash
brevis report --window 168h              # sends it
brevis report --window 168h --dry-run    # prints it, sends nothing
```

| flag | tipe | default | |
|---|---|---|---|
| `--window` | duration | `168h` | seberapa jauh ke belakang |
| `--dry-run` | bool | `false` | cetak alih-alih kirim |

Sebuah **command**, bukan loop, dan itulah perbedaan antara laporan dan alert: ia
berjalan dari CronJob, sehingga tidak ada loop baru, tidak ada state baru, dan
tidak ada leader election — klaster sudah punya scheduler. `--dry-run` adalah
cara seseorang menjadi percaya pada pesan mingguan, dengan membaca apa yang akan
dikatakannya.

Ia **tidak** melewati outbox alert. Keduanya hanya berbagi channel pengiriman dan
tidak ada yang lain: kehilangan alert adalah gangguan yang tidak didengar siapa
pun, dan kehilangan ringkasan satu minggu hanyalah ringkasan minggu depan.

Pesannya memuat jumlah run, sukses, kegagalan, pipeline yang paling sering gagal,
yang paling lama, serta baris dan byte yang dilaporkan SDK. Ia **tidak** memuat
CPU atau memori, dan juga tidak mencetak nol untuk keduanya — engine tidak
mengumpulkan itu, dan pesan menyebutkan di mana keduanya berada.

Jendela kosong menyatakannya alih-alih melaporkan 100%. Tingkat keberhasilan dari
ketiadaan adalah angka paling menenangkan yang bisa dicetak sebuah laporan dan
yang paling tidak benar: minggu kosong biasanya berarti scheduler sedang mati.

`deployments/kubernetes/report.yaml` menjalankannya setiap minggu.

---

## migrate

```bash
brevis migrate up      # applies what is missing
brevis migrate down    # reverts the last one
brevis migrate status  # shows the state
```

Menerima tepat satu argumen. Migrasi tertanam di dalam binary dan diterapkan oleh
subcommand tersendiri — **`serve` tidak pernah mengubah skema**.

---

## validate

Memvalidasi satu atau lebih file workflow. Menerima file **atau direktori**.

```bash
brevis validate examples/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    hello                        dag  4 steps, 4 dependencias  (manual)
```

Ia tidak menyentuh database dan tidak menjalankan server, sehingga berjalan di
editor maupun CI. Ia keluar dengan kode bukan nol dan menghitung kegagalannya.

---

## run

Menjalankan workflow **sekarang, di instance itu sendiri**: tanpa antrean, tanpa
database, tanpa scheduler.

```bash
brevis run examples/hello.yaml
brevis run wf.yaml --param load_full=true --retries 3 --timeout 5m
```

| flag | tipe | default | |
|---|---|---|---|
| `--param` | dapat diulang | — | `key=value` untuk parameter yang dideklarasikan |
| `--workdir` | string | direktori file | direktori kerja untuk step |
| `--retries` | int | `1` | percobaan per step (`1` = tanpa retry) |
| `--timeout` | duration | `0` | timeout per step (`0` = tanpa batas) |

`--param` tanpa `=` adalah **error**, bukan peringatan: `--param load_full`,
dengan lupa memberi nilai, akan berjalan dengan default — dan operator akan
mengira nilainya sudah diterapkan.

:::warning Empat batasan
`run` hanya beroperasi dengan `BREVIS_ENV=local`; step dengan `image:` berjalan di
instance itu sendiri dan menyatakannya; tidak ada registry task Go, sehingga
`action:` untuk task yang tidak terdaftar gagal dengan menyebutkan yang tersedia;
dan `run` tidak membaca `BREVIS_HOSTS`, sehingga step dengan `host:` sama sekali
tidak dapat berjalan di sini — ia gagal dengan menyebutkan host-nya, bahkan
ketika variabelnya diatur.
:::

---

## publish

Menyimpan workflow dan jadwalnya di database. Menerima file atau direktori.

```bash
brevis publish examples/hello.yaml
brevis publish workflows/ --project acme --prune
```

| flag | tipe | default | |
|---|---|---|---|
| `--project` | string | `default` | slug proyek |
| `--prune` | bool | `false` | menghapus dari proyek workflow yang tidak ada dalam daftar |

Dengan `--prune`, riwayat workflow yang dihapus dipertahankan.

---

## backfill

Mematerialisasi slot masa lalu dari workflow yang sudah dipublikasikan. Ia
**memasukkan ke antrean, tidak mengeksekusi.**

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

| flag | | |
|---|---|---|
| `--from` | **wajib** | tanggal mulai, `YYYY-MM-DD` |
| `--to` | **wajib** | tanggal akhir, `YYYY-MM-DD` |
| `--param` | | berlaku untuk **setiap** slot dalam rentang |

`--to` mencakup seluruh hari: secara internal akhirnya menjadi `23:59:59` pada
tanggal itu.

---

## cancel

Membatalkan run yang masih menunggu atau sedang berjalan. Feed event
menampilkan `cli` sebagai yang membatalkannya.

```bash
brevis cancel 3f1c9a52-6d0e-4b7a-9c41-2b8e5d7f0a13
```

| jawaban | yang terjadi |
|---|---|
| `canceled` | run sedang menunggu di antrean; ia keluar dan tidak akan berjalan |
| `cancel requested` | run sedang berjalan; dispatcher-nya menghentikan step dalam `--cancel-poll` (2 d) |
| `already ended: <status>` | run sudah berakhir; tidak ada yang berubah |

Run yang dibatalkan tidak di-retry. Id yang tidak ada adalah error.

---

## gateway

Cara sebuah gateway muncul di `/data`: konfigurasinya, dipublikasikan sebagai
tindakan yang disengaja — tidak pernah trafiknya, dan tidak pernah saat runtime.

```bash
gateway describe gateway.yaml > manifest.json   # the gateway's own binary
brevis gateway publish manifest.json
brevis gateway unpublish events_gateway
```

`publish` memeriksa manifest secara utuh sebelum menulis apa pun, dengan menyebut
stream dan peran dari masalah pertama, dan mengganti tujuan gateway tersebut
dalam satu transaksi. `unpublish` menghapus gateway yang sudah dinonaktifkan, dan
menjadi error bila tidak ada gateway dengan nama itu yang dipublikasikan.

Cara sebuah `brevis-sql-api` muncul di Data → Endpoints: apa yang diterima dan
dibaca setiap endpoint, dipublikasikan sebagai tindakan yang disengaja — tidak
pernah trafiknya.

```bash
brevis-sql-api describe --config api.yaml --out manifest.json   # the API's own binary
brevis endpoints publish manifest.json
brevis endpoints unpublish metrics-api
```

`publish` memeriksa manifest secara utuh sebelum menulis apa pun dan mengganti
endpoint API tersebut dalam satu transaksi; nama, default, atau catatan yang
berbentuk kredensial menolak seluruhnya.

Command tersendiri dan bukan mode dari `brevis publish`, karena workflow dan
gateway punya semantik set yang berbeda: `publish --prune` tidak boleh pernah
menjangkau baris milik gateway.

---

## prune

Menerapkan kebijakan retensi. Ini **satu-satunya** hal di Brevis yang menghapus
sesuatu, dan ia tidak pernah berjalan sendiri — operator yang menjadwalkannya.

```bash
brevis prune --dry-run          # what would go, changing nothing
brevis prune
brevis prune --purge-after 0    # trim only, never delete a run
```

| flag | default | |
|---|---|---|
| `--trim-after` | `720h` | mengosongkan log, fase, dan output yang dipublikasikan |
| `--purge-after` | `8760h` | menghapus run sepenuhnya; `0` tidak pernah menghapus |

---

## brand

Memvalidasi file branding tanpa menjalankan server.

```bash
brevis brand brand.yaml
```

```
  ok    Brevis · Orchestration
        logo      /assets/logo.svg  (built-in symbol)
        accent    (the product's own)
        Powered by Brevis
```

Satu perbedaan perilaku dari boot: **file yang hilang adalah error**. Di
`serve`, ketiadaan berarti "pakai identitas default"; orang yang meminta
memvalidasi sebuah path berharap diberi tahu bahwa path itu tidak ada.

---

## hash

Membuat nilai `BREVIS_AUTH_PASSWORD_HASH`.

```bash
brevis hash
```

Password dibaca dari terminal **tanpa echo**, tidak pernah dari argumen: argumen
muncul di `ps` proses mana pun dan tertinggal di riwayat shell. Ia menolak
password yang lebih pendek dari 12 karakter.

Hash dikirim sendirian ke **stdout** dan label ke **stderr**, sehingga
`brevis hash > hash.txt` hanya menyimpan yang penting.

---

## version

```bash
brevis version
```

```
brevis 0.11.2
  commit  bb832ff
  build   2026-09-05T12:00:00Z
  go      go1.25.7 darwin/arm64
```

Dibangun langsung dengan `go build`, ia mencetak `brevis dev` tanpa commit dan
tanpa tanggal — dan membedakannya dari artefak rilis penting ketika seseorang
melaporkan perilaku yang aneh.
