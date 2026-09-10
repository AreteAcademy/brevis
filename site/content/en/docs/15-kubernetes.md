---
title: Kubernetes
description: How to deploy the engine, with what permissions, and what to check afterwards.
group: Operations
order: 15
slug: kubernetes
---

A typical installation has **two Deployments** of the same binary — the API and
the scheduler — plus a Postgres.

## Install it with Helm

The chart is in the repository, at `deployments/helm/brevis`. Four values are
required and everything else has a working default:

```bash
helm install brevis ./deployments/helm/brevis \
  --namespace data --create-namespace \
  --set database.url="postgres://brevis:pw@postgres/brevis?sslmode=require" \
  --set auth.user=admin \
  --set auth.passwordHash="$(brevis hash)" \
  --set auth.secret="$(openssl rand -base64 48)"
```

That is the whole install: the migrations run first as a hook, both Deployments
come up with the right image each, and the scheduler gets a ServiceAccount that
may create pods while the task pods get one that may do nothing.

To reach the interface:

```bash
helm upgrade brevis ./deployments/helm/brevis --reuse-values \
  --set api.ingress.enabled=true \
  --set api.ingress.host=brevis.example.com \
  --set api.ingress.tls.enabled=true \
  --set api.ingress.tls.secretName=brevis-tls
```

In production, keep the connection string out of the release: put it in a
Secret and pass `--set database.existingSecret=brevis-db`. Passed inline, it
lands in the release's stored manifest, which anybody who can read Secrets in
that namespace can read.

`helm show values ./deployments/helm/brevis` lists every value with the reason
next to it.

### What the chart refuses

Anything it cannot make safe fails at render time, with a message that says what
would break — not at 3am, in a run that produced duplicate rows:

| refused | because |
|---|---|
| `scheduler.replicas` at all | two would materialise the same slots. `FOR UPDATE SKIP LOCKED` stops them claiming the same queue item; nothing stops them creating the same scheduled run, and a `dbt build` would run twice over one window with nothing failing |
| a missing credential outside `local` | the engine refuses to boot without one, so the chart would be handing you a CrashLoopBackOff |
| `auth.secret` under 32 bytes | it signs the session cookie, and the engine enforces the same minimum |
| an Ingress with no `host` | it would match every request reaching the controller — in a shared cluster, answering for somebody else's hostname |
| `alerts.enabled` with no webhook | the pod would drain the outbox and deliver nowhere, which is worse than not running: the failure then looks announced |
| a value the chart does not define | a typo in `--set` otherwise does nothing, silently |

`.github/scripts/helm-check.sh` asserts all of that on the rendered output, plus
the invariants above — one scheduler, `pods/log` present, migrations first, the
metrics port off the Service.

## Without Helm

Seven manifests are in `deployments/kubernetes/`, ready for `kubectl apply -f`
after you edit the namespace and the image tag:

```bash
kubectl apply -f deployments/kubernetes/rbac.yaml
kubectl apply -f deployments/kubernetes/api.yaml
kubectl apply -f deployments/kubernetes/scheduler.yaml
```

They carry the same decisions as the chart, with the reasoning in comments, and
`alert.yaml`, `report.yaml` and `publish-job.yaml` besides. The sections below
walk through what each one contains.

## The two roles

| role | command | image | replicas |
|---|---|---|---|
| API + interface | `serve` | `:0.11.2` (distroless) | as many as you like |
| scheduler + queue | `scheduler` | `:0.11.2-worker` (alpine) | **one** |

The API is distroless because it only serves HTTP: it executes nothing, so it
needs no shell. The worker is alpine because `run:` steps need one.

:::warning One scheduler replica
Two processes materialising the same schedule create duplicate runs. If you need
availability, use `replicas: 1` with `strategy: Recreate`.
:::

## Migrations

Run them as a Job before the deploy, never at `serve` boot:

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
          image: daniel3843/brevis:0.11.2
          args: ["migrate", "up"]
          envFrom:
            - secretRef: {name: brevis-db}
```

## API Deployment

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
          image: daniel3843/brevis:0.11.2
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

:::note Why the two probes differ
`/health` does **not** query the database; `/ready` does, and names the
dependency that failed. If liveness depended on Postgres, a database wobble
would make Kubernetes **kill** the API pods instead of merely removing them from
the load balancer — and recovery would get slower exactly when the system is
already under stress.
:::

## Scheduler Deployment

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
          image: daniel3843/brevis:0.11.2-worker
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

`BREVIS_PODS=on` **requires** a cluster: with no service account mounted, boot
fails instead of silently falling back to local execution.

## Permissions

The scheduler creates, watches and deletes pods in the execution namespace.
Nothing beyond that:

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

`pods/log` is what allows following a step's output into the interface. Without
it, the run works and the log stays empty.

## After the deploy

```bash
kubectl -n data get pods -l app=brevis-api
kubectl -n data exec deploy/brevis-api -- brevis version
curl -fsS https://brevis.example.com/ready
```

Trigger a simple workflow from the interface and confirm that the step's pod
appears and disappears:

```bash
kubectl -n data get pods -w
```

## When a step stays Pending

It is almost always one of these three:

| cause | how to confirm |
|---|---|
| taint without toleration | `kubectl describe pod` shows `node(s) had untolerated taint` |
| `nodeSelector` with no node | `describe` shows `didn't match Pod's node affinity` |
| resource unavailable | `describe` shows `Insufficient cpu` or `memory` |

To inspect a failed step's pod rather than watch it disappear:

```bash
BREVIS_POD_KEEP_ON_FAILURE=true
```

Leave it off in production — stopped pods consume quota.

## Next steps

- [Pod per step](/docs/pod-per-step/) — the execution model
- [Configuration](/docs/configuration/) — every `BREVIS_POD_*` variable
- [Observability](/docs/observability/) — what to scrape, and from which process
