# Kubernetes

> Cara men-deploy engine, dengan izin apa, dan apa yang diperiksa sesudahnya.

*https://brevis.sh/id/docs/kubernetes/ · brevis.sh docs (id)*

---

Instalasi tipikal memiliki **dua Deployment** dari binary yang sama — API dan
scheduler — ditambah sebuah Postgres.

## Pasang dengan Helm

Chart berada di repositori, di `deployments/helm/brevis`. Empat nilai wajib
diisi dan yang lainnya punya default yang berfungsi:

```bash
helm install brevis ./deployments/helm/brevis \
  --namespace data --create-namespace \
  --set database.url="postgres://brevis:pw@postgres/brevis?sslmode=require" \
  --set auth.user=admin \
  --set auth.passwordHash="$(brevis hash)" \
  --set auth.secret="$(openssl rand -base64 48)"
```

Itu seluruh instalasinya: migrasi berjalan lebih dulu sebagai hook, kedua
Deployment naik dengan image yang tepat masing-masing, dan scheduler mendapat
ServiceAccount yang boleh membuat pod sementara pod task mendapat yang tidak boleh
melakukan apa-apa.

Untuk menjangkau antarmuka:

```bash
helm upgrade brevis ./deployments/helm/brevis --reuse-values \
  --set api.ingress.enabled=true \
  --set api.ingress.host=brevis.example.com \
  --set api.ingress.tls.enabled=true \
  --set api.ingress.tls.secretName=brevis-tls
```

Di production, jauhkan connection string dari release: taruh di Secret dan
berikan `--set database.existingSecret=brevis-db`. Jika diberikan inline, ia
masuk ke manifest release yang tersimpan, yang dapat dibaca siapa pun yang bisa
membaca Secret di namespace itu.

`helm show values ./deployments/helm/brevis` mendaftar setiap nilai dengan
alasannya di sampingnya.

### Apa yang ditolak chart

Apa pun yang tidak dapat dibuatnya aman gagal saat render, dengan pesan yang
menyebutkan apa yang akan rusak — bukan pada pukul 3 pagi, dalam run yang
menghasilkan baris duplikat:

| ditolak | karena |
|---|---|
| `scheduler.replicas` sama sekali | dua replika akan mematerialisasi slot yang sama. `FOR UPDATE SKIP LOCKED` mencegah keduanya mengklaim item antrean yang sama; tidak ada yang mencegah keduanya membuat run terjadwal yang sama, dan `dbt build` akan berjalan dua kali atas satu jendela tanpa ada yang gagal |
| kredensial yang hilang di luar `local` | engine menolak boot tanpanya, sehingga chart akan memberi Anda CrashLoopBackOff |
| `auth.secret` di bawah 32 byte | ia menandatangani cookie sesi, dan engine menegakkan minimum yang sama |
| Ingress tanpa `host` | ia akan cocok dengan setiap request yang mencapai controller — di klaster bersama, menjawab untuk hostname orang lain |
| `alerts.enabled` tanpa webhook | pod akan menguras outbox dan mengirim ke mana-mana, yang lebih buruk daripada tidak berjalan: kegagalan lalu tampak sudah diumumkan |
| nilai yang tidak didefinisikan chart | typo pada `--set` kalau tidak akan tidak berbuat apa-apa, secara diam-diam |

`.github/scripts/helm-check.sh` menegaskan semua itu pada output yang dirender,
ditambah invarian di atas — satu scheduler, `pods/log` ada, migrasi lebih dulu,
port metrik tidak ada di Service.

## Tanpa Helm

Tujuh manifest ada di `deployments/kubernetes/`, siap untuk `kubectl apply -f`
setelah Anda mengedit namespace dan tag image:

```bash
kubectl apply -f deployments/kubernetes/rbac.yaml
kubectl apply -f deployments/kubernetes/api.yaml
kubectl apply -f deployments/kubernetes/scheduler.yaml
```

Mereka membawa keputusan yang sama dengan chart, dengan penalaran di komentar,
dan juga `alert.yaml`, `report.yaml`, dan `publish-job.yaml`. Bagian di bawah
menelusuri isi masing-masing.

## Dua peran

| peran | command | image | replika |
|---|---|---|---|
| API + antarmuka | `serve` | `:0.11.2` (distroless) | sebanyak yang Anda mau |
| scheduler + antrean | `scheduler` | `:0.11.2-worker` (alpine) | **satu** |

API memakai distroless karena hanya melayani HTTP: ia tidak mengeksekusi apa
pun, sehingga tidak membutuhkan shell. Worker memakai alpine karena step `run:`
membutuhkannya.

:::warning Satu replika scheduler
Dua proses yang mematerialisasi jadwal yang sama membuat run duplikat. Jika Anda
butuh ketersediaan, gunakan `replicas: 1` dengan `strategy: Recreate`.
:::

## Migrasi

Jalankan sebagai Job sebelum deploy, tidak pernah saat boot `serve`:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: brevis-migrate
spec:
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: migrate
          image: brevissh/brevis:0.40.0
          args: ["migrate", "up"]
          envFrom:
            - secretRef: {name: brevis-db}
```

## Deployment API

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: brevis-api
spec:
  replicas: 2
  selector:
    matchLabels: {app: brevis-api}
  template:
    metadata:
      labels: {app: brevis-api}
    spec:
      serviceAccountName: brevis
      containers:
        - name: api
          image: brevissh/brevis:0.40.0
          args: ["serve"]
          ports: [{containerPort: 8080}]
          envFrom:
            - secretRef: {name: brevis-db}
            - secretRef: {name: brevis-auth}
          env:
            - name: BREVIS_ENV
              value: production
          livenessProbe:
            httpGet: {path: /health, port: 8080}
          readinessProbe:
            httpGet: {path: /ready, port: 8080}
          resources:
            requests: {cpu: 100m, memory: 128Mi}
            limits: {memory: 256Mi}
```

:::note Mengapa kedua probe berbeda
`/health` **tidak** mengquery database; `/ready` melakukannya, dan menyebut
dependensi yang gagal. Jika liveness bergantung pada Postgres, database yang
goyah akan membuat Kubernetes **membunuh** pod API alih-alih sekadar
mengeluarkannya dari load balancer — dan pemulihan menjadi lebih lambat tepat
ketika sistem sudah berada di bawah tekanan.
:::

## Deployment scheduler

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: brevis-scheduler
spec:
  replicas: 1
  strategy: {type: Recreate}
  selector:
    matchLabels: {app: brevis-scheduler}
  template:
    metadata:
      labels: {app: brevis-scheduler}
    spec:
      serviceAccountName: brevis
      containers:
        - name: scheduler
          image: brevissh/brevis:0.40.0-worker
          args: ["scheduler", "--interval", "10s", "--concurrency", "5", "--max-pods", "10"]
          envFrom:
            - secretRef: {name: brevis-db}
            - secretRef: {name: brevis-auth}
          env:
            - name: BREVIS_ENV
              value: production
            - name: BREVIS_PODS
              value: "on"
            - name: BREVIS_POD_NAMESPACE
              value: data
            - name: BREVIS_POD_SERVICE_ACCOUNT
              value: brevis-runner
            - name: BREVIS_POD_ENV_FROM_SECRETS
              value: bigquery-cred
```

`BREVIS_PODS=on` **mensyaratkan** klaster: tanpa service account yang di-mount,
boot gagal alih-alih diam-diam turun ke eksekusi lokal.

## Izin

Scheduler membuat, memantau, dan menghapus pod di namespace eksekusi. Tidak lebih
dari itu:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: brevis-runner
  namespace: data
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["create", "get", "list", "watch", "delete"]
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]
```

`pods/log` adalah yang memungkinkan output sebuah step diikuti ke antarmuka.
Tanpanya, run berfungsi dan log tetap kosong.

## Setelah deploy

```bash
kubectl -n data get pods -l app=brevis-api
kubectl -n data exec deploy/brevis-api -- brevis version
curl -fsS https://brevis.example.com/ready
```

Picu workflow sederhana dari antarmuka dan pastikan pod step muncul dan
menghilang:

```bash
kubectl -n data get pods -w
```

## Ketika step tetap Pending

Hampir selalu salah satu dari tiga ini:

| penyebab | cara memastikan |
|---|---|
| taint tanpa toleration | `kubectl describe pod` menampilkan `node(s) had untolerated taint` |
| `nodeSelector` tanpa node | `describe` menampilkan `didn't match Pod's node affinity` |
| resource tidak tersedia | `describe` menampilkan `Insufficient cpu` atau `memory` |

Untuk memeriksa pod step yang gagal alih-alih menontonnya menghilang:

```bash
BREVIS_POD_KEEP_ON_FAILURE=true
```

Biarkan nonaktif di production — pod yang berhenti memakai kuota.

## Langkah berikutnya

- [Pod per step](/id/docs/pod-per-step/index.md) — model eksekusi
- [Konfigurasi](/id/docs/configuration/index.md) — setiap variabel `BREVIS_POD_*`
- [Observabilitas](/id/docs/observability/index.md) — apa yang di-scrape, dan dari proses mana
