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
  50-publish.yaml     the PostSync hook that loads the workflows
  60-agent.yaml       a pool of three agents: the runtime you keep
  70-goapp.yaml       one pod running a Go binary you wrote
  workflows/          one file per workflow; editing one IS deploying it
goapp/                that Go binary, and the image it goes into
runtime/              the pool's image: the agent, plus python and jq
```

## Two Applications, one knob

`BREVIS_GIT_REVISION` moves both — the demo tree and the chart — and defaults
to **the branch you are on**:

```bash
make cluster-up                              # this branch
BREVIS_GIT_REVISION=master make cluster-up   # somebody else's
```

It defaulted to `master`, and the failure that caused is worth keeping: a
bootstrap re-run from a feature branch re-pointed both Applications at master,
both went Synced/Healthy, and the thing being demonstrated was not in the tree.
Green, correct, and about the wrong commit.

Argo CD reads **GitHub**, not this directory, so the branch has to be pushed.
`cluster-up` refuses a revision origin does not have rather than leaving Argo CD
to say it in a `ComparisonError` nobody reads.

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

### The one thing that is not a commit

The chart's VALUES — `hosts`, `maxPods`, `env` — are in `brevis.yaml`, which the
bootstrap applies. Argo CD watches the chart's PATH in Git; it does not watch
this file. Editing it and pushing changes nothing in the cluster:

```bash
# after editing brevis.yaml
BREVIS_GIT_REVISION=$(git rev-parse --abbrev-ref HEAD) \
  docker compose -f docker-compose.cluster.yml run --rm bootstrap
```

Adding a host this way is how `goapp` below reached the engine, and finding
that out is how this paragraph came to exist: the Application synced, the chart
was Healthy, and `BREVIS_HOSTS` still listed one host.

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

## A pool, and why it is pointed at the Service

`60-agent.yaml` has `replicas: 3`. Change it to 1, push, sync, and it is
architecture C again; the number is the only difference between them.

What makes a pool spread is **the address in `BREVIS_HOSTS`**:

```yaml
hosts:
  - name: tools
    url: http://brevis-agent.dados.svc:9443     # the SERVICE, not a pod
```

A pod address sends every start to that one replica and the others never see
work. The headless Service resolves to all of them. Each replica then
advertises its OWN pod name, and the engine comes back there for a resume or a
cancel — because the agent keeps a ring and a process handle in its own memory,
and a resume that reaches a different replica is answered "not running here"
while the step runs on undisturbed.

```bash
make cluster-shell ARGS="-n dados logs brevis-agent-0" | grep -c 'step started'
```

across the three pods shows the work split.

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

## A binary you wrote, in a pod you own

`tools` is the agent with `apk add python3 jq` on top, which answers *can a step
call a tool the engine does not have*. `goapp` answers the question somebody
actually asks: **can it run my program**.

```
goapp/main.go      a Go binary that imports nothing of Brevis
goapp/Dockerfile   FROM brevis-agent:dev, plus that binary
apps/70-goapp.yaml a StatefulSet and a headless Service, in Git
```

The agent is the **floor** the image is built on, not a sidecar attached to it:
one container, one process tree, your tools on the PATH. That is why a step says
`vendor info` with no path, no shared volume and no init container.

```bash
make cluster-goapp
```

builds it, shows it in Argo CD's tree, counts the pods, runs the workflow and
counts them again. What the four steps prove, in order:

| step | what it settles |
|---|---|
| `where` | the hostname, and how long that container has been up — read from `/proc/1`, not asserted |
| `fetch` | its result goes out through `$BREVIS_OUTPUT`, the same contract a step has in a pod |
| `report` | a **different process** reads it back through `$BREVIS_INPUT` |
| `history` | the pod's own disk outlived the run |

The last one is the one a pod per step cannot do. Trigger the workflow twice:

```
this pod has served 1 runs
this pod has served 2 runs
```

### What "Brevis administers it" means here

Brevis **drives the work inside** that pod: it starts a command, follows its
output line by line, resumes a dropped stream and cancels a step. It does not
create, scale, restart or delete the pod — Argo CD does, from `70-goapp.yaml`.

That division is the mode, not a limitation of it. Your dbt version, your
licensed extractor and your base image stop being coupled to the engine's
release, and the engine stops needing permission to create workloads in a
namespace that is not its own.

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

**The pool was then run in the cluster, which is what the in-process tests
could not prove.** Three replicas, `BREVIS_HOSTS` pointed at the Service, seven
runs of `one_pod`: all `success`, zero step pods, the work split across two of
the three replicas (DNS round-robin with connection reuse is not even), and
**not one "not running here"** in any agent's log — which is what says every
resume and cancel reached the instance that had the work.

That run is also what caught the bound's third correction. The rule compared
DOMAINS, and a pod's name has one label MORE than its Service's, so it refused
the only topology in which a pool works. Every unit test passed; the cluster
would not have.

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

**2026-10-06, the pod with your own binary in it.** `make cluster-goapp` on the
same stack: `brevis-goapp-0` Running on an image built from `goapp/`, both
Applications Synced/Healthy, and `StatefulSet/brevis-goapp` in `make
cluster-tree` — Argo CD owns it, because it is in Git.

Eight pods in the namespace before the run and eight after. All four steps
`success`, every one of them on `brevis-goapp-0`:

```
where     this container has been up for 2m40s, so it was not created for this step
fetch     fetched 320/320 rows from partner-api
report    step "fetch" fetched 320 rows from partner-api on brevis-goapp-0
history   this pod has served 1 runs
```

`fetch` published `{"host": "brevis-goapp-0", "rows": 320, "source":
"partner-api"}` and `report`, a different process, read it back — the context
contract works over the wire exactly as it does in a pod. A second run said
`served 2 runs` and `up for 3m26s`: the same container, with its disk intact.

Two sharp edges of this demo were found doing it, and both are fixed above:
`BREVIS_GIT_REVISION` defaulted to `master`, so a bootstrap re-run silently
pointed a branch demo at a tree without the branch in it; and the chart's
values live in `brevis.yaml`, which Git does not carry into the cluster, so
adding a host needs the bootstrap again.
