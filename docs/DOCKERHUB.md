# Brevis

**A data orchestration runtime, in Go.**

Declarative transformation, workflow orchestration, a persistent queue, a
scheduler and an operational interface — in one binary. Every step runs as its
own Kubernetes pod, with its own image.

## Tags

| tag | base | role |
|---|---|---|
| `:<version>` | distroless | The API and the UI. It runs nothing, so it has **no shell**. |
| `:<version>-worker` | alpine | `scheduler`, `publish`, `backfill`. It **has a shell**, because a workflow's `run:` steps need one. |

The split is not fussiness. The worker runs arbitrary commands out of the
client's YAML and the API does not, so giving the API a shell would widen the
surface of the network-exposed process by the component that needs it least.

Both are built `linux/amd64` and `linux/arm64` from the same binary.

## Try it

```bash
docker run --rm areteacademy/brevis:latest version
```

A full stack — Postgres, the API, the scheduler and a worker — is one compose
file:

```bash
curl -O https://raw.githubusercontent.com/AreteAcademy/brevis/master/examples/quickstart/docker-compose.yml
docker compose up -d
open http://localhost:8080
```

## Where to go next

- **[brevis.sh](https://brevis.sh)** — the website
- **[Documentation](https://brevis.sh/docs/)** · [Quickstart](https://brevis.sh/docs/quickstart/) · [Kubernetes](https://brevis.sh/docs/kubernetes/)
- **[GitHub](https://github.com/AreteAcademy/brevis)** — source, issues, discussions
- **[Go SDK](https://pkg.go.dev/github.com/AreteAcademy/brevis/sdk)** — for writing the steps

MIT licensed.
