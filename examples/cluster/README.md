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

## What to watch

```bash
make cluster-status                        # what Argo CD has, and what runs
make cluster-shell ARGS="-n dados get pods -w"
```

The thing worth seeing is **what is NOT in Argo CD's tree**. It owns Postgres,
the engine and the scheduler. A step's pod is not there — it appears
underneath, created by Brevis, in the one namespace its Role names, and it is
gone when the step ends.

## Verified by hand

**2026-10-06** — `BREVIS_GIT_REVISION=engine/agent-instance make cluster-up` on
Docker for Mac. Both Applications Synced/Healthy, `brevis-api` and
`brevis-scheduler` Running, `/health` answering `{"status":"ok"}` on :30081,
Argo CD on :30080, and the scheduler logging `running steps as pods`. Nothing
was applied by hand after the bootstrap.

Then a workflow was edited in Git — one tag added — pushed, and synced: the
ConfigMap's hash changed, the hook ran a second time, and the engine's database
held `"Tags": ["demo", "edited-in-git"]`. That is the claim this directory
makes, checked end to end rather than described.

Two product bugs were found by running this rather than reading it, and both
are fixed: five manifests lacked the `runAsUser` the other five had, so the
chart's migration hook could not start; and the chart's `appVersion` said
`0.13.0` while `VERSION` said `0.15.2`, so it deployed an engine two releases
behind. **Nothing checks that those two agree** — a gate for it is still open.
