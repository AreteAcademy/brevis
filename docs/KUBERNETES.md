# Running on Kubernetes — one pod per step

## Installing it

Use the chart, at [`deployments/helm/brevis`](../deployments/helm/brevis) — its
[README](../deployments/helm/brevis/README.md) has the four required values and
the list of what it refuses to render.

`deployments/kubernetes/` holds the same thing as seven plain manifests, with
the reasoning in comments, for an installation that does not use Helm. The two
are kept in step by `.github/scripts/helm-check.sh`, which asserts the
invariants on the chart's rendered output: one scheduler replica with
`strategy: Recreate`, `pods/log` in the Role and no `update` or `patch`, the
migration Job as a `pre-install,pre-upgrade` hook, the right image per role, the
metrics port off the Service, and no token mounted in the API's pod.

The rest of this document is what those manifests contain and why — read it when
changing them, or when an installation behaves in a way the chart's values do
not explain.

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

## A third place a step can run: `host:`

Some work cannot be a pod on demand — a licensed tool, a GPU machine, a dlt
pipeline on a VM somebody else administers. Such a step names a machine instead
of an image:

```yaml
steps:
  - id: extract
    host: dlt-runner-01
    run: python pipelines/orders.py
```

The machines come from the **installation**, never from the file:

```bash
BREVIS_HOSTS=dlt-runner-01=https://10.0.3.7:9443,gpu-01=https://10.0.3.9:9443
BREVIS_HOST_TOKEN=...
```

For the same reason `BREVIS_POD_ALLOWED_SECRETS` exists: the file that names a
host is written by somebody else, and a workflow free to dispatch to an
arbitrary address would be dispatching this engine's credentials to it. Empty
means no step may use `host:` at all, and the refusal says so with the variable's
name in it.

`host:` and `image:` are **alternatives**, and naming both is refused at
publish rather than at execution — otherwise the step publishes fine, sits in
the schedule, and fails at three in the morning on whichever executor won.

### It does not fall back

An `image:` with no cluster degrades to a local process with a warning, because
a container and a process are the same command in two wrappers. **A host does
not.** It is where the licence lives, or the GPU, or the data, and quietly
running the command somewhere else is the worst of the three outcomes. An
unknown host fails the step, naming what the installation actually offers.

### Running the agent

`brevis-agent` is the program on the other side. It ships from this repository
and is a binary of its own:

```bash
brevis-agent \
  --listen :9443 \
  --token-file /etc/brevis/agent-token \
  --secrets-dir /etc/brevis/secrets \
  --allow-secrets vendor-api,partner-sftp \
  --state-dir /var/lib/brevis-agent
```

| flag | |
|---|---|
| `--token-file` | the shared token, **from a file** and never a flag: a flag is in `ps` output for anybody on the host to read |
| `--secrets-dir` | the store, as `<dir>/<name>/<key>` — the shape the kubelet mounts and Docker uses |
| `--allow-secrets` | which names a step may ask for. **Empty denies every one** |
| `--state-dir` | what survives the agent restarting: the execution → pid map, so a cancel still reaches a running step, and the steps that **finished**, so a resume is still answered with how they ended. On Kubernetes it has to be a **persistent volume** — see [Its state on a persistent volume](#its-state-on-a-persistent-volume) |
| `--ring` | lines kept for a reconnect. Beyond this a dropped connection fails the step rather than resuming with a hole |
| `--keep-finished` | how long a step stays resumable **after it ends** (default `1h`), so a connection that dropped at the end — or a scheduler that was down while it ended — still reads its exit code, its published context and the end of its log. With `--state-dir` they are also written there, and survive a restart with the same retention; without it an agent restart loses them |
| `--keep-finished-max` | at most this many finished steps kept (default `200`), each with its ring; past it the oldest goes first |
| `--drain-timeout` | on SIGTERM, how long to wait for the running steps to end **and be read** before cancelling them. Default `0`: as long as it takes, the orchestrator's grace period being the bound — see [When the agent stops](#when-the-agent-stops) |
| `--advertise` | the address the engine comes back to for **this instance's** executions. Required behind a shared address, meaningless without one — see below |
| `--insecure-no-token` | run with **no authentication at all**: anything that reaches the port runs any command. It has to be said, because an empty `--token-file` does not start |

### Behind one address: `--advertise`

An agent is stateful per execution — the output ring and the process handle are
in **that process's** memory — so a resume or a cancel that reaches a different
replica is answered "not running here" while the step runs on undisturbed.

```
--advertise http://$(POD_NAME).brevis-agent.$(POD_NAMESPACE).svc:9443
```

Told, not discovered: the process can read its hostname and cannot know which of
its names the engine routes to. `BREVIS_HOSTS` points at the **Service**, so
starts spread; each replica then says where to come back to. Left empty, the
engine keeps using the address it was configured with, which is right for one
agent and wrong for a pool — and the pool's symptom is a step failed on the
first network blip, with a process still running.

The advertised address has to belong to the same installation as the configured
one: same scheme, and a host that is the configured one or a name inside it. A
replica that advertises somewhere else is ignored rather than followed.

### The probe

**`GET /health` is the only route that answers without the token**, and the only
GET. A kubelet sends no `Authorization` header, so the probe either sits outside
the guard or the pod it gates never goes Ready.

It checks nothing outside the process. The agent has no dependency to be ready
for — no database, no queue — so it reports the one state it has: `200 ok` while
it serves, `503 draining` once a SIGTERM has arrived.

The manifests gate **readiness** with it, and deliberately not liveness. It does
**not** decide DNS: the Service publishes not-ready pods, so a draining one keeps
its name (below). It decides the **rollout** — a StatefulSet moves to the next
pod only when the new one is Ready, and without the probe that is the instant the
container runs, before the listener binds. A liveness probe would SIGTERM a pod
that may be holding hours of a step's work, which is not the probe's to do.

It is **not an orchestrator**. It knows nothing of workflows, dependencies,
schedules or retries — an agent that learned any of that would be a second
orchestrator to keep in agreement with the first.

**It has no TLS of its own.** Put it behind something that terminates TLS, or on
a network where that is somebody else's job. A token on a plain socket is a
token anybody on the path can read.

### When the agent stops

A rolling restart of the agents, or a node drain, sends each one SIGTERM while
steps may be running on it. It **drains**:

1. **No new step.** A start is answered `503`, before anything is reserved, and
   `/health` turns `503 draining`.
2. **The running steps go on**, and so do their resumes and cancels — the
   listener stays up. An engine restarted in the same rollout comes back for the
   step it handed over and finds it.
3. **It waits for each step to end and its exit to be read.** Ended is not
   enough: a step that ends while the engine is away has its exit only in the
   agent's memory, and an agent that left then would answer the returning engine
   "unknown execution" — which starts the step **again**. Read means the stream
   reached the engine's socket to its last line; a step cancelled meanwhile is
   not waited for.
4. **Then it exits `0`.**

Past `--drain-timeout`, or on a **second** SIGTERM, what is left is cancelled —
the whole process group, as a cancel does — logged by id, and the agent exits
`1`. Every minute of a drain it logs what it is still waiting for.

**It needs the time to do it.** The bound is the orchestrator's: Kubernetes kills
the pod when `terminationGracePeriodSeconds` runs out (30s unless said), systemd
when `TimeoutStopSec` does (90s). The manifests here set **3600**, from
`agent.terminationGracePeriodSeconds` in the chart; set it to the longest step
the host runs, plus margin. On systemd, raise `TimeoutStopSec` to match.

**A draining pod keeps its DNS name.** Kubernetes marks a terminating pod's
endpoint not-ready, and a headless Service publishes only ready ones — so the
name `--advertise` points at would vanish at SIGTERM, and the engine's resume
would fail with `no such host` while the agent waits to be read. The agent's
Service sets `publishNotReadyAddresses: true`. The cost is that a **start** can
land on a pod that is draining or not yet bound; the engine sends a start
answered `503` or refused **again** — five tries over about fifteen seconds —
which cannot run it twice: a start that did arrive makes the next one "already
running". Resumes and cancels are not retried that way; they belong to the agent
holding the step.

**A start refused for draining goes to another replica.** The engine resolves
the Service's name itself and dials its addresses in turn, so a pool is not
one address behind a kept-alive connection. A draining agent answers a start
`503` with `Brevis-Refusal: draining` and closes the connection; the engine
then sends that start, and the next ones for 30 seconds, to the other
replicas, and tries the draining one last. A step reaches a draining pod at
most once per drain. An agent from 0.38, which sends no header, is recognised
by its message. Only an explicit draining refusal counts: a bare `503`, from a
balancer with nobody behind it, is a host that is down.

The engine talks to agents over HTTP/1.1. A connection carries one exchange at
a time, so a start is never stuck on a connection held by another step's
stream to the draining pod.

**When every replica is draining**, the step was not started, and nothing is
spent on it: the step's own attempts are not used, and the run goes back to
the queue without spending one of its own and without an alert, to be tried
again after `--drain-requeue-delay` (30s). It waits that way for at most
`--drain-wait` (1h) from the first refusal; past that the pool counts as down,
and the run fails its attempt as before, its error saying how long it waited.
`--drain-wait 0` spends the attempt at once. A step that failed for real
beside one that was refused makes the run an ordinary failure. See
[COMMANDS.md](COMMANDS.md).

**What a drain does not cover, `--state-dir` does.** A drain only happens on a
SIGTERM. An agent that crashes, is OOM-killed or loses its node never drains,
and a step that finished there with nobody reading it would be answered
"unknown execution" — run again. So every finished step is also written to
`<state-dir>/finished/` and read back when the agent starts, and the returning
engine gets the same log, context and exit the process before would have given
it. The two are layered on purpose: the drain does not trust the directory to
outlive the pod, because the agent cannot know whether it does.

#### Its state on a persistent volume

On an `emptyDir` the state dies with the pod, which is every rollout. The chart
and the manifests here claim a volume per pod instead — `ReadWriteOnce`,
**1Gi**, the cluster's default StorageClass — and set `fsGroup: 65532`, since a
claimed volume comes up owned by root and the agent does not run as root. In
the chart: `agent.state.size`, `agent.state.storageClassName`, and
`agent.state.persistent=false` to go back to an `emptyDir`, losing cancel and
resume across a restart.

1Gi holds `--keep-finished-max` (200) steps of `--ring` (10,000) lines of a few
hundred bytes. A step whose lines are much longer can fill it; that write fails
with a warning, the step itself is unaffected, and only its surviving a restart
is lost.

**Upgrading an agent that had an `emptyDir`** takes one step, once. A
StatefulSet's `volumeClaimTemplates` cannot change, so the apply, `helm
upgrade` or Argo sync is refused until the old object is gone:

```bash
kubectl -n <namespace> delete statefulset brevis-agent --cascade=orphan
# then apply / helm upgrade / sync: the pods are replaced one at a time
```

The name is `brevis-agent` in the manifests here and from the chart installed as
`brevis`; `kubectl get statefulset` lists yours. `--cascade=orphan` deletes the
StatefulSet and leaves its pods running; the new one adopts them and rolls them,
each one draining as above. What the old `emptyDir` held is lost in that one
rollout.

### What the agent does, and what the engine still will not do

The engine does **not** send secret values, and this executor did not become the
first place it does. `secrets:` crosses as the coordinate `secret-name/key`,
exactly as it reaches the kubelet in a pod, and the agent resolves it against
its own store under its own allowlist. The trade-off is stated rather than
hidden: the policy now lives in two places, one per host, and a machine somebody
else administers should not inherit this cluster's list.

Three more things the first version does and does not:

- **A dropped connection resumes.** The agent numbers every line and the engine
  asks to continue from the last one it saw, so a network blip does not fail a
  forty-minute load. When the agent's buffer can no longer reach back that far
  the step **fails** rather than continuing with a hole: output missing an
  unknown number of lines would leave a step's counters wrong by an unknown
  amount, silently.
- **An agent that stops reporting loses the step.** It renews a lease on the
  same stream, and a step that is merely quiet still renews it. When the lease
  expires the step fails saying so — and saying plainly that the process **may
  still be running** on a host the engine can no longer see.
- **One token for every host.** No revoking a single host without changing them
  all, and no per-host identity in the audit trail. That is a first version, and
  it is written here rather than discovered.

## What the scheduler does (and does not do)

It does **not** run dbt or Python. It speaks HTTP to the API server and SQL to
Postgres — which is why the Deployment asks for 50m of CPU and 128Mi. The heavy
work is in the pods it creates.

Four REST calls, written on the stdlib: create a pod, read the status, read the
log, delete the pod. No `client-go`: the official library brings hundreds of
dependencies and tens of MB for this — the same arithmetic that led to
vendoring React instead of adopting npm.

### When it stops

A deploy or a node drain sends the scheduler SIGTERM, and a run may be halfway
through. What happens to it:

1. **A `host:` step is handed over; anything else is stopped.** A `host:` step
   goes on running on its agent, and its row stays `running` with where it is
   — the instance, the execution, the last line read. A pod is deleted and a
   local process killed: they do not outlive the scheduler.
2. **Nothing interrupted is recorded as done.** The steps stopped end
   `canceled`; the steps not yet started get no row — they were not ruled out,
   the run was stopped. A step counts as succeeded only when its executor said
   so.
3. **The run goes back to the queue, for free.** It moves `failed → retrying →
   queued` without spending an attempt, without an alert and without a
   backoff, its error saying the scheduler shut down. The next scheduler takes
   it at once: it **follows** a handed-over step on its agent — the same
   execution, as the same attempt, its log from the start of the agent's ring
   — and re-runs what had not succeeded.
4. **Then the process exits**, after the dispatcher has written all of that
   down.

A run whose last step finished as the signal came keeps its outcome, success or
failure.

**When a handed-over step cannot be followed**, the next scheduler decides by
why:

| the agent says | what happens |
|---|---|
| it has it, finished — even after an agent restart, with its state on a persistent volume | it is **followed** as before: its log, context and exit are read, and nothing runs again |
| it has no execution by that id — it restarted without its state, or kept it for less than `--keep-finished` (1h) | the step starts again, under the same id |
| it has it, but its ring no longer reaches the last line read — more than `--ring` lines written meanwhile | it is stopped on the agent and the attempt fails; the retry runs it under a new id |
| nothing — the agent cannot be reached | the attempt fails |

A handover that could not be written down — the database gone at that moment —
is not left running: the step is stopped, as with handover off.

**`BREVIS_HOST_HANDOVER=off`** restores cancelling: every `host:` step is
stopped on a shutdown, and re-run by the next scheduler. On is the default.
A **lost claim** always stops it, handover or not: another scheduler holds the
run.

**It needs time to do it.** Writing the end takes up to 15s, a `host:` cancel up
to 10s, a pod deletion up to 30s, and they overlap. Kubernetes' default
`terminationGracePeriodSeconds` is 30s and Docker Compose's stop timeout is 10s;
neither is raised by this chart or `docker-compose.yml`. A process killed before
it finishes leaves its runs to the orphan sweep, which reclaims them after
`Visibility` (15 minutes) of silence — and a `host:` step it neither handed over
nor cancelled may still be running on its agent. Raise the grace period if your
steps take long to stop.

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
  (`BREVIS_POD_KEEP_ON_FAILURE`). Thousands of `Completed` pods clutter the
  namespace and say nothing that Brevis's own history does not say better. A
  pod whose run was **cancelled** is deleted even so: it has not finished, and
  keeping it would keep its step running.
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
| `concurrency:` in the YAML | simultaneous runs **of the same workflow**, or of every workflow in a **group** | the **data** |

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

### Groups: two workflows, one limit

A per-workflow limit cannot say that two workflows must not overlap. That was
#32: a daily and a `*/15` building the same dbt models, each with
`concurrency: 1`; a manual run of the daily landed on a slot of the other, one
dropped the `__dbt_tmp` table the other was reading, and dbt failed. Both
declare a group instead:

```yaml
concurrency:
  group: jaffle_models
  limit: 1        # a group without a limit means 1
```

The claim counts by the run's **concurrency key** rather than its workflow:
`COALESCE('g:' || group, 'w:' || workflow_slug)`. A workflow with no group keeps
contending with itself, as before; the prefixes stop a group called `daily`
from sharing a limit with the workflow `daily`. The group is a snapshot on the
run (`runs.grupo_concorrencia`), like the limit, so editing a file does not
change what is already queued.

- **Manual runs join the group.** A click outranks scheduled work in the
  group's queue, but waits for what is in flight. #32 was a manual run.
- **Every member declares the same limit**, refused at `validate` and
  `publish` otherwise — `publish` also checks the members already in the
  database. The limit is read per run, so members disagreeing would let
  whichever run the claim numbered first decide.
- **A full group accumulates runs**, as `concurrency: 1` always did for one
  workflow. It changes the scale, not the behaviour.
- **A waiting run says what holds it.** Its page names the group (or the
  workflow's own limit), the runs in the slots and how many are ahead of it.
  The page repeats the claim's arithmetic (`RunRepo.WaitingOn`), and shows
  nothing when the limit is not what holds the run -- a dispatcher slot,
  `--max-pods`, a retry's backoff.
- **It serializes Brevis runs only.** A `dbt run` started outside Brevis is
  not in this queue, and the symptom is identical.

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

Four decisions:

- **The alert fires when the run GIVES UP**, not on every attempt. Announcing
  every failure would turn a successful retry into two alerts and a silence, and
  a channel that shouts for nothing stops being read.
- **The webhook never comes from the YAML.** It is a credential: whoever has the
  URL posts in the channel as if they were the platform.
- **Failing to announce takes nothing down.** A webhook that is down becomes a
  retry, and the run ends FAILED with the queue still being consumed.
- **The scheduler does not talk to Slack.** It writes the alert into an outbox,
  in the same transaction as the failure, and `brevis alert` delivers it.

### The outbox, and why there is a third pod

`deployments/kubernetes/alert.yaml` runs `brevis alert`, which claims from the
`alertas` table and delivers.

It is there because of where an alert is WRITTEN. Before it, the dispatcher
called Slack directly and, when the call failed, logged and carried on — the
right behaviour for a pipeline, and also how the alert was **lost**: no retry,
no record, and nothing on a screen to say anybody should have been told. A
separate pod the dispatcher *called over HTTP* would have had exactly the same
failure with more moving parts.

What removes it is the transaction. Either the run is recorded as out of
attempts and the alert exists, or neither happened.

| | |
|---|---|
| Slack is down | the outbox retries with backoff; before, it was lost |
| the scheduler restarts | the alert is a row, not a goroutine |
| the alert pod restarts mid-send | the visibility sweep hands the row back |
| delivery is visible | attempts and the last error, on the run's screen |
| rate limits | one sender, delivering serially — not N dispatchers |

**One replica, and not as a placeholder for leader election.** Slack's rate
limit is per workspace, so two senders competing during a burst of failures —
exactly when alerts matter most — turn one incident into two.

An alert it gives up on is **kept**. "Raised, not delivered, 4 attempts, 403
from Slack" is the most useful row this table produces, because it is the case
where somebody is waiting for a message that is not coming; deleting it makes
that indistinguishable from an alert nobody raised.

### The weekly summary is a CronJob, not a loop

`report.yaml` runs `brevis report --window 168h` on Monday mornings, and it does
**not** go through the outbox above. The two share a delivery channel and
nothing else: an alert is triggered by an event, is about one run and is wanted
now; a report is triggered by a schedule, is about a window and is wanted on
Monday. Losing an alert is an outage nobody hears about; missing one week's
summary is next week's summary.

A CronJob rather than a loop means no new state and no leader election — the
cluster already has a scheduler. And `--dry-run` prints what the message would
have said, which is how somebody comes to trust a weekly summary:

```bash
kubectl -n dados run brevis-report --rm -it --restart=Never \
  --image=brevissh/brevis:0.2.1-worker \
  --env=BREVIS_DATABASE_URL="$(kubectl -n dados get secret brevis-db -o jsonpath='{.data.url}' | base64 -d)" \
  -- report --window 168h --dry-run
```

The message carries runs, failures, the pipelines that failed most, the ones
that took longest, and the rows and bytes the SDK reported. It carries **no**
CPU or memory, and prints no zero for them: the engine does not collect those,
the cluster's own metrics do, and the message says so rather than letting their
absence read as "nothing to report".

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
kubectl apply -f deployments/kubernetes/api.yaml \
              -f deployments/kubernetes/scheduler.yaml \
              -f deployments/kubernetes/alert.yaml \
              -f deployments/kubernetes/report.yaml
```

`alert.yaml` requires the `brevis-slack` secret above — unlike the scheduler,
where it is optional. That process refuses to start without a channel: one with
nowhere to deliver drains the outbox into "undelivered" as fast as it fills, and
those rows look exactly like Slack rejecting them.

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
