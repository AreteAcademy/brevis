# Workflows

> Format YAML lengkap — step, dependensi, image, resource, dan jadwal.

*https://brevis.sh/id/docs/workflows/ · brevis.sh docs (id)*

---

Sebuah workflow adalah file YAML. Ia mendeklarasikan **apa yang dijalankan**,
**dalam urutan apa**, dan **dengan runtime apa** — dan tidak lebih dari itu.

## Struktur minimum

```yaml
name: hello
steps:
  - id: only
    run: echo done
```

`name` mengidentifikasi workflow di database dan di antarmuka. `steps` adalah
daftar step, masing-masing dengan `id` yang unik dan sebuah `run`.

### `description` — untuk apa workflow ini

Opsional, bentuk bebas, dan tidak dibaca oleh apa pun di engine:

```yaml
name: daily-orders
description: >
  Pulls yesterday's orders from the vendor and lands them in bronze.
  Runs at 04:00 because the vendor closes its books at 03:30.
steps:
  - id: extract
    run: python fetch.py
```

Ia tampil di bagian atas layar workflow, di atas jumlah dan jadwal. Graf sudah
menjelaskan apa yang **dilakukan** sebuah alur; ini satu-satunya tempat yang
menjelaskan mengapa alur itu ada, dan itulah yang sebenarnya dibutuhkan orang
yang membukanya untuk pertama kali pada pukul tiga pagi.

Workflow tanpa deskripsi tidak menampilkan apa pun di sana — bukan blok kosong,
yang terbaca seperti sesuatu yang gagal dimuat.

## Urutan: chain atau dag

```yaml
type: chain   # order is the file's order
type: dag     # order comes from depends_on
```

`chain` adalah gula sintaksis: parser mengubah urutan itu menjadi edge, dan
**engine hanya mengenal DAG**. Gunakan `dag` ketika urutannya tidak linear:

```yaml
type: dag

steps:
  - id: prepare
    run: ./prepare.sh

  # Siblings: they depend on the same step, so they run in parallel.
  - id: extract
    run: ./extract.sh
    depends_on: [prepare]

  - id: validate
    run: ./validate.sh
    depends_on: [prepare]

  - id: publish
    run: ./publish.sh
    depends_on: [extract, validate]
```

Runner menelusuri graf **per level**: semua yang berada dalam satu level berjalan
paralel, dan level berikutnya baru mulai ketika level sebelumnya selesai
seluruhnya.

:::warning Satu kegagalan menghentikan seluruh level
Runner berhenti pada kegagalan **pertama** dalam sebuah level, tanpa memulai level
berikutnya. Melanjutkan setelah error akan menghasilkan hasil parsial yang
tampak lengkap.
:::

## Jadwal

```yaml
schedule: "0 5 * * *"   # five-field cron
concurrency: 1
```

Tanpa `schedule`, workflow bersifat manual: ia hanya berjalan lewat pemicu di
antarmuka, lewat `brevis run`, atau lewat `backfill`.

`concurrency: 1` membatasi run bersamaan dari workflow yang sama — inilah yang
mencegah `*/15` tumpang tindih dengan dirinya sendiri ketika satu run melewati
lima belas menit.

Dua workflow yang **berbeda** yang menulis tabel yang sama membutuhkan sebuah
**group**. Workflow harian dan workflow `*/15` yang sama-sama membangun model dbt
yang sama masing-masing mendeklarasikan:

```yaml
concurrency:
  group: jaffle_models   # the shared resource, named the same in both files
  limit: 1               # optional; a group without one means 1
```

Run dari setiap workflow dalam group dihitung terhadap satu batas yang sama,
apa pun yang memulainya — jadwal, sebuah landing, sebuah backfill, atau sebuah
klik. Run yang mendapati group penuh **menunggu di antrean**; ia tidak dilewati
dan tidak gagal. Workflow ketiga yang menulis tabel yang sama bergabung dengan
menyebutkan nama group. Selama menunggu, halaman run menyatakannya: group mana,
batasnya, dan run mana yang memegang slot.

- Setiap anggota mendeklarasikan **batas yang sama**. `validate` dan `publish`
  menolak group yang dideklarasikan dengan 1 di satu file dan 3 di file lain,
  termasuk terhadap anggota yang sudah dipublikasikan.
- Sebuah group menyerialisasi **run Brevis**. `dbt run` yang dijalankan seseorang
  di mesinnya sendiri tidak ada di antrean ini, dan gagal dengan cara yang sama.

## Image dan resource

```yaml
image: us-central1-docker.pkg.dev/example/apps/dbt:1.10.3
resources:
  cpu: 200m
  memory: 1Gi
  limits: {memory: 2Gi}
```

Jika dideklarasikan di bagian atas, nilai ini menjadi **default untuk setiap
step**. Setiap step dapat menimpa nilainya sendiri:

```yaml
steps:
  - id: bronze
    run: dbt build --select bronze+          # inherits the top-level image

  - id: notify
    image: ghcr.io/example/notify:0.3        # another runtime
    shell: false                             # distroless has no shell
    run: /notify --channel data
    resources: {cpu: 25m, memory: 32Mi, limits: {memory: 64Mi}}
    depends_on: [bronze]
```

Inilah yang membuat fetcher Go hanya berbiaya 12 MB dan 32Mi di samping
`dbt build` sebesar 1,9 GB, alih-alih keduanya sama-sama menanggung ukuran yang
lebih besar. Lihat [Pod per step](/id/docs/pod-per-step/index.md).

## Field step

| field | | |
|---|---|---|
| `id` | **wajib** | unik dalam workflow; nama yang ditampilkan di graf dan log |
| `run` | | command-nya |
| `image` | | image step; tanpanya, mewarisi image tingkat atas |
| `host` | | kirim command ke mesin yang **tidak** dikelola engine — alternatif untuk `image`, bukan pendampingnya. Lihat [`host:` di panduan Kubernetes](https://github.com/AreteAcademy/brevis/blob/master/docs/KUBERNETES.md#a-third-place-a-step-can-run-host) |
| `shell` | `true` | `false` berjalan tanpa shell — wajib pada distroless |
| `depends_on` | | daftar `id` yang harus selesai lebih dulu; sebuah entri boleh berupa `{step, label}` |
| `marker` | `false` | step tanpa command, untuk `start` atau `end` |
| `resources` | | `cpu`, `memory`, dan `limits` untuk step itu |
| `runtime`, `tools` | | bahasa dan tool milik step — lihat [Runtime step](/id/docs/runtime/index.md) |
| `when` | `all_success` | pada state dependensi seperti apa step ini berjalan — lihat di bawah |
| `unless_empty` | | sebuah key context yang menentukan apakah ada yang perlu dikerjakan — lihat di bawah |
| `for_each` | | sebuah key context berisi list; step berjalan sekali per elemen — lihat di bawah |
| `group` | | menggambar step ini di dalam kotak bernama yang dapat dilipat — lihat di bawah |
| `uses` | | workflow lain yang step-nya menggantikan posisi step ini — lihat di bawah |
| `on_error` | | mengumumkan kegagalan step ini — lihat di bawah |

## Menjalankan step hanya ketika ada yang perlu dikerjakan

```python
# in extract
context.set(has_rows=len(rows) > 0)
```

```yaml
  - id: transform
    run: ./transform.sh
    depends_on: [extract]
    unless_empty: extract.has_rows
```

`unless_empty` menyebut sebuah **key**, bukan ekspresi. Step yang memutuskan dan
mempublikasikan jawabannya; engine membaca satu key dan menanyakan apakah isinya
kosong.

| dihitung kosong | dihitung ada |
|---|---|
| `false`, `0`, `""`, `null`, `[]`, `{}` | semua yang lain |

`"false"` sebagai **string** dihitung ada. Step yang mempublikasikan lima karakter
itu telah mempublikasikan sesuatu, dan menebak bahwa maksudnya boolean adalah
cara sebuah aturan mulai punya opini yang tidak terlihat oleh penulisnya.
Publikasikan boolean yang sebenarnya.

### Mengapa bukan ekspresi

Desain yang menggoda adalah `when: "{{ context.extract.rows > 0 }}"`. Bahasa
ekspresi mini adalah komitmen besar: sebuah parser, sebuah sistem tipe untuk
menjelaskan arti `>` pada JSON `any`, sebuah kisah keamanan karena ekspresi itu
datang dari YAML yang ditulis orang lain, dan pesan error yang menunjuk ke dalam
sebuah string. Setiap orkestrator yang memilikinya punya bug tracker yang penuh
dengan hal itu.

Di sini keputusan berada dalam bahasa yang sudah ditulis oleh pembuatnya, di
tempat framework test miliknya sendiri dapat menjangkaunya.

### Key yang hilang adalah kegagalan, bukan nilai kosong

Jika key tidak ada, step **gagal** dan pesannya menyebut key itu berdampingan
dengan apa yang sebenarnya ditulis oleh publisher:

```
step "transform" reads `extract.has_row`, and "extract" published "has_rows" but not "has_row"
```

Alternatifnya — membaca key yang hilang sebagai "kosong, jadi lewati" — mengubah
typo menjadi step yang berhenti berjalan secara diam-diam dan selamanya, tanpa
ada yang menjelaskan sebabnya. Itu hasil terburuk yang bisa dimiliki fitur ini.

Key selalu dikualifikasikan oleh step yang mempublikasikannya, dan step itu harus
salah satu yang menjadi dependensi step ini. Keduanya ditolak saat **publish**:
step yang digerbangi oleh key yang tidak akan pernah terlihat olehnya akan
dilewati selamanya, dan mengetahuinya ketika job malam berhenti berjalan sudah
terlambat.

## Menjalankan step sekali per elemen

```python
# in extract
context.set(partitions=["2026-01", "2026-02", "2026-03"])
```

```yaml
  - id: load
    run: ./load.sh "$BREVIS_MAP_VALUE"
    depends_on: [extract]
    for_each: extract.partitions
```

Step berjalan sekali per elemen, masing-masing dengan barisnya sendiri, retry
sendiri, dan exit code sendiri. Graf menampilkan **satu node dengan `[3]`**,
bukan tiga node.

| variabel | |
|---|---|
| `BREVIS_MAP_INDEX` | `0`, `1`, `2` … |
| `BREVIS_MAP_VALUE` | elemennya |

String JSON tiba **tanpa tanda kutip**, sehingga `for_each` atas `["2026-01"]`
memberikan `2026-01` ke shell, bukan `"2026-01"`. Selain itu — angka, objek,
list — tiba sebagai JSON-nya.

Tidak satu pun dari kedua variabel itu ada pada step yang tidak di-map. Variabel
yang selalu ada dan selalu kosong mengajari siapa pun yang membaca environment
untuk mengabaikannya.

### Bentuk DAG tidak berubah

Step yang di-map tetap satu node dengan satu set edge. Hanya jumlah baris di
bawahnya yang bervariasi, sehingga layout-nya tetap seperti biasa dan `[3]`
dihitung dari baris-baris itu saat dibaca — tidak ada yang disimpan, jadi tidak
ada yang harus disinkronkan dan hitungan yang salah tidak bisa bertahan lebih
lama dari query yang sudah diperbaiki.

Selama instance masih berjalan, kartu menampilkan `[2/3]`.

### Satu instance gagal membuat step gagal

Tiga partisi dimuat dan satu tidak berarti step itu tidak menyelesaikan
tugasnya, dan step di bawahnya melihat hal itu. Node yang menjadi hijau karena
sebagian besar berhasil adalah lencana yang berbohong.

**Retry hanya mengulang instance yang gagal.** Delapan belas partisi yang
berhasil tidak dimuat ulang hanya karena dua yang rusak.

### List kosong adalah `skipped`, bukan sukses

Step yang tidak melakukan apa-apa karena memang tidak ada yang dikerjakan tidak
berhasil mengerjakannya. Node hijau di atas nol instance persis jenis hal yang
dijadikan dasar dashboard oleh seseorang.

Nilai yang **bukan list** gagal, begitu pula key yang tidak ada — kebijakan yang
sama seperti `unless_empty:`, dengan alasan yang sama: typo yang diam-diam
menghasilkan nol instance akan menonaktifkan step selamanya.

### Fan-out dibatasi, dan batasnya diwarisi

List itu berjalan di context, dan context memiliki batas 4096 byte yang berasal
dari kubelet. Sebuah workflow tidak bisa meminta sepuluh ribu pod tanpa lebih
dulu menemukan cara menyatakannya dalam empat kilobyte. Itu batas yang layak
dipertahankan, bukan dicari jalan pintasnya.

### Apa yang dipublikasikan step yang di-map

Outputnya dicatat pada baris setiap instance dan **tidak** terlihat oleh step di
bawahnya. Empat instance yang mempublikasikan di bawah nama satu step berarti
empat nilai untuk satu key, dan tidak ada jawaban atas
`context.String("load.bucket")` yang bukan tebakan. Step menyatakannya di log-nya
alih-alih membuangnya begitu saja.

## Mengelompokkan step di graf

```yaml
steps:
  - id: extract_orders
    group: sales_data_reporting
    run: ./extract.sh
  - id: load_orders
    group: sales_data_reporting
    depends_on: [extract_orders]
    run: ./load.sh
```

Step digambar di dalam kotak bernama yang dapat dilipat — TaskGroup milik
Airflow, untuk kasus ketika sebuah DAG menjadi cukup besar sehingga tidak lagi
terbaca.

**Hanya visual.** TaskGroup Airflow juga *memberi prefiks* pada id di dalamnya,
sehingga `extract` menjadi `sales.extract`. Di sini tidak: pemberian prefiks akan
mengubah setiap `depends_on`, setiap key context, dan setiap baris tercatat pada
workflow yang sudah ada, demi fitur yang seluruh nilainya adalah graf besar
terbaca lebih baik. Namespacing dapat ditambahkan nanti — itu penambahan murni
atas ini.

Group adalah **label**, bukan container. Step mempertahankan id globalnya,
sebuah group boleh melintasi beberapa level, dan tidak ada yang berubah pada
eksekusi.

Mengklik nama group melipatnya: step-nya menghilang dan panah yang melintasi
batas menunjuk ke kotaknya.

## Memakai ulang workflow lain

```yaml
# nightly.yaml
steps:
  - id: prepare
    run: ./prepare.sh

  - id: mlops
    uses: ml_training       # another workflow in the same publish
    depends_on: [prepare]

  - id: report
    run: ./report.sh
    depends_on: [mlops]
```

Saat **publish**, step milik `ml_training` mengambil tempat step itu, diberi
prefiks id-nya, dan node `uses` menghilang:

```
prepare → mlops.train → mlops.evaluate → report
```

Step-step itu tiba sebagai satu group yang dapat dilipat, sehingga graf
menampilkan apa yang dikatakan file.

**Diekspansi saat publish, tidak dijalankan saat run.** Step yang memicu *run*
anak lalu menunggunya adalah desain yang membuat Airflow deadlock, dan engine ini
punya bahan yang sama: batas pod adalah semaphore per proses, sehingga induk yang
memegang slot sambil menunggu anak yang membutuhkan slot dari pool yang sama akan
hang — dan hanya di bawah beban, yaitu di production. Satu run, satu graf, satu
pool.

Yang dikorbankan adalah run anak dengan id dan riwayatnya sendiri.

### Aturannya

| | |
|---|---|
| anak harus berada di **publish yang sama** | bukan sekadar sudah dipublikasikan |
| panah | yang masuk ke step menjadi panah yang masuk ke setiap **root** anak; yang keluar darinya, keluar dari setiap **leaf**-nya |
| `image`, `env`, `secrets`, `resources` milik anak | dimaterialisasi pada setiap step, sehingga berjalan di apa yang dikatakan file-nya sendiri |
| key `unless_empty` / `for_each` milik anak | ikut berpindah bersama prefiks |
| nesting | diratakan, depth first |
| sirkular | ditolak saat publish, dengan menyebut rantainya |

**Publish yang sama, dan bukan "sudah dipublikasikan", adalah yang terpenting.**
Ekspansi yang membaca database akan membuat `brevis validate` — yang tidak
menyentuh database, dengan sengaja — menjawab pertanyaan yang berbeda dari
`brevis publish`, dan file yang lolos CI akan menjadi file yang gagal saat
deploy.

Anak tetap dapat dipublikasikan sendiri: ekspansi **menyalin**, tidak
mengonsumsi. Sebuah step tidak boleh sekaligus `uses:` dan mendeklarasikan
`run:`, `action:`, atau `marker:` — itu file yang mengatakan dua hal.

## Menyatakan arti sebuah panah

Sebuah dependensi dapat membawa label, yang ditampilkan pada panah di graf.

```yaml
steps:
  - id: determine_load_type
    run: ./decide.sh

  - id: load_full
    run: ./full.sh
    depends_on:
      - {step: determine_load_type, label: additional data}

  - id: load_delta
    run: ./delta.sh
    depends_on:
      - step: determine_load_type
        label: changed existing data
```

Bentuk polos — `depends_on: [extract]` — tetap berfungsi dan tetap menjadi
bentuk normal. Sebagian besar dependensi tidak punya apa-apa untuk dikatakan,
dan label pada setiap panah hanyalah noise.

Label layak ada pada sebuah **percabangan**: dua panah yang keluar dari step yang
sama tanpa tulisan apa pun adalah diagram yang mengharuskan membuka source untuk
membacanya, padahal itulah satu hal yang justru ingin dihindari oleh sebuah graf.

## Step yang tidak melakukan apa-apa

```yaml
steps:
  - id: start
    marker: true

  - id: end
    marker: true
    depends_on: [load_full, load_delta, report]
```

`marker: true` adalah step tanpa command. Ia tidak menjalankan apa pun, berhasil
seketika, dan muncul di graf.

Ini bukan dekorasi. Sebuah `end` yang bergantung pada setiap cabang mengubah
pertanyaan *apakah semuanya selesai?* menjadi satu node, bukan enam panah yang
harus diikuti — dan ia berperilaku seperti step lain, sehingga `end` di bawah
cabang yang gagal **dilewati**, bukan hijau.

**Step tanpa `run:` dan tanpa `marker: true` tetap ditolak.** Kedua kasus itu
tidak boleh bercampur: command kosong hampir selalu sebuah kesalahan, dan
`marker: true` adalah cara seseorang menyatakan bahwa ia memang bermaksud
demikian. Marker yang juga mendeklarasikan `run:` juga ditolak — itu file yang
mengatakan dua hal.

## Menjalankan step hanya ketika ada yang gagal

Secara default, step berjalan ketika semua yang ada sebelumnya berhasil. `when:`
mengubahnya.

```yaml
steps:
  - id: extract
    run: python fetch.py

  - id: notify_failure
    run: ./notify.sh
    depends_on: [extract]
    when: any_failed          # runs precisely when extract did not make it

  - id: cleanup
    run: ./cleanup.sh
    depends_on: [extract, transform]
    when: all_done            # runs either way
```

| aturan | |
|---|---|
| `all_success` | default. Semua yang menjadi dependensi step ini berhasil — dependensinya **sendiri**, seperti di Airflow |
| `any_failed` | setidaknya satu step yang menjadi dependensi step ini gagal |
| `all_done` | semua yang menjadi dependensi step ini sudah selesai, apa pun akhirnya |

Aturan yang tidak dikenal ditolak saat publish, dengan menyebutkan apa yang
valid. Harus demikian: `when: on_failure` yang dibaca sebagai "default" akan
berjalan saat **sukses** — kebalikan dari yang tertulis, dan baru ketahuan pada
malam yang menentukan.

### Step yang tidak berjalan berstatus `skipped`

Ini state tersendiri, digambar dengan warnanya sendiri, dan membawa alasannya:
*`extract` berstatus failed*. Sebelum ini, step di bawah sebuah kegagalan tidak
punya catatan sama sekali dan layar menampilkannya pending selamanya.

`skipped` bukan kegagalan dan bukan sukses. Step yang upstream-nya rusak tidak
gagal — ia tidak pernah diberi kesempatan — dan menyebutnya sukses adalah
kebohongan yang sampai ke status run itu sendiri.

Trigger rule menentukan step mana yang **berjalan**. Ia tidak menentukan hasil
run: `notify_failure` yang berhasil mengirim pesannya tidak berarti pipeline
bekerja, dan run tetap berstatus failed.

### `all_success` bersifat lokal, seperti di Airflow

Aturan ini menanyakan dependensi **milik** step itu sendiri dan tidak yang lain.
Kegagalan di cabang yang tidak terkait tidak menghentikan yang ini:

```
extract_orders ──✕                 (failed)
extract_users  ──✓── transform_users ──✓     keeps going
```

Cabang yang berada **di bawah** kegagalan tetap berhenti — `all_success` yang
bersifat lokal tidak berarti ia tidak ada.

Engine ini dulu membatalkan seluruh graf pada kegagalan pertama. Itu ada
alasannya: melanjutkan setelah error menghasilkan hasil parsial yang tampak
lengkap, dan sebuah pipeline berjalan terlambat 28 hari tanpa ada yang menyadari.

**Apa yang menggantikan perlindungan itu**, dan ini bukan tidak ada:

- run tetap **gagal**, dengan error yang sama;
- graf menampilkan step yang gagal dengan warna merah dan setiap step yang
  dilewati dengan warnanya sendiri, masing-masing menyebutkan step mana yang
  menghentikannya;
- alert tetap terkirim ketika run menyerah.

Hasil parsial tidak lagi tampak lengkap, karena run menyatakan bahwa ia tidak
lengkap.

**Apa yang dikorbankan**, dinyatakan terus terang: cabang yang tidak terkait
kini menulis datanya pada run yang gagal di tempat lain.

### Retry hanya menjalankan ulang yang gagal

Retry sebuah run tidak mengulang seluruh graf. Step yang sudah berhasil pada
percobaan sebelumnya dari **run yang sama** mempertahankan hasilnya dan tidak
dijalankan lagi — seperti perilaku DAG run yang di-clear di Airflow.

```
attempt 1   extract ✓    load ✕    report (skipped)
attempt 2   extract –    load ✓    report ✓            extract is not re-run
```

Ia mempertahankan barisnya yang asli: durasi, log, dan apa yang dipublikasikannya
adalah milik percobaan pertama, karena di situlah pekerjaan terjadi. **Step di
bawahnya tetap membaca apa yang dipublikasikannya** — context run disimpan, bukan
dibangun ulang.

Pembedaan yang penting: ini tentang percobaan sebelumnya dari **run ini**. Step
yang berhasil pada run *kemarin* berjalan normal hari ini. (Pertanyaan lain itu
juga ada, dan itulah yang memberi tahu SDK apakah ini kali pertama sebuah step —
lihat `BREVIS_RUN_FIRST`.)

Step yang **dilewati** belum selesai: ia tidak pernah berjalan, dan retry
memutuskannya lagi dengan hasil percobaan yang baru.

## Mengumumkan kegagalan sebuah step

Setiap kegagalan sudah diumumkan: dengan webhook terkonfigurasi, run yang
menyerah mengirim satu pesan, tanpa blok yang diulang di file mana pun.
`on_error` ditujukan untuk step yang membutuhkan pengumumannya sendiri.

```yaml
steps:
  - id: fetch_observations
    run: python fetch.py
    on_error:
      type: SLACK
```

| field | | |
|---|---|---|
| `type` | **wajib** | `SLACK`. Nilai yang tidak dikenal ditolak saat publish, dengan menyebutkan apa yang valid |
| `when` | `give_up` | `attempt` mengumumkan setiap percobaan yang gagal, bukan hanya yang terakhir |

Dua pesan tiba, bukan satu, dan itulah intinya: alert tingkat run mengatakan
*`id_verification` gagal*, yang dibutuhkan oleh pemilik pipeline; alert tingkat
step mengatakan *`fetch_observations` gagal*, yang dibutuhkan oleh pemilik
integrasi itu.

**Tidak ada field `webhook` atau `url`, dan tidak akan ada.** Tujuannya adalah
kredensial — siapa pun yang memegangnya memposting di channel seolah-olah ia
platform — dan file workflow ditulis oleh seseorang yang belum tentu berhak
memilih ke mana alert perusahaan dikirim. File menyatakan **apakah** dan
**bagaimana**; instalasi menyatakan **ke mana**, lewat `BREVIS_SLACK_WEBHOOK`.

**`when: give_up` menjadi default karena soal malam siapa yang terganggu.** Step
yang gagal dua kali lalu lolos pada percobaan ketiga akan mengirim dua pesan di
bawah default yang lain, dan pesan kedua tiba setelah masalahnya hilang.

**Step yang tidak gagal tidak pernah diumumkan.** Dalam workflow ketika satu
cabang rusak, pemilik cabang lainnya tidak dibangunkan.

Pengiriman adalah tugas `brevis alert` — alert dicatat dalam transaksi yang sama
dengan kegagalan, sehingga gangguan Slack hanya menundanya, bukan menghilangkannya.

## Tag

```yaml
tags: [analytics, dbt, daily]
```

Tag memfilter di antarmuka. Tag tidak memengaruhi eksekusi.

## Validasi

Validasi tidak membutuhkan database, sehingga berjalan di CI bersama test:

```bash
brevis validate workflows/
```

```
  ok    daily_analytics              dag  5 steps, 5 dependencias  (manual)
  ok    daily-report                 chain  3 steps, 2 dependencias  cron 0 2 * * *
```

Ia menerima file atau direktori. Dalam direktori, ia mencocokkan `*.y*ml` dan
mengurutkan — dua run menghasilkan log yang sama, dan selisih antara dua deploy
tidak menjadi noise.

## Publikasi

```bash
brevis publish workflows/
brevis publish workflows/ --project acme --prune
```

`--prune` menghapus dari project workflow yang tidak ada dalam daftar yang
dipublikasikan, dengan mempertahankan riwayatnya. Ini **bukan** default, karena
alasan praktis: `publish one-file.yaml` tidak boleh menghapus empat puluh delapan
lainnya hanya karena tidak disebut di command line.

## Langkah berikutnya

- [Parameter](/id/docs/parameters/index.md) — apa yang berubah di antara dua run
- [Scheduler dan antrean](/id/docs/scheduler-and-queue/index.md) — bagaimana workflow menjadi eksekusi
- [Konteks antar step](/id/docs/context/index.md) — apa yang dikatakan satu step kepada step berikutnya
