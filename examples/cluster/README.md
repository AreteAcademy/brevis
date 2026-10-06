# The demo stack, deployed from this directory

Argo CD watches this path and makes the cluster match it. Nothing here is
applied by hand: `make cluster-up` installs Argo CD, points it at these files,
and from then on **every change is a commit**.

```
root.yaml      the Application for this directory        ─┐ applied by the
brevis.yaml    the Application for the Helm chart        ─┘ bootstrap, once
apps/
  00-namespace.yaml   where Brevis and its step pods live
  10-postgres.yaml    the demo's database, in Git like everything else
  40-ui-nodeport.yaml the UI, reachable from the host
```

## Two Applications, one knob

`BREVIS_GIT_REVISION` moves both — the demo tree and the chart — and defaults
to `master`:

```bash
BREVIS_GIT_REVISION=my-branch make cluster-up
```

They are two rather than one because of exactly that. An Application nested
inside `apps/` carries whatever revision is written in the file, so testing a
chart change would mean editing a tracked file. Two Applications templated by
the bootstrap means pointing them apart is a flag away instead.

## The chart, not a copy of it

`brevis.yaml` deploys `deployments/helm/brevis` — the artefact a client
installs, including its RBAC. A demo that deployed an edited version of it
would demonstrate something nobody has.

Its values are the demo's: `env: local` so no credential is required, one API
replica, `maxPods: 3` because a laptop is the cluster, and
`keepPodsOnFailure: true` — the opposite of the production default, and the
right choice here, since the reason to run this is to look at what happened.

**The UI is a Service of the demo's**, not a value in the chart. The chart's
schema refuses `api.service.nodePort`, and it is right to: a NodePort is a
decision about one cluster's network, and a chart should not grow a field so a
demo can reach a page.

## Editing a workflow IS deploying it

The workflows live in `apps/workflows/`. kustomize builds a ConfigMap from
those files, and the publish Job is a **PostSync hook** — Argo CD deletes the
previous one and runs it again after every sync.

```bash
# change a workflow, then
git commit -am "a different schedule" && git push
# and sync, from the UI or:
make cluster-shell ARGS="-n argocd annotate app brevis-demo argocd.argoproj.io/refresh=hard --overwrite"
```

`deployments/kubernetes/publish-job.yaml` spells out the three commands this
replaces: create the ConfigMap `--from-file`, delete the Job, apply it again.

Two details that are not decoration. The generated ConfigMap carries a content
HASH, so editing a workflow makes it a new object and kustomize rewrites the
Job's reference to match. And the hook's delete policy is
`BeforeHookCreation` rather than `HookSucceeded`: a Job with a fixed name and
no hook is created once and never runs again, which looks exactly like the
change not taking.

`--prune` makes the folder the source of truth. A workflow that leaves Git
leaves the database, schedule and all — without it, deleting a YAML would
leave its schedule creating runs for ever.

## Watching a pod per step

Two terminals. In one:

```bash
make cluster-watch
```

In the other:

```bash
make cluster-run            # WORKFLOW=... for another one
```

What you see is the model:

```
pod-per-step-large-a3c60fa3   Pending   python:3.12-slim   100m   128Mi
pod-per-step-small-d8145013   Pending   alpine:3.20         50m    32Mi
```

**Two pods at once, two different images, two different sizes** — each step
brought its own runtime and asked for its own share. Nothing waited for a
worker to be free, because there is no worker. And when the run ends both are
gone.

A failed step's pod STAYS, because this demo sets `keepPodsOnFailure: true` —
the opposite of the production default, and the right choice when the reason to
run this is to look at what happened.

## The same work, with no pod at all

`one-pod.yaml` is `pod-per-step.yaml` with `image:` replaced by `host: tools`.
Everything else is the same. With `make cluster-watch` open:

```bash
make cluster-run WORKFLOW=pod_per_step    # four pods appear and die
make cluster-run WORKFLOW=one_pod         # nothing happens
```

The second run succeeds and creates **no pod**. The work went to
`brevis-agent-0`, which was already up, and its own log shows the four steps
arriving. Two of them start six milliseconds apart — the parallel pair, in the
same container, which is the concurrency nobody is bounding.

**The agent image is built from this tree**, because `…:0.15.2-agent` ships for
the first time in the next release:

```bash
make cluster-dev-image      # builds it and imports it into the cluster
```

`imagePullPolicy: Never` is what stops the kubelet looking for a tag nobody
published.

### What it gives up, in the same frame

`one-pod.yaml` has no `resources:`, and that is not an omission. On a host it
means nothing: the step gets whatever the agent's pod has. A pod asks for its
own share; a guest does not. Scaling the StatefulSet to 3 makes it architecture
D — the engine still reaches the instance that took each step, because
`--advertise` is built from the pod's own name.

### What is NOT in Argo CD's tree

```bash
make cluster-tree
```

```
ConfigMap/brevis-workflows-…   Namespace/dados   Secret/brevis-db
Service/brevis-ui   Service/postgres   Deployment/postgres   Job/brevis-publish
```

Argo CD owns the stack. **It does not own the step pods** — they appear
underneath it, created by Brevis, in the one namespace its Role names, and they
are gone when the step ends. That picture is the answer to "what does Brevis
need permission to do in my cluster", and it is a better answer than a
paragraph.

## Verified by hand

**2026-10-06** — `BREVIS_GIT_REVISION=engine/agent-instance make cluster-up` on
Docker for Mac. Both Applications Synced/Healthy, `brevis-api` and
`brevis-scheduler` Running, `/health` answering `{"status":"ok"}` on :30081,
Argo CD on :30080, and the scheduler logging `running steps as pods`. Nothing
was applied by hand after the bootstrap.

A run was then triggered through `make cluster-run`: before it, no step pods;
during it, `pod-per-step-large` on `python:3.12-slim` with 100m/128Mi and
`pod-per-step-small` on `alpine:3.20` with 50m/32Mi, both Pending at the same
moment; after it, none — and `runs` in the database says `success`, which is
what `keepPodsOnFailure: true` makes an absent pod mean. `make cluster-tree`
listed seven objects and not one of them was a step pod.

`one_pod` was then run: the same four steps, `success`, and **zero step pods
created** — `task_runs` has all four, and `brevis-agent-0`'s own log shows them
arriving, with the parallel pair six milliseconds apart.

Then a workflow was edited in Git — one tag added — pushed, and synced: the
ConfigMap's hash changed, the hook ran a second time, and the engine's database
held `"Tags": ["demo", "edited-in-git"]`. That is the claim this directory
makes, checked end to end rather than described.

Two product bugs were found by running this rather than reading it, and both
are fixed: five manifests lacked the `runAsUser` the other five had, so the
chart's migration hook could not start; and the chart's `appVersion` said
`0.13.0` while `VERSION` said `0.15.2`, so it deployed an engine two releases
behind. **Nothing checks that those two agree** — a gate for it is still open.
