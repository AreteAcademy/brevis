# A local cluster, with Argo CD as the UI

A Kubernetes cluster on your machine, with Argo CD on it and **nothing
deployed**. That is the starting point: Brevis arrives the way a client's
would — deployed from Git by Argo CD — rather than by a Makefile.

```bash
make cluster-up     # k3s + Argo CD, nothing on them
make cluster-ui     # the URL and the admin password
make cluster-down   # gone, volumes included
```

`make cluster-shell ARGS="get pods -A"` runs `kubectl` against it.

## Argo CD, not Argo Workflows

The next person to read this will assume the other one, so: **Argo CD is a
deployer.** You point it at a repository and it makes the cluster match. Argo
Workflows is an orchestrator — it would be a second one competing with Brevis
for the same job, and nothing here uses it.

## What this is for

Watching where Brevis sits, which is a question a document answers badly:

- **a pod per step** — pods appearing and dying under a run, each with its own
  image and its own limits;
- **one pod you keep** — the same work with `host:`, and **no pod appears**;
- **who owns what** — Argo CD's tree holds the engine and the agent, and the
  step pods are *not in it*. They appear underneath, owned by Brevis, in the
  one namespace its Role names:

  ```yaml
  kind: Role                  # not ClusterRole
    namespace: dados
    - resources: ["pods"]      verbs: [create, get, list, watch, delete]
    - resources: ["pods/log"]  verbs: [get]
  ```

## Things worth knowing before they surprise you

**k3s runs privileged.** A container running containerd, managing cgroups and
mounting filesystems needs it, and every local-cluster tool does the same one
layer down. It is a throwaway cluster on a developer's machine.

**Argo CD is pinned**, to `v2.13.3`. `stable` moves, and a demo that breaks
when upstream releases is a demo nobody trusts the next time it breaks for a
real reason. Bump `ARGOCD_VERSION` to move it.

**The UI is plain HTTP**, which is correct here and nowhere else: the
alternative is a self-signed certificate every browser argues with on the
first visit.

**Ports are overridable**, because 6443 is very often already taken:

```bash
BREVIS_K8S_PORT=16443 BREVIS_ARGOCD_PORT=30081 make cluster-up
```

## This is NOT in CI

A cluster per push costs minutes on every run, and the unit and integration
suites are the gate. So this is run by hand, and this line is the honest
record of when somebody last did:

**Verified by hand: 2026-10-06** — `make cluster-up` on Docker for Mac,
k3s v1.31.5, Argo CD v2.13.3. The API answered, the UI answered on :30080,
and `kubectl -n argocd get applications` found none, which is the point.
The bootstrap was run twice to check it is idempotent; the second run changed
nothing and exited 0.
