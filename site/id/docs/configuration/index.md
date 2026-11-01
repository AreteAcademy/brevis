# Konfigurasi

> Setiap variabel environment, dengan default, format, dan apa yang terjadi bila hilang.

*https://brevis.sh/id/docs/configuration/ · brevis.sh docs (id)*

---

Semua konfigurasi berasal dari **environment**, dibaca sekali saat boot. Tidak
ada yang menanyakan environment sesudahnya: proses yang konfigurasinya berubah di
tengah jalan adalah proses yang perilakunya tidak dapat direproduksi siapa pun.

## Wajib

| variabel | |
|---|---|
| `BREVIS_DATABASE_URL` | Postgres. Hilang = **error boot** pada lima subcommand yang memakai database |

## Proses

| variabel | default | |
|---|---|---|
| `BREVIS_ENV` | `local` | `local` mencatat log dalam teks dan membiarkan antarmuka terbuka; selain itu membutuhkan kredensial dan mencatat log JSON |
| `BREVIS_HTTP_ADDR` | `:8080` | alamat listen |
| `BREVIS_LOG_LEVEL` | `info` | |
| `BREVIS_SHUTDOWN_TIMEOUT_SECONDS` | `15` | integer; nilai non-numerik adalah error boot |
| `BREVIS_BRAND_FILE` | `brand.yaml` | branding; tidak ada = default |
| `BREVIS_UI_URL` | — | basis tautan run di alert |
| `BREVIS_METRICS_ADDR` | — | port metrik Prometheus; kosong = tidak diekspos. Lihat [Observabilitas](/id/docs/observability/index.md) |
| `BREVIS_SLACK_WEBHOOK` | — | tujuan alert kegagalan. Kosong = tidak ada yang diberi tahu |

## Autentikasi

| variabel | |
|---|---|
| `BREVIS_AUTH_USER` | pengguna antarmuka |
| `BREVIS_AUTH_PASSWORD_HASH` | hash `pbkdf2-sha256$…`, dibuat oleh `brevis hash` |
| `BREVIS_AUTH_SECRET` | 32+ byte untuk menandatangani sesi (`openssl rand -base64 48`) |

Ketiganya datang bersama, atau tidak sama sekali.

:::danger Setengah terkonfigurasi adalah error boot
Siapa pun yang mengisi nama pengguna percaya pintunya tertutup. Antarmuka
memicu pipeline: `POST /workflows/<slug>/trigger` menjalankan `dbt build` yang
menulis ke warehouse. Terbuka di internet, ia menjadi remote control untuk
warehouse — dan peringatan di log tidak meruntuhkan keyakinan itu, karena tidak
ada yang membaca log proses yang berfungsi.
:::

Di luar `BREVIS_ENV=local`, naik tanpa kredensial ditolak:

```
error: BREVIS_ENV=production requires a credential: set BREVIS_AUTH_USER,
BREVIS_AUTH_PASSWORD_HASH (generate one with `brevis hash`) and BREVIS_AUTH_SECRET
```

:::note Nama lama masih berfungsi
Empat variabel diganti namanya pada 2026-09-10, ketika nama-nama berbahasa
Portugis terakhir meninggalkan sisi operator proyek ini: `BREVIS_AUTH_USUARIO`,
`BREVIS_AUTH_SENHA_HASH`, `BREVIS_AUTH_SEGREDO`, dan
`BREVIS_POD_MANTER_EM_FALHA`. Instalasi yang mengatur nama lama tetap berfungsi
dan mencatat peringatan yang menyebut nama baru.

Tambahkan nama baru, deploy, lalu hapus nama lama — nama baru menang bila
keduanya diatur. Nama lama akan hilang pada versi mayor.

Di SDK, `BREVIS_SDK_LIMITE_INLINE` menjadi `BREVIS_SDK_INLINE_LIMIT` dengan aturan
yang sama.
:::

## Environment task

| variabel | default | |
|---|---|---|
| `BREVIS_TASK_ENV` | kosong | apa yang diterima setiap step |

Sebuah task **tidak** mewarisi environment orkestrator. Alasannya konkret:
proses membawa `BREVIS_DATABASE_URL` dengan username dan password Postgres, dan
workflow adalah command sembarang yang ditulis orang lain — mewarisi berarti
menyerahkan kredensial database ke setiap step dari setiap pipeline.

Apa yang dibutuhkan task dideklarasikan:

```bash
BREVIS_TASK_ENV=GOOGLE_PROJECT_ID,STAGE   # forwards these two from the process
BREVIS_TASK_ENV=STAGE=prod                # sets a literal
BREVIS_TASK_ENV='*'                       # forwards everything EXCEPT BREVIS_*
```

`PATH` dan `HOME` selalu disertakan — tanpa `PATH` tidak ada command yang
terselesaikan, dan errornya akan berupa "not found" yang tidak menjelaskan apa-apa.

Nama yang hilang **tidak** menjadi string kosong: `GOOGLE_PROJECT_ID=""` akan
membuat dbt gagal dengan pesan yang lebih buruk daripada variabel yang hilang.

## Kubernetes

Keputusan **instalasi**, bukan keputusan workflow.

| variabel | default | format |
|---|---|---|
| `BREVIS_PODS` | `auto` | `auto`, `on`, atau `off`; selain itu adalah error boot |
| `BREVIS_POD_NAMESPACE` | milik klaster | |
| `BREVIS_POD_SERVICE_ACCOUNT` | — | |
| `BREVIS_POD_PULL_SECRETS` | — | daftar dipisahkan koma |
| `BREVIS_POD_ENV_FROM_SECRETS` | — | daftar dipisahkan koma |
| `BREVIS_POD_ENV_FROM_CONFIGMAPS` | — | daftar dipisahkan koma |
| `BREVIS_POD_ALLOWED_SECRETS` | — | daftar dipisahkan koma |
| `BREVIS_POD_CREDENTIAL_PATH` | — | path yang di-mount di pod |
| `BREVIS_POD_CREDENTIAL_PVC` | — | PVC yang menyimpan kredensial |
| `BREVIS_POD_NODE_SELECTOR` | — | `key=value,other=value` |
| `BREVIS_POD_TOLERATIONS` | — | `key=value:effect,…`; hanya operator `Equal` |
| `BREVIS_POD_KEEP_ON_FAILURE` | `false` | `true` mempertahankan pod untuk diperiksa |

Dalam daftar, entri kosong diabaikan: `a,,b` adalah typo, dan nama secret yang
kosong akan membuat server menolak seluruh pod.

## Test

| variabel | |
|---|---|
| `BREVIS_TEST_DATABASE_URL` | database untuk test integrasi; tanpanya, test dilewati |

## Endpoint health

| | |
|---|---|
| `GET /health` | liveness — **tidak** mengquery database |
| `GET /ready` | readiness — mengquery, dan menyebut dependensi yang gagal |

Pemisahan ini disengaja: liveness probe yang bergantung pada layanan eksternal
membuat Kubernetes **membunuh pod** ketika database goyah, alih-alih sekadar
mengeluarkannya dari load balancer.

## Contoh lengkap

```bash
# required
export BREVIS_DATABASE_URL='postgres://brevis:secret@db:5432/brevis?sslmode=require'

# production requires credentials
export BREVIS_ENV=production
export BREVIS_AUTH_USER=operator
export BREVIS_AUTH_PASSWORD_HASH="$(brevis hash < password.txt)"
export BREVIS_AUTH_SECRET="$(openssl rand -base64 48)"

# what the tasks need, and nothing more
export BREVIS_TASK_ENV=GOOGLE_PROJECT_ID,STAGE,DBT_KEYFILE

# pods
export BREVIS_PODS=on
export BREVIS_POD_NAMESPACE=data
export BREVIS_POD_SERVICE_ACCOUNT=brevis-runner
export BREVIS_POD_ENV_FROM_SECRETS=bigquery-cred

# alerts
export BREVIS_SLACK_WEBHOOK='https://hooks.slack.com/services/...'
export BREVIS_UI_URL='https://brevis.example.com'
```
