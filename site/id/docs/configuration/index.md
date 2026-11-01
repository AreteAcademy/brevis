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
| `BREVIS_ENV` | `production` | `local` mencatat log dalam teks dan membiarkan antarmuka terbuka; selain itu, termasuk bila tidak diset, membutuhkan kredensial dan mencatat log JSON. Set `BREVIS_ENV=local` untuk pengembangan |
| `BREVIS_HTTP_ADDR` | `:8080` | alamat listen |
| `BREVIS_LOG_LEVEL` | `info` | |
| `BREVIS_SHUTDOWN_TIMEOUT_SECONDS` | `15` | integer; nilai non-numerik adalah error boot |
| `BREVIS_DB_MAX_CONNS` | default driver, `max(4, CPUs)` | batas pool Postgres di tiap proses; nilai non-numerik atau negatif adalah error boot |
| `BREVIS_CONSOLE_STATEMENT_TIMEOUT_SECONDS` | `0` (mati) | hanya konsol (`brevis serve`): Postgres membatalkan statement-nya yang berjalan lebih lama, sehingga query daftar yang lepas kendali tidak menahan koneksi. `30` adalah nilai yang direkomendasikan di mana pun pool menerima parameter startup; mati secara default karena PgBouncer dalam mode transaction akan menolak koneksi jika tidak. Dikirim sebagai parameter startup koneksi, jadi PgBouncer dalam mode transaction memerlukan `ignore_startup_parameters = statement_timeout` |
| `BREVIS_RETENTION_TRIM_DAYS` | `0` (mati) | prune terjadwal opt-in, dijalankan oleh `brevis scheduler`: mengosongkan log, fase, dan output run yang lebih lama dari nilai ini. Efeknya sama dengan `brevis prune --trim-after`. `0` = scheduler tidak pernah melakukan prune |
| `BREVIS_RETENTION_PURGE_DAYS` | `0` (tidak pernah) | bersama nilai di atas, juga menghapus run yang lebih lama dari nilai ini. Harus minimal sama dengan nilai trim |
| `BREVIS_RETENTION_INTERVAL_HOURS` | `24` | seberapa sering prune terjadwal berjalan (sekali saat start, lalu setiap N jam) |
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

| variabel | default | |
|---|---|---|
| `BREVIS_AUTH_CLIENT_IP_HEADER` | — | header yang diisi proxy Anda dengan alamat klien, mis. `X-Forwarded-For`. Entri **terakhir** yang dipakai (yang ditambahkan proxy Anda). Kosong = alamat koneksi |

Sign-in dibatasi per alamat klien (IPv6 berdasarkan /64-nya): setelah tiga kegagalan, alamat dikunci 2 detik, lalu menggandakan hingga 15 menit. **Di belakang ingress atau load balancer, set variabel ini** (Helm chart melakukannya saat `api.ingress.enabled` aktif; timpa dengan `auth.clientIPHeader`): tanpanya semua klien berbagi alamat proxy, sehingga satu orang yang menebak password akan mengunci semua orang. Dalam kasus itu, di luar `BREVIS_ENV=local`, penguncian dibatasi 30 detik dan peringatan dicatat di log saat boot. Set variabel ini hanya jika header ditulis oleh proxy yang Anda percaya dan tidak dapat dipalsukan klien. Hanya satu pengecekan password yang berjalan per alamat pada satu waktu, jadi dua orang di belakang satu NAT yang sign-in pada detik yang sama mungkin melihat 429 sesaat.

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
