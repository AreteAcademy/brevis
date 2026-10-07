# Brevis gateway

An HTTP endpoint that lands data: validate, give it an identity, batch it,
deliver it. Configured entirely by a YAML file.

```
POST /v1/clicks  →  decode  →  hook  →  ingestion_id  →  batch  →  202
                                                          ↓
                                            workers  →  Pub/Sub | Postgres | …
```

```bash
docker run -p 8080:8080 \
  -v ./gateway.yaml:/etc/brevis/gateway.yaml:ro \
  areteacademy/brevis-gateway:0.7.1-slim
```

## Tags

| tag | carries | pull |
|---|---|---|
| `0.7.1` | six sinks, S3 and GCS, Redis and memcached | 18 MB |
| `0.7.1-slim` | `postgres`, `auto_table`, local `files` | **4.8 MB** |

The sinks are compiled in, so the import list is the selection: a binary that
never imports the BigQuery driver does not carry BigQuery, or Arrow, or the
Storage Write API. 864 packages against 232. A build with a different pair is
fifteen lines — see `cmd/gateway-slim` in the repository.

There is no `latest-slim`. `latest` is already a tag nobody should deploy.

## What it does

**The file is the contract.** Streams, paths, formats, identity, retry,
destination and dead letter — every field refused when it cannot be honoured. A
gateway that starts on a config it half understood drops events for a reason
nobody can see.

**Every record leaves with an `ingestion_id`**: a frozen UUID v5 over
`provider|entity|source_key|record_ts`. It is the same id a Brevis batch
pipeline computes for the same record, so a row this lands and a row a pipeline
lands are one row. It also makes a retried `POST` the same record.

**The request never waits for the sink.** It decodes, runs the hook, computes
the identity, appends to a buffer and returns. Delivery happens on a bounded
worker pool; when the buffer and queue are full the answer is `503` with
`Retry-After`, and retrying it cannot create a duplicate.

**A refused batch is retried and then buried**, with the reason, the sink and
the time on every record. A stream with no `dead_letter` is refused at load.

**Six destinations**: `pubsub`, `postgres`, `mysql`, `bigquery`, `redshift`,
`files` (a directory, `gs://` or `s3://`). Four are proven end to end against
the real thing; BigQuery and Redshift have no emulator, so what is tested there
is the config and the driver beneath it.

**`auto_table`** routes each event to the table its own payload names, creating
it if absent — four fixed columns with the document in a `JSON` column.

**Eleven Prometheus series**, on their own port. Never the ingest one: that port
is public by design, and a `/metrics` on it would publish every stream name and
destination.

## What it does not do

- **No UI.** It shares no database with the Brevis console.
- **`durability: memory` only.** A crash loses what is in flight, and `disk` is
  refused by name rather than accepted and treated as memory.
- **This image has no hooks.** A hook is Go, compiled in, so a hook of your own
  means a binary of your own — ten lines around `gateway.Main`.
- **`202`, not `200`.** It has accepted the events; the tier that earns a `200`
  is the one that has written them down, and it is not built yet.

## Documentation

Full reference, configuration and the reasoning behind each refusal:
**[docs/GATEWAY.md](https://github.com/AreteAcademy/brevis/blob/master/docs/GATEWAY.md)**

MIT licensed. Built at [Aretê Academy](https://areteacademy.com.br/).
