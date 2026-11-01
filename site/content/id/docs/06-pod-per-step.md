---
title: Pod per step
description: Mengapa setiap step berjalan di pod-nya sendiri, dan apa yang berubah pada biaya dan isolasi.
group: Konsep
order: 6
slug: pod-per-step
---

Di Kubernetes, **setiap step workflow menjadi sebuah pod** dengan image yang
dideklarasikan di step tersebut. Tidak ada worker generik yang menunggu
pekerjaan: pekerjaan membawa runtime-nya sendiri.

![Diagram: Core memulai satu pod per step — extract di Go, prepare di Python, load di Go, dan transform di dbt. Step saling meneruskan context, dan SDK adalah yang membaca dan menulisnya.](/assets/execution.svg)

```
scheduler                          cluster
─────────                          ───────
walks the graph
  for each ready step:
    builds the Pod  ───────────►  Pod: the step's image, the step's command
    follows status      ◄───────  Pending → Running → Succeeded/Failed
    follows the log     ◄───────  container stdout
    reads the exit code ◄───────  containerStatuses[0].terminated
    deletes the pod ───────────►  (gone)
```

## Apa yang berubah

Worker monolitik memaksa image berisi segala yang mungkin dibutuhkan step mana
pun. Dalam praktiknya:

| | satu image | pod per step |
|---|---|---|
| step dbt | 1,9 GB · 1Gi RAM | 1,9 GB · 1Gi RAM |
| fetcher Go di sebelahnya | **1,9 GB · 1Gi** | **12 MB · 32Mi** |
| menaikkan versi dbt | build ulang semuanya | satu baris di YAML workflow itu |
| step yang bocor memori | menjatuhkan worker beserta tetangganya | mati sendirian |

Baris ketiga paling tidak kentara dan paling mahal dalam keseharian: dengan satu
image, memindahkan dbt dari 1.10 ke 1.11 di satu pipeline memaksa perpindahan itu
di semuanya.

## Image menurut peran

Image ditentukan per **peran**, bukan per proyek. Segala sisa di dalam image
ditarik oleh setiap node yang menjalankan step itu:

| peran | ukuran | cold start |
|---|---|---|
| **Go** | **5,8 MB** | 0,18 d |
| **Python** | **118 MB** | 0,54 d |
| **dbt** | **620 MB** | 3,3 d |

Sebelumnya: satu image 1,87 GB untuk semuanya.

Untuk dbt, `dbt parse` berjalan saat build dan `target/partial_parse.msgpack`
dibawa di dalam image — **lebih cepat 2,68 d per pod**, pada setiap pemanggilan.

## Di mana setiap step berjalan

Keputusan dibuat sekali, saat boot, dan bergantung pada `BREVIS_PODS` serta ada
tidaknya klaster:

| `BREVIS_PODS` | dengan klaster | tanpa klaster |
|---|---|---|
| `auto` (default) | step dengan `image:` menjadi pod | semuanya sebagai proses lokal, dengan peringatan di log |
| `on` | step dengan `image:` menjadi pod | **error boot** |
| `off` | semuanya sebagai proses lokal | semuanya sebagai proses lokal |

**Step dengan `host:` tidak melewati semua ini.** Ia diperiksa sebelum pod dan
sebelum proses, lalu pergi ke mesin yang sudah berjalan — `BREVIS_PODS` tidak
berlaku untuknya, dan ia tidak pernah turun ke proses lokal. Itu disengaja:
`image:` turun ke eksekusi lokal dengan peringatan karena container dan proses
adalah command yang sama dalam dua pembungkus, sedangkan host tidak. Host adalah
tempat lisensi berada, atau GPU, atau datanya.

`auto` menjadi default karena binary yang sama berjalan di kedua tempat: di laptop
tidak ada service account yang di-mount sehingga ia turun ke proses lokal; di
klaster ada, dan ia mulai membuat pod.

`on` ada untuk deployment yang **tidak boleh** diam-diam menjadi eksekusi lokal.
Di sana, ketiadaan klaster harus menjadi error boot.

:::warning `brevis run` tidak membuat pod
Command `run` hanya membangun executor proses. Step dengan `image:` berjalan di
instance itu sendiri — dan menyatakannya. Diam saja akan membuatnya tampak
seolah step berjalan di image yang dideklarasikan, jenis kesalahan yang baru
muncul setelah hasilnya sudah salah.
:::

## Identitas dan kredensial

Identitas yang dipakai pod saat naik adalah keputusan **instalasi**, bukan
keputusan workflow — sebuah pipeline tidak boleh bisa memilih service account
tempat ia berjalan. Maka ia berasal dari environment:

```bash
BREVIS_POD_NAMESPACE=data
BREVIS_POD_SERVICE_ACCOUNT=brevis-runner
BREVIS_POD_PULL_SECRETS=registry-cred
BREVIS_POD_ENV_FROM_SECRETS=bigquery-cred,api-tokens
```

Dalam mode pod, environment task berasal dari Secret klaster — bukan dari
`BREVIS_TASK_ENV`.

## Node selector dan toleration

```bash
BREVIS_POD_NODE_SELECTOR='kubernetes.io/arch=arm64'
BREVIS_POD_TOLERATIONS='dedicated=data:NoSchedule'
```

Hanya operator `Equal` yang diterima pada toleration: `Exists` akan mentoleransi
taint **apa pun** dengan key itu, terlalu luas untuk keputusan yang berasal dari
variabel environment.

Toleration yang salah format **diabaikan** alih-alih dijadikan error boot — ia
membiarkan pod `Pending`, yang terlihat, sedangkan menolak boot juga akan
menghentikan workflow yang tidak membutuhkan pool itu.

## Men-debug kegagalan

```bash
BREVIS_POD_KEEP_ON_FAILURE=true
```

Mempertahankan pod yang gagal untuk `kubectl logs` dan `kubectl describe`.
Biarkan nonaktif di production: pod yang berhenti memakai kuota klaster.

## Langkah berikutnya

- [Kubernetes](/docs/kubernetes/) — deployment lengkap
- [Konfigurasi](/docs/configuration/) — setiap variabel `BREVIS_POD_*`
- [Observabilitas](/docs/observability/) — metrik dari kedua proses
