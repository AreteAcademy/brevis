# Scheduler dan antrean

> Dua loop independen — siapa yang membuat run, siapa yang mengeksekusinya, dan mengapa keduanya dipisah.

*https://brevis.sh/id/docs/scheduler-and-queue/ · brevis.sh docs (id)*

---

brevis.sh memisahkan **pembuatan** sebuah run dari **eksekusi**-nya. Dua loop
berjalan bersamaan dan tidak bergantung satu sama lain.

```
scheduler                          queue
─────────                          ─────
reads the schedule
materialises the slot
creates the Run  ─────────────►  (pending in the database)
                                   claims it
                                   walks the graph
                                   executes each step
                                   records state and log
```

## Mengapa dipisah

Satu loop yang sekaligus menjadwalkan dan mengeksekusi punya satu sifat buruk:
ketika eksekusi macet, penjadwalan ikut berhenti. Slot untuk dua belas jam
berikutnya begitu saja tidak ada, dan tidak ada yang menyadarinya sampai
seseorang mencari data yang tidak pernah datang.

Dengan keduanya terpisah:

- **scheduler** boleh mati dan antrean terus menguras apa yang sudah dibuat;
- **antrean** boleh mati dan slot terus dimaterialisasi, untuk dieksekusi ketika
  ia kembali;
- **pemrosesan ulang tidak bergantung pada jam** — `backfill` membuat run masa
  lalu lewat jalur yang sama dengan yang dipakai cron.

## Prosesnya

Kedua loop berada dalam command yang sama:

```bash
brevis scheduler --interval 5s --concurrency 4 --max-pods 10
```

| flag | default | |
|---|---|---|
| `--interval` | `10s` | selang antar siklus scheduler |
| `--concurrency` | `5` | **run** simultan |
| `--max-pods` | `5` | total **step** simultan |

`--concurrency` dan `--max-pods` menghitung hal yang berbeda, dengan sengaja:
lima run dengan masing-masing tiga step paralel akan menjadi lima belas pod
apabila satu-satunya batas ada pada run.

:::note `serve` tidak mematerialisasi jadwal
Proses API menjalankan scheduler yang sama, tetapi **tanpa loop** — ia ada hanya
untuk melayani pemicu manual dari antarmuka, sehingga aturan "scheduler membuat
run" tetap punya satu pemilik. Agar jadwal terpicu, sebuah `brevis scheduler`
harus berjalan berdampingan.
:::

## Snapshot definisi

Ketika scheduler membuat sebuah run, ia menyimpan **definisi workflow di dalam
run itu**. Eksekusi membaca snapshot tersebut, bukan YAML di disk.

Alasannya konkret: di antara pemicu dan eksekusi, seseorang mungkin sudah
mengedit file dan men-deploy-nya. Tanpa snapshot, run yang dibuat pukul 05:00
dengan satu definisi akan dieksekusi pukul 05:02 dengan definisi lain — dan log
tidak punya cara untuk menjelaskan perbedaannya.

## Retry

Run yang gagal dicoba **tiga kali**, dan jedanya **berlipat dua**: dengan nilai
default, percobaan jatuh pada **0 d, 30 d, dan 1 mnt 30 d**.

```bash
brevis scheduler --max-attempts 3 --retry-backoff 30s --retry-backoff-max 1h
```

| flag | default | |
|---|---|---|
| `--max-attempts` | `3` | percobaan per run, termasuk yang pertama |
| `--retry-backoff` | `30s` | jeda pertama, dilipatduakan pada setiap percobaan sesudahnya |
| `--retry-backoff-max` | `1h` | batas atas jeda itu |

Proses mencetak jadwalnya saat boot, sehingga tidak perlu dihitung dari dua
angka:

```
scheduler and dispatcher are up  max_attempts=3 retry_backoff=30s retry_at="0s, 30s, 1m30s"
```

:::tip
**Vendor yang membatasi rate membutuhkan jeda lebih lama.** Pipeline semacam ini
gagal karena satu alasan di atas semua yang lain: upstream yang transien — API
yang menjawab 200 dengan "Anda telah mencapai batas request", gangguan kuota
warehouse sesaat. Itu butuh sekitar satu menit untuk pulih, dan retry yang selesai
sebelum itu sama saja dengan retry yang tidak pernah terjadi. `--retry-backoff 1m`
menempatkan percobaan pada 0 d, 1 mnt, dan 3 mnt.
:::

Retry berlaku per **run**, dan hanya menjalankan ulang step yang gagal —
`dbt_build` yang gagal tidak menjalankan ulang fetch di atasnya yang berhasil.

Nomor percobaan disimpan secara persisten, bukan state proses. Memulai ulang
worker tidak menghapus apa yang sudah diketahui tentang run itu.

Nomor percobaan **run** masuk ke nama pod. Tanpanya, retry akan memulai run dari
nol, bertemu pod percobaan sebelumnya dengan nama yang sama, dan menggantung di
`Pending` selamanya.

## Backfill

Mematerialisasi slot masa lalu dari workflow yang sudah dipublikasikan:

```bash
brevis backfill daily --from 2026-01-01 --to 2026-01-31
brevis backfill daily --from 2026-01-01 --to 2026-01-31 --param load_full=true
```

```
  31 run(s) de backfill enfileirados para daily (2026-01-01 a 2026-01-31)
  rode `brevis scheduler` para executa-los
```

Ia **memasukkan ke antrean, tidak mengeksekusi.** Eksekusi adalah tugas
`scheduler` — pemisahan yang sama lagi. `--to` mencakup seluruh hari.

## Dimulai oleh sebuah landing

Sebuah workflow dapat berjalan karena data tiba, bukan karena jam yang
memutuskannya:

```yaml
trigger:
  on_landed:
    - bigquery://acme-prod/bronze/orders
    - bigquery://acme-prod/bronze/*
  debounce: 5m
```

Setiap step sudah mendeklarasikan apa yang ditulisnya, lewat pipe yang membawa
fase-fasenya — itulah yang mengisi `/data`. Sebuah workflow berlangganan pada
salah satu nama itu dan mulai ketika sesuatu mendarat di sana.

`/*` di akhir berlangganan ke seluruh dataset. Ini ada untuk `auto_table`, yang
membuat satu tabel per rute: dataset adalah satu-satunya yang bisa disebut untuk
tabel yang tidak dideklarasikan siapa pun. Ia cocok dengan `bronze/orders` dan
tidak dengan `bronze_raw/orders`.

### Jendelanya, dan mengapa sepuluh landing menjadi satu run

`debounce` meleburkan lonjakan. Sepuluh tabel yang mendarat dalam lima menit
memulai **satu** run, bukan sepuluh, dan run itu menyebutkan yang mana saja:

```
BREVIS_AUTO_LANDED=bigquery://acme-prod/bronze/orders,bigquery://acme-prod/bronze/items
```

Daftar persisnya juga ada di `BREVIS_AUTO_PARAMS` sebagai JSON, yang sebaiknya
dibaca ketika sebuah target bisa mengandung koma.

Jendela dipotong dari **kapan engine mencatat landing**, bukan dari kapan step
mengatakan hal itu terjadi. Step yang melaporkan saat query-nya dimulai
melaporkan sesuatu yang benar sekaligus lama, dan satu jam yang melenceng akan
memecah lonjakan menjadi dua run.

Hilangkan `debounce` dan setiap landing memulai sebuah run.

### Yang tidak akan dilakukannya

**Workflow tidak pernah dimulai oleh tulisannya sendiri.** Engine membuang
landing yang penulisnya adalah workflow yang berlangganan itu sendiri dan mencatat
alasannya di log — tanpa itu, workflow yang membaca dan menulis tabel yang sama
akan memicu dirinya sendiri selamanya.

Penolakan terjadi **saat terpicu** dan bukan saat file diperiksa, dan itu adalah
keterbatasan, bukan kelalaian: `brevis validate` membaca file dan tidak ada file
yang mendeklarasikan apa yang ditulis sebuah step. Target warehouse ditentukan
oleh koneksi, bukan oleh YAML. Jadi `validate` meloloskan workflow yang akan
ditolak saat runtime, setiap kali, dengan alasan di log.

**Siklus pertama tidak memulai apa pun.** Trigger mulai menghitung sejak saat ia
aktif, seperti jadwal — kalau tidak, mempublikasikan satu trigger akan memicu run
untuk setiap landing dalam riwayat katalog.

## Alert

```bash
export BREVIS_SLACK_WEBHOOK='https://hooks.slack.com/services/...'
export BREVIS_UI_URL='https://brevis.example.com'
```

Tanpa webhook, proses memberi peringatan saat boot bahwa kegagalan tidak akan
dilaporkan. Instalasi yang gagal secara diam-diam cenderung ditemukan oleh
pelanggan, bukan oleh tim.

`BREVIS_UI_URL` membangun tautan run di dalam alert — tanpanya, alert menyebutkan
apa yang gagal tetapi membuat pembaca mencari run itu sendiri secara manual.

## Langkah berikutnya

- [Pod per step](/id/docs/pod-per-step/index.md) — di mana setiap step benar-benar berjalan
- [CLI](/id/docs/cli/index.md) — setiap flag `scheduler` dan `backfill`
