# The ingestion gateway

An HTTP endpoint that lands data. `POST` an event, it is shaped by a hook,
given an identity, batched, and delivered to a destination.

```bash
docker compose up -d pubsub topic gateway

curl -X POST http://localhost:8090/v1/clicks \
  -H 'Content-Type: application/json' \
  -d '{"event_id":"e-42","occurred_at":"2026-09-24T12:00:00Z",
       "host":"acme.example.com","region":"sa-east-1","amount":99.9}'
# → 202 {"accepted":1,"rejected":null}
```

What the subscriber then receives:

```json
{
  "amount": 99.9,
  "event_id": "e-42",
  "host": "acme.example.com",
  "ingestion_id": "7aa2549c-0503-5f93-ab58-0ab78e7eb555",
  "occurred_at": "2026-09-24T12:00:00Z",
  "region": "sa-east-1",
  "tenant": "acme"
}
```

attributes: `{region: sa-east-1, tenant: acme}`

The payload is what was sent plus what the hook added. Nothing Brevis-shaped
wraps it, and only the attributes the file named travel beside it: the topic's
contract belongs to whoever owns the topic.

## It is not the engine, and shares nothing with it

No database, no queue, no migration, no console. That is the design and not an
omission — `engine-weight.sh` states the rule both follow:

> *the data drivers live in the TASKS' pods, which are other images*

The engine orchestrates and never touches customer data. The gateway does
nothing else, on every request. They are separate modules, separate binaries,
separate images, and the engine's package count is unchanged by the gateway
existing.

| | engine | gateway |
|---|---|---|
| shape | batch, scheduled | online, per request |
| unit | a run | an event |
| the SLO | did the run finish | p99 of `POST`, nothing lost on a 200 |
| scaling | one scheduler | N stateless replicas |

## Why there is nothing in the console

**There is nothing to see today.** The console reads the engine's Postgres, and
the gateway never writes to it.

Making it appear there is possible, and the shape matters more than the effort:

- **The gateway writing to the engine's database.** Cheap to build and the worst
  of the options: it couples an always-on data plane to the orchestrator's
  store. The question it forces — *does the gateway stop accepting when that
  Postgres is down?* — has no good answer.
- **The console querying the gateways over HTTP.** Which replica? They each hold
  their own buffer, so the answer is "all of them, and add up", and the console
  grows a service discovery problem.
- **Publishing the config, the way a workflow is published.** `brevis publish`
  already takes a file and stores a definition the console renders. A gateway
  config is a file of exactly that kind, and publishing is a deliberate act
  rather than a runtime dependency: the console shows what is *configured* to
  receive data, and the gateway keeps accepting whether or not that database is
  reachable.

The third is the one worth doing, and it is worth being clear about what it
gives: the **configuration**, not the traffic. Live counters — accepted,
rejected, delivered, buffered, sink failures — belong in `/metrics`, beside the
engine's, which is where an operator already watches for trouble. A console
page that showed a number a minute old would be worse than one that sends you
to the dashboard that updates.

Neither is built. Both are listed in the plan's build order, and neither is in
the way of using the gateway.

## When the sink refuses

It is retried — four attempts over roughly seven seconds by default, doubling
with jitter — and then the batch goes to the **dead letter**, with the reason
attached to every record:

```json
{
  "event_id": "dl-9",
  "host": "x.y",
  "ingestion_id": "84aaee4b-66af-5cc0-b97b-6856dec95b25",
  "_dead_letter_reason": "… rpc error: code = NotFound desc = Topic not found",
  "_dead_letter_sink": "pubsub:brevis-local/nao-existe",
  "_dead_letter_at": "2026-09-24T22:02:32Z"
}
```

The reason travels **on the record** and not only in a log, because whoever
finds this file later has the events and not the log, and *why is this here* is
their first question. The `ingestion_id` travels too, so a replay lands where
the original would have.

**A stream with no `dead_letter` is refused at load.** Defaulting it to silence
would put the decision where nobody makes it — and a refused batch that is only
a log line is losing data quietly, which is the one failure this exists not to
have.

`files` reads a directory, `gs://` and `s3://`, so the dead letter is a folder
on a laptop and a bucket in production with no change to the gateway. In compose
it is a **volume**: written inside the image it would die with the container,
which is a slower way of losing the events it exists to keep.

## Configuration

See [`gateway/example/gateway.yaml`](../gateway/example/gateway.yaml). Every
field is refused when it cannot be honoured, and two refusals are worth knowing:

**`buffer.durability` accepts only `memory` today**, and refuses `disk` **by
name** rather than accepting the word. Somebody writing `disk` believes their
events survive a crash, and behaving like `memory` while agreeing would be the
one failure that field exists to prevent.

**`identity` requires all four fields.** The `ingestion_id` is a UUID v5 over
`provider|entity|source_key|record_ts` and the formula is frozen, so leaving one
out produces a *different* id rather than a weaker one.

## The hook is Go, compiled in

```go
hooks := gateway.NewHooks()
hooks.MustRegister("enrich_clicks", enrichClicks)
gateway.Run(hooks)

func enrichClicks(e map[string]any) (map[string]any, error) {
    host, _ := e["host"].(string)
    e["tenant"] = strings.Split(host, ".")[0]
    return e, nil          // returning nil DROPS the event, on purpose
}
```

The YAML names a hook; it does not carry one. Measured before choosing: a Go
function is 93 ns/op against Starlark's 959 and yaegi's 1,281, for nothing added
to the binary — and `plugin.Open` is not an option at all, because under
`CGO_ENABLED=0`, which is the build every artifact here ships with, it returns
`plugin: not implemented`.

What it costs, where somebody will read it: **adding a hook is a rebuild and a
deploy, not a config change.** That is the right trade while the hooks are
written by the people who ship the binary, and the wrong one the day a customer
has to change one without a release.

## 202, not 200

The gateway has **accepted** the events. With `durability: memory` that is the
whole of what it can honestly claim — the tier that earns a `200` is the one
that has written them down, and it is not built yet.

What is in a buffer at shutdown is delivered, not dropped: `memory` already
loses on a crash, and losing on a clean stop as well would make the tier useless
rather than merely limited.
