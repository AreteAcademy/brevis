# Running on Kubernetes — one pod per step

## How it moves

```
scheduler                          cluster
─────────                          ───────
reads the schedule
creates the Run  ─────────────►  (nothing yet)
walks the graph
  for each ready step:
    builds the Pod  ───────────►  Pod: the step's image, the step's command
    follows the status  ◄───────  Pending → Running → Succeeded/Failed
    follows the log     ◄───────  the container's stdout
    reads the exit code ◄───────  containerStatuses[0].terminated
    deletes the pod  ──────────►  (gone)
```

The pod starts with **that step's exact image** and a command. There is no
generic worker waiting for work: the work is what brings its own runtime.

## Why this is the point of the project

A monolithic worker forces the image to contain everything any step might need.
In practice:

| | one image | one pod per step |
|---|---|---|
| a dbt step | 1.9 GB, 1Gi of RAM | 1.9 GB, 1Gi |
| a Go fetcher next to it | **1.9 GB, 1Gi** | **12 MB, 32Mi** |
| changing the dbt version | rebuild everything | one line in that workflow's YAML |
| a step that leaks memory | takes the worker and its neighbours down | dies on its own |

The third item is the least obvious and the most expensive day to day: with a
single image, moving dbt from 1.10 to 1.11 on one pipeline forces moving it on
all of them.

## The YAML

```yaml
name: platform_workspace
schedule: "0 5 * * *"

image: us-central1-docker.pkg.dev/acme/apps/dbt:1.10.3   # the steps' default
resources:
  cpu: 200m
  memory: 1Gi
  limits: {memory: 2Gi}

steps:
  - id: bronze_workspace
    run: dbt build --select bronze_workspace+

  - id: notify
    image: ghcr.io/acme/notify:0.3   # a Go binary: another runtime, another size
    shell: false                       # distroless has no shell
    run: /notify --channel data
    resources: {cpu: 25m, memory: 32Mi, limits: {memory: 64Mi}}
    depends_on: [bronze_workspace]
```

`image` and `resources` at the top are the defaults; a step overrides what it
needs, field by field — a step can ask for more memory alone without losing the
default's CPU.

`shell: false` passes the command as argv. It exists for distroless images,
where `/bin/sh` does not exist and `sh -c` would fail with "no such file or
directory" — a correct error that says nothing about the cause.

## The same YAML runs locally

`BREVIS_PODS=auto` (the default): with no service account mounted, the step runs
as a process on the instance itself and the `image:` is **ignored with a warning
in the log** — silencing it would make it look as though it ran on the declared
image.

In the cluster's deployment use `BREVIS_PODS=on`: there, ending up without a
cluster has to be a boot error. With `auto`, a failure to mount the service
account would make the scheduler run everything inside its own 128Mi pod, in
silence.

## What the scheduler does (and does not do)

It does **not** run dbt or Python. It speaks HTTP to the API server and SQL to
Postgres — which is why the Deployment asks for 50m of CPU and 128Mi. The heavy
work is in the pods it creates.

Four REST calls, written on the stdlib: create a pod, read the status, read the
log, delete the pod. No `client-go`: the official library brings hundreds of
dependencies and tens of MB for this — the same arithmetic that led to
vendoring React instead of adopting npm.

## Decisions the implementation pins down

- **`restartPolicy: Never`.** The dispatcher is what counts attempts and applies
  backoff. Letting the kubelet restart would create a second retry policy,
  invisible to the history.
- **A deterministic name** per (run, step, attempt). If the process dies between
  creating the pod and recording that, the next attempt adopts the existing pod
  instead of starting a second one running the same dbt in parallel.
- **`activeDeadlineSeconds`** mirrors the timeout. It is the safety net on the
  cluster's side: if Brevis dies, the pod still stops on its own.
- **The reason for waiting becomes a log line.** `ImagePullBackOff` and
  `CreateContainerConfigError` produce no output at all; without reporting them,
  the step would look stuck until the timeout, with no line explaining it.
- **The log is drained after the end**, as well as followed live. Closing the
  channel with lines in the buffer would lose exactly the last ones — the ones
  that explain the failure.
- **A successful pod is deleted; a failed one can stay**
  (`BREVIS_POD_MANTER_EM_FALHA`). Thousands of `Completed` pods clutter the
  namespace and say nothing that Brevis's own history does not say better.
- **The cluster's `reason` goes into the failure message.** `OOMKilled` and
  `DeadlineExceeded` call for the opposite actions from "the code failed".

## Variables inside the task

The task does not inherit the orchestrator's environment — it carries
`BREVIS_DATABASE_URL` with a credential, and a workflow is an arbitrary command
written by somebody else.

There are two paths, and the difference between them is **who chooses**.

### From the installation, for every task

| mode | mechanism |
|---|---|
| pod | `BREVIS_POD_ENV_FROM_SECRETS=my-secret` → becomes an `envFrom.secretRef` on the pod. The variables go from the Secret straight to the task, **without passing through the scheduler**. |
| local | `BREVIS_TASK_ENV=GOOGLE_PROJECT_ID,STAGE` → passes those through from the process's environment. `NAME=value` sets a literal; `*` passes everything except the `BREVIS_*` ones. |

In both, `PATH` and `HOME` always go in: without `PATH` no command resolves, and
the error would be a "not found" that explains nothing.

The reach is **every step of every workflow**. For a credential only one fetcher
uses, that is more reach than need: one vendor's cookie also enters the
`dbt build` pod next door.

### From the YAML, per step

```yaml
steps:
  - id: fetch_occurrences
    run: /usr/local/bin/gabriel
    env:
      BREVIS_LOG_LEVEL: info              # a literal — the file is in git
    secrets:
      GABRIEL_SESSION_COOKIE: gabriel-session/cookie
```

They are two keys and not one on purpose. With only one, the shortest path to
making it work would be pasting the secret into the YAML.

| | `env:` | `secrets:` |
|---|---|---|
| the value is in the file | yes | **no**, only the coordinate |
| in a pod | `env: [{name, value}]` | `valueFrom.secretKeyRef` — the kubelet is what reads it |
| locally | a literal | the same-named variable in the engine's environment; **absent is an error** |

Both inherit from the workflow down to the step, name by name, like `image` and
`resources`. Precedence, weakest to strongest: the engine's global environment,
the workflow's `env:`, the step's `env:`.

### And the installation still decides which secrets exist

`secrets:` inverts who chooses, and the YAML is written by somebody else.
Without a limit, a workflow would mount any Secret in the namespace — Brevis's
own database included — and run an arbitrary command with it in hand.

```bash
BREVIS_POD_ALLOWED_SECRETS=gabriel-session,ana-api
```

**Empty denies everything.** Denying by default costs one variable in the
installation; allowing by default costs the inverse, and the inverse is
irreversible. The refusal happens while the pod is being assembled, naming the
Secret and where to allow it — not at the server, which would accept the
`secretKeyRef` and fail later for another reason.

The final split: **the installation says which secrets exist for workflows, the
YAML says which step gets each one.**

## The credential volume

A credential that **rotates** — a session cookie with a sliding window — dies
with the pod when the new value goes nowhere. Without this, somebody re-pastes
the seed once per window, forever.

With a volume, the trade is different: the environment variable stops holding
the **rotating** value and starts holding a **static** key. It is pasted once.

```bash
BREVIS_POD_CREDENTIAL_PVC=brevis-credentials
BREVIS_POD_CREDENTIAL_PATH=/var/brevis/credentials   # optional, this is the default
```

With the PVC set, **every step's pod** gets the volume and a
`BREVIS_CREDENTIAL_DIR` env pointing at the mount — the same variable the SDK
reads when somebody runs on their own machine with
`BREVIS_CREDENTIAL_DIR=./.brevis`. The same code in both. Without the PVC,
nothing changes.

A step that declares its own `BREVIS_CREDENTIAL_DIR` in `env:` beats the
injection, and the variable does not go in twice.

### The contents are encrypted, and the engine does not have the key

The SDK writes AES-256-GCM with the key from `BREVIS_CREDENTIAL_KEY`, which is
an ordinary Secret and arrives through `BREVIS_POD_ENV_FROM_SECRETS` or through
`secrets:` in the YAML. **Without a key the SDK refuses to turn the store on**
rather than writing in the clear — a volume becomes a snapshot, a snapshot
becomes a backup, and a backup becomes a place where nobody remembers there is a
credential.

```bash
head -c 32 /dev/urandom | base64    # the key, once
```

> **The recommendation has changed.** The path below still works and is what
> serves whoever has no GCS — but to keep a rotating credential, prefer the
> SDK's `gcs.Credential{Bucket, Object}`: it does not touch the cluster, it
> creates no `PersistentVolume` (which is not namespaced), an object write **is**
> atomic where gcsfuse's `rename` is not, and concurrency comes out through
> `ifGenerationMatch` instead of an approximate lock. The strongest one: an
> object **survives rebuilding the infrastructure**, and a PVC dies with the
> cluster — and it is precisely during an environment rebuild that nobody
> remembers there was a rotating credential somewhere.

### The PV, for GCS Fuse

The dev cluster is GKE, and `ReadWriteMany` there is not EFS. Of the three
options, the one that serves is the **GCS Fuse CSI**: real RWX, and the cost of
keeping a few KB is cents. `Filestore` has a 1 TiB minimum instance; an RWO
Persistent Disk does not share between nodes.

```yaml
apiVersion: v1
kind: PersistentVolume
metadata:
  name: brevis-credentials
spec:
  accessModes: [ReadWriteMany]
  capacity: {storage: 1Gi}      # ignored by gcsfuse; the field is required
  storageClassName: ""
  # The SDK refuses a directory with loose permissions, so the mode comes from
  # the mount: a shared volume at 0755 is readable by every pod that mounts it.
  mountOptions:
    - implicit-dirs
    - uid=0
    - gid=0
    - dir-mode=0700
    - file-mode=0600
  csi:
    driver: gcsfuse.csi.storage.gke.io
    volumeHandle: YOUR-BUCKET
```

Confirm first that the driver is enabled (`gcsFuseCsiDriver` in the addon
config) and that the pods' service account has `roles/storage.objectAdmin` on
the bucket.

**One honest caveat:** `rename` on gcsfuse is **not atomic** the way it is on a
real POSIX filesystem. The SDK writes to a temporary file and renames, which
covers a crash mid-write on a normal filesystem; on gcsfuse, for a file of a few
KB written by one pod at a time, the risk is low — but it is real, and it is one
more reason to keep `concurrency: 1` on the workflow that uses this.

The SDK writes **last writer wins**, and that is a choice: with the provider
that motivated the feature, rotating does not invalidate the previous token, so
two concurrent values both work. For a provider that does invalidate the
previous one, do not use this without a lock of your own.

## Security

Two accounts, and the separation is what matters:

| account | permission |
|---|---|
| `brevis-scheduler` | create, read, list, watch and delete pods; read logs. No `update`, no `patch`. |
| `brevis-task` | **none** |

The task's pod runs commands that came out of a YAML. If it inherited the
scheduler's account, any workflow could create pods, read secrets and scale
itself. With an account that has no role, the worst an arbitrary command does is
use the credentials the installation explicitly gave it — through
`BREVIS_POD_ENV_FROM_SECRETS`, which comes from the scheduler's environment, or
through a `secrets:` in the YAML **that only reaches the Secrets in
`BREVIS_POD_ALLOWED_SECRETS`**.

The YAML never widens the set; it only chooses, within what the installation
allowed, which step gets what.

## Concurrency: three limits

| where | counts | protects |
|---|---|---|
| `--concurrency` | simultaneous RUNS in the dispatcher | the process |
| `--max-pods` | simultaneous STEPS = live pods | the **cluster** |
| `concurrency:` in the YAML | simultaneous runs **of the same workflow** | the **data** |

The third is what stops a `*/15` that takes 20 minutes from overlapping itself —
two `dbt build`s on the same model fighting over the same table. Thirty-six of
the data repository's 51 flows declared that limit in Kestra.

```yaml
name: id_verification_today
schedule: "5-59/15 * * * *"
concurrency: 1
```

It is enforced **in the claim query itself**, for the same reason the global
limit is: there is no path in which more items leave the queue than are allowed.
Claiming and then handing back would be a window in which two dispatchers had
already taken the same workflow.

Two details the implementation pins down:

- **It counts claimed items, not runs in `running`.** Between the claim and the
  state transition there is an instant where the run is still `queued`; counting
  by status would open exactly that gap.
- **Numbering per workflow inside the batch.** Without it, three items of the
  same workflow with a limit of 1 would all come out in the same claim — the
  count does not change partway through the query.

A workflow at its limit **does not block the others**: the queue is shared, and
stalling everything because of one would be worse than having no limit.

The first alone is not enough: five runs with three parallel steps each would
mean **fifteen** pods. `--max-pods` is a semaphore shared by every run in the
process — with ten steps ready and five slots, five run and the rest go in as
slots open up.

Measured, with ten steps that do not depend on each other and `--max-pods 5`:

```
steps | peak_simultaneous | total_duration
   10 |                 5 | 00:00:12
```

Two batches of six seconds. The peak landed exactly on the ceiling — not six
(which would be a leak) and not fewer (which would be the limit turning into
serialization).

The slot is taken **per attempt**, not for the whole step: holding the place
during a retry's backoff would leave a slot in the cluster idle waiting on a
clock.

The semaphore lives in the process, and that is why the Deployment has one
replica with `Recreate`. With two replicas each would have its own ceiling —
when there is leader election or a count in the database, the limit becomes
genuinely global.

## Coming from Leoflow

What was a per-DAG packaging file there (`schema_version`, `dag_id`,
`base_image`, `build.platforms`, `tasks.<id>.execution`) splits into two places
here, for one reason: **what belongs to the pipeline stays in the YAML; what
belongs to the installation stays in the scheduler's environment.**

| Leoflow (per DAG) | Brevis | where |
|---|---|---|
| `dag_id`, `description`, `tags` | `name`, `tags` | workflow |
| `base_image` | `image:` | workflow (per step, with a default) |
| `tasks.run.resources` | `resources:` | workflow (per step) |
| `variables: [...]` | `BREVIS_POD_ENV_FROM_SECRETS` | installation |
| `execution.service_account` | `BREVIS_POD_SERVICE_ACCOUNT` | installation |
| `execution.node_selector` | `BREVIS_POD_NODE_SELECTOR` | installation |
| `execution.tolerations` | `BREVIS_POD_TOLERATIONS` | installation |
| `build.platforms` | — | the image is built elsewhere, once |
| `params.get('x')` (Airflow) | `params:` + `{{ .x }}` | workflow |
| `alerts.on_failure` | `BREVIS_SLACK_WEBHOOK` | installation |
| `connections` | **does not exist** | — |

A service account in the pipeline's YAML would be the dangerous inversion: a
workflow file choosing which identity it runs as in the cluster. That is why
those three stay in the scheduler's environment.

**There is no per-DAG registry and no per-DAG build.** In Leoflow, publishing a
new DAG went through `leoflow deploy`; here the folder is the artifact — a
ConfigMap with the YAMLs and a Job that runs `brevis publish --prune`. See
`publish-job.yaml`.

**The absence that remains is *connection*** — there is no named connection. The
failure alert does exist (see below).

## Failure alerting

```bash
kubectl -n dados create secret generic brevis-slack \
  --from-literal=webhook='https://hooks.slack.com/services/...'
```

With the webhook configured, **every workflow starts announcing** — with no
block repeated in any YAML. That was the design difference against Kestra:
there, all 51 flows each carried the same copied `errors: alert_slack`, twenty
lines of payload fifty times.

Three decisions:

- **The alert fires when the run GIVES UP**, not on every attempt. Announcing
  every failure would turn a successful retry into two alerts and a silence, and
  a channel that shouts for nothing stops being read.
- **The webhook never comes from the YAML.** It is a credential: whoever has the
  URL posts in the channel as if they were the platform.
- **Failing to announce takes nothing down.** A webhook that is down becomes a
  log line; the run ends FAILED and the queue goes on being consumed.

The message carries the domain and the pipeline (from the `tags`, with the
slug's prefix as a fallback), the trigger, the attempts, the logical date, the
error's last lines and a direct link to the run. The error is truncated at 900
characters — Slack's block refuses the *whole* message above 3000, so truncating
is what guarantees the alert arrives.

## Applying

```bash
kubectl apply -f deployments/kubernetes/rbac.yaml
kubectl -n dados create secret generic brevis-db --from-literal=url='postgres://...'
kubectl -n dados create secret generic brevis-task-env \
  --from-literal=STAGE=prod --from-literal=GOOGLE_PROJECT_ID=acme-...
kubectl -n dados create configmap brevis-brand --from-file=brand.yaml
kubectl apply -f deployments/kubernetes/api.yaml -f deployments/kubernetes/scheduler.yaml
```

`job-example.yaml` shows, written by hand, the pod the scheduler assembles —
useful for checking what the cluster is going to receive before anything runs.

## What does not exist yet

- **Volumes.** No step mounts a PVC or an emptyDir; what has to pass between
  steps goes through the warehouse. `volumes:` in the YAML is the natural next
  step.
- **Leader election.** One scheduler replica, and that is why the Deployment
  uses `Recreate`.
- **A native Job/CronJob.** The pods are created directly. A `Job` would bring
  retry on the cluster's side, which is exactly the second retry policy that was
  avoided.
- **Sidecars and initContainers.**
