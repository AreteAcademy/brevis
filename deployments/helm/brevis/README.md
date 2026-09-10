# brevis

A data orchestration runtime: the API and interface, a cron scheduler with a
persistent queue, and one Kubernetes pod per workflow step.

```bash
helm install brevis ./deployments/helm/brevis \
  --namespace data --create-namespace \
  --set database.url="postgres://brevis:pw@postgres/brevis?sslmode=require" \
  --set auth.user=admin \
  --set auth.passwordHash="$(brevis hash)" \
  --set auth.secret="$(openssl rand -base64 48)"
```

That is the whole install. Migrations run first as a pre-install hook, both
Deployments come up with the right image each, and the scheduler gets a
ServiceAccount that may create pods while the task pods get one that may do
nothing at all.

## What it installs

| | |
|---|---|
| `Job` (hook) | `migrate up`, before anything else. `serve` never changes the schema |
| `Deployment` api | `serve` on the distroless image — it executes nothing, so it needs no shell, and its token is not mounted |
| `Deployment` scheduler | `scheduler` on the `-worker` image, **one** replica, `strategy: Recreate` |
| `Service` | the http port only. The metrics port is deliberately absent |
| `Ingress` | optional |
| `ServiceAccount` ×2 | one that may create pods, one that may do nothing — that is the isolation |
| `Role` + `RoleBinding` | `pods` and `pods/log`, without `update` or `patch` |
| `Deployment` alert | optional; drains the alerts outbox |
| `CronJob` report | optional; the weekly summary |
| `Secret`, `ConfigMap` | for values passed inline, and for `brand.yaml` |

## Required values

| value | |
|---|---|
| `database.url` | or `database.existingSecret`, which is what production should use |
| `auth.user` | who logs into the interface |
| `auth.passwordHash` | from `brevis hash` — the hash, never the password |
| `auth.secret` | 32+ bytes, `openssl rand -base64 48` |

Outside `env: local` the engine refuses to boot without a credential, and the
reason is not ceremony: the interface triggers pipelines, so a `POST` to
`/workflows/<slug>/trigger` runs a `dbt build` that writes to the warehouse.

`helm show values .` lists everything else, each value with the reason next to
it.

## What it refuses, and why

A misconfiguration Kubernetes would accept and that is wrong at runtime fails at
render time instead:

- **`scheduler.replicas`, at any value.** Two replicas would not claim the same
  queue item — the dispatcher uses `FOR UPDATE SKIP LOCKED` — but they would
  both materialise the same slots, and what guards that is an idempotency key,
  not a lock. The second replica creates the same scheduled run, one window gets
  built twice, nothing fails, and it surfaces as duplicated rows in a report
  weeks later. To scale reads, raise `api.replicas`; to run more at once, raise
  `scheduler.concurrency` and `scheduler.maxPods`.
- **A missing credential outside `local`**, because the engine refuses too — the
  chart would otherwise hand you a CrashLoopBackOff on a manifest that looks
  right.
- **`auth.secret` under 32 bytes**, the same minimum the engine enforces.
- **An Ingress with no host**, which matches every request reaching the
  controller.
- **`alerts.enabled` with no webhook**: the pod would drain the outbox and
  deliver nowhere, which is worse than not running it, because the failure then
  looks announced.
- **Any value the chart does not define**, so a typo in `--set` cannot silently
  do nothing.

## Two things worth knowing

**The metrics port is not on the Service.** It is scraped from the pods, through
their annotations. The http port sits behind the login, so a scrape endpoint
there would either need a session — which no scraper has — or publish every
workflow and step name to whoever finds the path. Putting `9090` on this Service
is one `ServiceMonitor`-shaped convenience away from undoing that; an
installation that needs one gets a second, headless Service.

**The scheduler's pod is the one worth scraping.** The API serves screens; the
scheduler claims work and runs steps, so queue depth, claim latency, slots in
use and orphans recovered all live there. A dashboard built on the API's scrape
alone is a dashboard of an idle process.

## Upgrading

```bash
helm upgrade brevis . --reuse-values --set image.tag=0.13.0
```

The migration hook runs again, which is the point: an upgrade is when a new
column arrives. A failed migration Job is kept rather than cleaned up, because
its log is the only thing that says why.

Docs: <https://brevis.sh/docs/kubernetes/>
