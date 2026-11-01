---
title: Pengantar
description: Apa itu brevis.sh, masalah yang diselesaikannya, dan kapan ia bukan pilihan yang tepat.
group: Memulai
order: 1
slug: introduction
---

brevis.sh adalah **runtime orkestrasi data yang ditulis dalam Go**. Satu binary
menyatukan apa yang biasanya membutuhkan empat tool: transformasi deklaratif,
orkestrasi workflow, antrean persisten, scheduler, dan antarmuka untuk
mengoperasikan semuanya.

Proyek ini bersumber terbuka dengan lisensi MIT, dan lahir di
[Aretê Academy](https://areteacademy.com.br/).

## Masalahnya

Stack data yang umum menggabungkan tool transformasi, orkestrator, antrean, dan
antarmuka. Setiap bagian bagus pada tugasnya — dan integrasi di antara mereka
menjadi pekerjaan permanen: empat skema versi, empat cara konfigurasi, empat
tempat untuk memeriksa ketika sesuatu gagal.

Biaya itu tidak pernah muncul di diagram arsitektur. Ia muncul pada waktu yang
dihabiskan tim untuk merawat perekat, alih-alih mengerjakan domain yang hanya
mereka pahami.

## Usulannya

Satu artefak, dengan subcommand yang dibutuhkan oleh perannya:

```bash
brevis serve        # API + interface
brevis scheduler    # materialises schedules and drains the queue
brevis run wf.yaml  # runs now, no database
```

Sebuah workflow adalah file YAML yang diberi versi. Mereview sebuah pipeline
berarti mereview sebuah diff, bukan sesi klik-klik.

```yaml
name: ingest
schedule: "0 5 * * *"

steps:
  - id: bronze
    image: registry.example/dbt:1.10.3
    run: dbt build --select bronze+

  - id: notify
    image: ghcr.io/example/notify:0.3
    run: /notify --channel data
    depends_on: [bronze]
```

## Apa yang membuatnya berbeda

**Setiap step berjalan di pod-nya sendiri, dengan image-nya sendiri.** Tidak ada
worker generik yang menunggu pekerjaan: pekerjaan membawa runtime-nya sendiri.
Step `dbt` naik dengan image dbt; fetcher Go di sebelahnya naik dengan 5,8 MB dan
32Mi memori, alih-alih mewarisi ukuran tetangganya yang terbesar.

**Scheduler membuat run; antrean mengeksekusinya.** Dua loop independen, dan
pemisahan itu disengaja: yang satu bisa mati tanpa mengganggu yang lain, dan
memproses ulang bulan lalu tidak bergantung pada jam.

**Validasi tidak membutuhkan database.** `brevis validate` berjalan di CI
bersama test, sehingga kesalahan YAML gagal di pull request dan bukan di
klaster.

## Tiga bagian, sengaja dipisah

| bagian | apa itu | versi |
|---|---|---|
| **engine** | `brevis`, binary yang dibahas halaman ini: mengorkestrasi, menjadwalkan, mengeksekusi, dan mengoperasikan | `0.15` |
| **SDK** | modul Go untuk menulis step yang mengekstrak dan memuat | `0.66` |
| **gateway** | endpoint HTTP yang mendaratkan data yang *datang*, alih-alih pergi mengambilnya | `0.8` |

Versinya independen karena tingkat kematangannya memang berbeda. Menomori gateway
`0.15.3` hanya karena engine ada di sana akan mengklaim kematangan yang tidak
dimilikinya, dan sebuah versi tidak bisa ditarik kembali setelah dipublikasikan.

Yang menyatukan ketiganya adalah sebuah angka: `ingestion_id`, UUID v5 yang
dibekukan atas `provider|entity|source_key|record_ts`. Baris yang didaratkan
[gateway](/docs/ingestion/) dan baris yang didaratkan fetcher [SDK](/docs/sdk/)
untuk record yang sama adalah *satu baris*, tanpa rekonsiliasi di antara
keduanya.

## Kapan brevis.sh bukan pilihan

Mendokumentasikan apa yang tidak dilakukan sebuah tool menghemat lebih banyak
waktu daripada mendokumentasikan apa yang dilakukannya:

- **Anda sudah menjalankan Airflow atau Dagster dan puas.** Migrasi lebih mahal
  daripada penghematan mengoperasikan satu binary lebih sedikit.
- **Workflow Anda bukan tentang data.** brevis.sh mengasumsikan DAG transformasi
  dan load; orkestrator serba guna lebih cocok untuk Anda.
- **Anda butuh katalog operator siap pakai.** Di sini sebuah step adalah image
  dan sebuah command. Itu sederhana, dan terbatas — tidak ada ratusan integrasi
  yang tinggal diseret masuk.
- **Tim Anda tidak memakai Kubernetes dan tidak berniat memakainya.** Mode lokal
  berfungsi, tetapi desainnya mengasumsikan pod.

## Kode terbuka. Pilih cara Anda ikut serta.

brevis.sh 100% sumber terbuka. Tanpa lisensi proprietary, tanpa edisi terbatas, tanpa pembatasan pada apa yang Anda bangun.

### 100% terbuka

*kode · lisensi sumber terbuka*

Baca, gunakan, adaptasi, dan tingkatkan runtime di operasi Anda sendiri. Runtime lengkap · Kode yang dapat diaudit · Kontribusi terbuka.

[Lihat repositori](https://github.com/AreteAcademy/brevis)

### Membangun bersama

*komunitas · issue terbuka* · **Jalur utama**

Ikut dalam percakapan yang menentukan akan menjadi apa proyek ini. Issue dan proposal · Pertukaran antar rekan · Roadmap publik.

[Ikuti proyek](https://github.com/AreteAcademy/brevis/issues)

### Belajar mendalam

*aretê · teknologi dengan tujuan*

Kenali sekolah yang menghubungkan pemikiran filosofis dan rekayasa perangkat lunak. Konten orisinal · Pembelajaran berkelanjutan · Kanal terbuka.

[Kunjungi Aretê](https://areteacademy.com.br/)

## Ke mana selanjutnya

| jika Anda ingin | buka |
|---|---|
| memasang dan melihatnya berjalan | [Instalasi](/docs/installation/) dan [Quickstart](/docs/quickstart/) |
| memahami format workflow | [Workflows](/docs/workflows/) |
| memahami arsitekturnya | [Scheduler dan antrean](/docs/scheduler-and-queue/) |
| daftar command dan flag | [CLI](/docs/cli/) |
| menulis fetcher dalam Go | [SDK](/docs/sdk/) |
| meneruskan nilai dari satu step ke step berikutnya | [Konteks antar step](/docs/context/) |
| menulis step dalam Python | [Pustaka klien](/docs/libraries/) |
| menerima data lewat HTTP alih-alih mengambilnya | [Ingestion](/docs/ingestion/) |
| membangun dashboard dan alert | [Observabilitas](/docs/observability/) |

:::note Tentang nama
*Brevis* dalam bahasa Latin berarti "pendek, singkat" — akar kata *brevity*. Nama
ini berasal dari **De Brevitate Vitae** karya Seneca: bukan karena kita punya
sedikit waktu, melainkan karena kita menyia-nyiakan banyak.
:::
