# The proofs that need infrastructure

**Written on** 2026-09-07 · **Base** engine `v0.7.0`, `sdk/v0.53.0`
**Status** proposed — not started

Three code paths that CI does not exercise. None is a bug; each is a **claim
with no proof**, and this repository has been bitten four times by exactly that
shape — the engine had no CI at all, the release workflow had never run, the
queue's tests ran only on a laptop, and two features shipped switched off.

They are grouped here because they share a cause: the test exists and the
environment to run it does not.

---

## 1. The Kubernetes log path — the one that is a real gap

`Logs(pod, follow=true)` is the piece of the phase pipeline with **no proof at
all**. Everything else on that path is covered end to end by
`TestIntegrationStagesReachTheDatabaseThroughARealBinary` — a real SDK binary,
a real OS pipe, a real Postgres — but through the **local** executor.

So the `@brevis:` line has never crossed a kubelet.

What that means concretely: the stages on the graph, the SDK badge, and now the
context in the pod's termination message all arrive through code that only ever
ran against a fake API server. `internal/execution/kubernetes/executor_test.go`
drives a `fakeAPI` — good tests, and they prove the state machine, not the wire.

### `kind` in CI is the route

```yaml
- uses: helm/kind-action@v1
- run: |
    kind load docker-image brevis:test
    kubectl apply -f deployments/kubernetes/rbac.yaml
    go test -tags=cluster ./internal/execution/kubernetes/... -run Cluster
```

One workflow, one job, roughly four minutes. The test itself is small: publish a
workflow whose step prints a marked line and writes a termination message, run
it, and assert the phases and the context reached Postgres.

**Not this machine.** Its kubectl contexts are real EKS clusters, production
among them, and they are not a test bed. That is recorded so nobody tries.

### What it would have caught

The `terminationMessagePath` gap, three days ago, in the commit that introduced
it rather than in a conversation two days later.

---

## 2. The integration tests that skip on credentials

21 of them, waiting on `GCP_CREDENTIALS` and the `BREVIS_IT_*` variables. Only
BigQuery and GCS are affected; MinIO, Postgres and MySQL already run in CI.

| what | needs |
|---|---|
| `sdk/load/integration_test.go` | a BigQuery dataset the CI service account can write |
| `sdk/from/integration_test.go` (GCS cases) | a bucket, `BREVIS_IT_BUCKET` |
| `sdk/store/gcs` | the same bucket |

**This is the repository owner's to add, not a code change**: a service account,
a dataset, a bucket, and the JSON as a repository secret.

The honest note on scope: these cover the loader's real behaviour against
BigQuery — `DedupMerge`, the staging path, `CreateSQL`. `TestIntegrationCreateSQLRunsTheCallersDDL`'s
own comment says CreateSQL had existed since `v0.9.0` **and had never been
executed against BigQuery**. That is what a skipped integration test costs.

---

## 3. The cross-language test skips without Python

`TestIntegrationPythonAndShellExchangeContext` is the acceptance criterion for
the whole context feature — bash publishes, Go reads and publishes, Python reads
and publishes, and a fourth step reads all three.

It calls `t.Skip("no python3 on this machine")`.

CI runners have `python3`, so it runs there today. But the skip means a machine
without Python **reports green having proved nothing**, and that is precisely
the shape this document is about.

**The fix is one line**, and it belongs in `test.yml` rather than in the test:

```yaml
- name: The cross-language test must not skip here
  run: |
    cd . && go test ./internal/application/execution/ \
      -run TestIntegrationPythonAndShell -v 2>&1 | tee /tmp/out
    grep -q -- "--- PASS" /tmp/out || { echo "::error::it skipped"; exit 1; }
```

Asserting it PASSED rather than that it did not fail. `go test` exits 0 on a
skip, which is the whole problem.

---

## Order, and what each one buys

| | cost | buys |
|---|---|---|
| **3. the Python skip** | one line | the acceptance criterion stops being optional |
| **1. `kind` in CI** | one workflow, ~4 min per run | the only production path with no proof |
| **2. GCP credentials** | a service account and three secrets | 21 tests, and the loader's real behaviour |

Do 3 first because it is a line. 1 is the one that matters. 2 is not a code
change at all.

## How each is proven

The same rule the rest of the repository follows: **revert the thing and watch
the check go red.**

- For 3: make the test skip unconditionally, and the CI step fails.
- For 1: remove `terminationMessagePath` from `BuildPod`, and the cluster test
  fails where the unit test cannot.
- For 2: point `BREVIS_IT_BUCKET` at a bucket that does not exist, and the
  suite fails rather than skipping.
