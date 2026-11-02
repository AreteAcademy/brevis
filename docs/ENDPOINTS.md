# Endpoints: the data leaves the way it arrived

`brevis-sql-api` serves declared, read-only SQL over HTTP. One YAML file
names the warehouse, each endpoint's path and parameters, and its SQL; every
request reads as its key's tenant; an answer is cached until a model it reads
is rebuilt.

```bash
make up-data                          # serves examples/endpoints on :8095, published to the console
curl -si 'localhost:8095/v1/orders?period=today'
curl -s 'localhost:8096/v1/chart/revenue?start_date=01/10/2026&end_date=14/10/2026'  # sql/example, with hooks
make endpoints-build                  # rebuild the model: the next request is a miss
make endpoints-demo                   # the same cycle on the host, end to end, with the gateway
```

```
client ──Bearer──► brevis-sql-api ──READ ONLY, bound args──► warehouse
                   key → tenant          (a connection in brevis.yaml)
                   params → (sql, args)
                   cache: Redis, keyed by each model's generation
```

It is the third leg of the cycle the gateway (ingest) and brevis-sql
(transform) begin, and it shares nothing with the engine at runtime: no
database, no call, no route. It appears in the console the way a gateway
does — published, as a deliberate act ([below](#in-the-console)).

## It is a binary of its own

`brevis-sql-api` is the `sql` module's second binary, released on the same
`sql/v*` tag, in its own image (`brevissh/brevis-sql-api`, distroless,
~15 MB). Not a `brevis-sql` subcommand: `brevis-sql` runs as a step in every
pipeline that builds a model, and it must not carry an HTTP server, a cache
and a compressor it never uses. `.github/scripts/sql-weight.sh` holds both
binaries to their own ceilings and fails if `brevis-sql` imports the API.

It reuses what the module already trusts: the connection registry
(`brevis.yaml`), both dialects, the read-only guard and BigQuery's dry run.

## The file

```yaml
name: metrics-api
connection: warehouse              # a name in brevis.yaml (--connections)
timezone: UTC                      # where `period` resolves; an IANA zone

listen:
  addr: :8080
  auth:
    type: bearer
    keys_from: BREVIS_API_KEYS     # the variable, never the keys

metrics:
  addr: :9090                      # its own port, never the API's

cache:                             # optional: absent, every request reads the warehouse
  type: redis
  addr_from: BREVIS_CACHE_ADDR     # host:port, redis:// or rediss://
  ttl: 6h                          # a ceiling, not the refresh; 0 is refused
  max_entry: 4MiB                  # larger answers are served and not kept

limits:                            # file-wide; an endpoint may only lower them
  timeout: 10s
  max_rows: 10000                  # at most 100000
  max_bytes_billed: 1GiB           # BigQuery; Postgres has nothing to bill
  max_response_bytes: 8MiB         # one answer's JSON, 1KiB to 64MiB
  max_in_flight: 32                # requests waiting on the warehouse, whole API

params:                            # definitions an endpoint names with `ref`
  period: {type: period, default: last_7_days}
  order:  {type: enum, values: [asc, desc], default: desc}

endpoints:
  #: Clicks per day, for the customer's dashboard.
  - name: clicks
    path: /v1/clicks               # versioned from the first day
    params:
      period: {ref: period}
      start_date: {type: date}
      end_date: {type: date}
      campaign: {type: string, max_len: 64}
      order: {ref: order}
    limits: {max_rows: 1000}
    sql_file: endpoints/clicks.sql # relative to this file; or `sql:` inline
```

Every field that cannot be honoured is refused at load, naming the setting:
an unknown key, a connection the registry does not declare, a path twice, a
`ref` to nothing, a template using a name nobody declared, a default its own
type cannot hold, a limit an endpoint raises, a credential named without
`_from`. `brevis-sql-api validate` loads the file and its SQL connecting to
nothing — the thing to run in a pull request.

## Parameters

| type | accepts | in the SQL |
|---|---|---|
| `string` | UTF-8, no NUL, at most `max_len` characters (required, ≤ 1024) | bound |
| `int` | base-10 int64, optional `min` / `max` | bound |
| `date` | `YYYY-MM-DD` and nothing an instant parser would also take | bound, as a DATE |
| `bool` | `true` or `false` | bound |
| `enum` | exactly one of `values` (identifiers) | **written into the text** — the only one |
| `period` | `today`, `yesterday`, `last_7_days`, `last_30_days`, `last_week`, `last_month`, `month_to_date` | sets `start_date` and `end_date` |

- An unknown, repeated or unparseable parameter is a `400`, never ignored:
  `?campain=x` ignored is a report with no filter, answered with a 200.
- `last_7_days` and `last_30_days` are **complete days, today excluded** —
  the data behind an endpoint arrives in batches and today is always partial.
  Weeks are ISO, Monday first. Explicit `start_date` and `end_date` win over
  a period and come as a pair.
- `default` stands in for an omitted value and is parsed exactly as a sent
  one; `required` refuses a request that omits it.

## The template: shape, never values

The SQL is a **subset of Jinja, and not Jinja**: text, `{{ name }}`,
`{# comment #}`, and `{% if %}` / `{% elif %}` / `{% else %}` / `{% endif %}`
over names joined by `not`, `and`, `or`. Everything else — filters,
attributes, literals in `{{ }}`, `set`, `for`, macros, includes, whitespace
control — is a parse error naming what was written.

```sql
select day, sum(clicks) as clicks
from marts.clicks_daily
where workspace_id = {{ tenant }}
  and day between {{ start_date }} and {{ end_date }}
{% if campaign %}  and campaign_id = {{ campaign }}  {% endif %}
group by 1
order by 1 {{ order }}
```

**`{{ x }}` is a placeholder.** It renders as `$n` on Postgres and `@x` on
BigQuery, and the value travels as a typed argument: whatever a caller sends
is data. An `enum` is written into the text only after it matched a declared
identifier exactly, checked again at render. The models brevis-sql builds
stay template-free ([`SQL-hub.md`](SQL-hub.md)); only an endpoint, which
exists to take parameters, gets a template — and only for its shape.

Refused, because there a placeholder would be text the warehouse never binds:
`{{ x }}` inside a string, a comment or a quoted name, or glued to a word;
`/* block comments */`; Postgres `$$` / `$tag$` and `E''` strings; BigQuery
`'''` and `"""`. Every render is checked again on the branches it took.

Why not a real Jinja engine: measured, gonja v2 is +88 packages and +6.8 MB
for five node kinds, and a parser that knows only five things refuses the
rest by not parsing them.

## Who may read, and as whom

```bash
openssl rand -hex 16 | tee key.txt | brevis-sql-api key hash --tenant acme
# 3f0a…c9=acme   ← one line of BREVIS_API_KEYS
```

`keys_from` names a variable of `sha256(key)=tenant` lines: the hash, never
the key, so an environment dump hands nobody a way in. Several lines per
tenant is how a key rotates. The list is read once at start; a malformed or
repeated line refuses to start, by line number, never quoted.

- **The tenant is the key's.** It is bound into every query as
  `{{ tenant }}`, a caller cannot send one (`?tenant=` is a 400), and an
  endpoint's SQL must use it **outside every condition** — a filter inside
  `{% if %}` is one a caller turns off by omitting a parameter. An endpoint
  whose data is the same for everybody says so: `tenant: none`, printed at
  start.
- Outside `BREVIS_ENV=local`, no `listen.auth` is no start — the gateway's
  rule, spelled the same way. Locally it is open and every request reads as
  tenant `local`.
- Every path but `/health` answers `401` without a valid key, unknown ones
  included, so status codes do not map the API.
- A browser never holds a key. A customer's frontend calls the customer's
  backend, which calls this.

## Read-only, by the warehouse

- **Postgres** reads inside a `READ ONLY` transaction: an `UPDATE` in a
  template is refused by Postgres, not by a parser.
- **BigQuery** dry-runs first, with the arguments: a statement it does not
  call `SELECT` is refused, one over `max_bytes_billed` is a `422` before
  anything is billed. A job whose request timed out is cancelled, and every
  job carries its deadline.
- A warehouse that can be made neither is refused.

**A READ ONLY transaction is not a SELECT-only role.** On Postgres it still
runs functions with side effects, `dblink_exec`, advisory locks and
`pg_sleep` up to the timeout. The SQL is the author's and not the caller's,
but give the API a role that can only `SELECT` the schemas its endpoints
read, and a `statement_timeout` on that role.

## The cache: invalidated by the data, not the clock

```
brevis-sql build --generations-from BREVIS_CACHE_ADDR
    └─ for each model built: SET brevis:gen:{brevis}:marts.clicks_daily <token>

request ── MGET the generations of every model the SQL reads ── one round trip
        ── key = brevis:api:{brevis}:<api>:<endpoint>:sha256(api, connection,
               endpoint, tenant, statement, typed args, generations)
        ── hit  → the stored Brotli bytes
        ── miss → one warehouse query however many ask → stored, ttl as ceiling
```

- APIs share the GENERATION keys on purpose -- one build, many readers --
  and never an ANSWER: the API's name and where its connection reaches
  (host, port and database, or the project; never a credential) are in
  every answer's key.
- The models an endpoint reads are read from its SQL at load, **in every
  branch**: a model read only when a parameter is sent still invalidates.
- A table built by something else — a Go step, dbt, **the gateway** — gets
  a generation with `brevis-sql generation bump --generations-from VAR
  --dialect postgres schema.name`. Nothing bumps a table the gateway lands:
  an endpoint reading one straight is fresh only to its ttl. Read a model
  built over it instead, or bump it on a schedule. `serve` prints, per
  endpoint, which models it waits on.
- Names are keyed as a build writes them: lowered on Postgres, and on
  BigQuery without a leading project.
- A generation that could not be written is a warning and the build stands;
  answers over that model stay until their ttl.
- A caller sending `Accept-Encoding: br` gets the stored bytes as they are,
  `Content-Encoding: br`. `ETag` is the key's hash, so a `304` holds until a
  model is rebuilt. Every answer is `Cache-Control: private, max-age=0`.
- `X-Brevis-Cache: hit | miss | bypass`. Redis unreachable is a request
  answered from the warehouse, never a failed one.
- A truncated answer, or one over `max_entry`, is served and not kept.
- Redis is spoken through a RESP2 subset in this module (`SET`, `GET`,
  `MGET`, `AUTH`, `SELECT`, TLS). Cluster works only because every key carries
  the `{brevis}` hash tag; Sentinel is not supported.

## The answer

```json
{"data": [{"day": "2026-10-05", "clicks": 412}],
 "truncated": false,
 "meta": {"endpoint": "clicks", "rows": 1, "generated_at": "2026-10-12T14:03:11Z"}}
```

Columns in the order the query named them; NUMERIC as a string, so no float
rounds a sum. `max_rows` cuts and says `truncated: true`, and so does
`max_response_bytes`: the answer stops at the last row that fits, and
`meta.rows` counts what it kept. There is no
pagination: an endpoint serves an aggregate, and wrapping its SQL to page it
would change what the warehouse plans.

| status | when |
|---|---|
| `400` | a parameter unknown, repeated, unparseable or invalid — the body names it; or the endpoint's before hook refused, in its own words |
| `401` | no key, or a key not on the list |
| `404` | authenticated, and the path is not declared |
| `405` | anything but `GET` and `HEAD` |
| `422` | BigQuery priced the query over `max_bytes_billed` |
| `429` | four requests from this tenant are already waiting (`Retry-After`) |
| `503` | the API is at `max_in_flight` (`Retry-After`) |
| `504` | `timeout` reached; the query was cancelled, or a hook was abandoned |
| `500` | the endpoint's SQL cannot render for this request, or one of its [hooks](#hooks-go-compiled-in) failed — its author's to fix; the reason is on the audit line |
| `502` | the warehouse refused — never quoting the SQL or the values |

## Hooks: Go, compiled in

An endpoint can name a **before**, which sees the request before its
parameters are read, and an **after**, which turns its rows into the
answer's `data`:

```yaml
  - name: revenue
    path: /v1/chart/revenue
    tenant: none
    params:
      start_date: {type: date, required: true}
      end_date: {type: date, required: true}
    sql_file: endpoints/revenue.sql
    hook: {before: day_first_window, after: revenue_series}
```

The file **names** a hook; it does not carry one. A hook is a Go function
registered in the binary, the gateway's idiom and for the gateway's
measurements: 93 ns/op against Starlark's 959, nothing added to the module,
and `plugin.Open` is not implemented under the `CGO_ENABLED=0` every image is
built with. **The published image has none** and refuses a file with
`hook:`, naming `sql/example` — a binary of your own, ten lines:

```go
func main() {
	hooks := api.NewHooks() // github.com/AreteAcademy/brevis/sql/api
	hooks.MustRegisterBefore("day_first_window", dayFirstWindow)
	hooks.MustRegisterAfter("revenue_series", revenueSeries)
	api.Main(hooks) // validate, serve, describe, key hash -- the same commands
}
```

A name nobody registered, an empty one, or a before named as an after is
refused at load, by `validate` as by `serve`. What it costs: **a new hook is
a rebuild and a deploy, not a config change.**

**before** — `func(ctx, *api.Request) error`. It runs behind the key and
ahead of the parameters, on a copy of the query string and the headers
**without `Authorization`**: the hook is the customer's code and has no use
for the bearer. What it leaves in `Query` is read through every check a
caller's query meets, so it can turn `14/10/2026` into a date, but a
`tenant` or a parameter nobody declared is a `400` exactly as if the caller
had sent it. `api.Refuse(msg)` answers `400` with `msg` — the one way a hook
speaks to the caller. Any other error, or a panic, is a `500` with the
reason on the audit line. The line's `params` stay what the caller sent.

**after** — `func(ctx, *api.Result) (any, error)`. It runs inside the
query's timeout, on the rows already cut by `max_rows` (`Truncated` says
so), with the parameters typed in types a binary outside the module can
name — a date as the `YYYY-MM-DD` it was sent as, an enum as its string.
What it returns becomes `data`; `truncated` and `meta` stay the API's, and
`meta.rows` is what the hook was given:

```json
{"data": {"series": [{"sku": "a", "points": [{"day": "2026-10-14", "revenue": 21}]}]},
 "truncated": false,
 "meta": {"endpoint": "revenue", "rows": 1, "generated_at": "2026-10-14T14:03:11Z"}}
```

**A hook is bounded by the endpoint's `timeout` whether it reads its context
or not**: past it the request answers `504` and the hook is abandoned. An
abandoned hook keeps one of its tenant's four slots until it really returns,
so one that hangs leaves at most four goroutines per tenant behind it, and
that tenant is told `429` — the other tenants are not. A before runs in one
of those slots too.

`max_response_bytes` holds over the whole body and **refuses rather than
cuts**: the hook's output has no rows to stop at. An error, a panic, a value
that is not JSON or an answer over the ceiling is a `500`, and nothing is
cached.

**The cache keeps what the after made.** It runs on a miss, once per key
however many are asking, and a hit serves its answer without asking the
warehouse or the hook. So **an after must be a function of its `Result`**:
one that reads the clock or the network answers every later caller with what
was true for the first. The key holds the after's name, the binary's
build (a clean VCS revision, or else the executable's sha256) and every parameter the hook is shown — a
`format` declared for the hook alone never reaches the SQL, and is still a
different answer — so a deploy that changes what a hook does under the same
name is a miss, not the old binary's answer.

## What it counts

On `metrics.addr` only: `brevis_sql_api_requests_total{endpoint,status}`,
`rows_total`, `bytes_billed_total`, `cache_requests_total{endpoint,result}`,
and `request_seconds` / `warehouse_seconds` histograms — written by hand, as
`brevis-sql serve`'s are, because the client library is over both of this
module's ceilings. **No tenant in any label**, and unauthorised requests are
counted under `endpoint="-"`, never by the path they guessed. Hooks add
`hook_total{endpoint,hook,outcome}` — `ok`, `refused`, `error`, `panic`,
`oversize`, a closed set and never an error's text — and `hook_seconds`,
counted where a hook runs: an after collapsed into one miss counts once, and
a hit runs none.

One JSON audit line per request on stdout: endpoint, tenant, the parameter
**names** sent, an `args_hash` (an HMAC under a per-process key — a plain hash
of a date is a dictionary away from it), status, rows, bytes, cache result,
and `hook` when one decided the status. Never a value of the API's making —
but a hook's error is on the line as its author wrote it, cut at 512 bytes,
so a hook that puts a value in its error puts it in the log. A panic is
logged by its type and where it happened, never by the value it carried.

## In the console

```bash
brevis-sql-api describe --config api.yaml --out manifest.json   # opens nothing
brevis endpoints publish manifest.json
brevis endpoints unpublish metrics-api
```

The manifest carries each endpoint's parameters resolved through their refs,
its effective limits, the catalog targets its SQL reads, and the `#:` notes
its author marked — never the SQL, never a variable's value, never an
ordinary comment. Beside the endpoints the console keeps the API's own
settings: the connection's name, the dialect and the cache -- its type, its
`ttl` and the NAME of the variable its address is read from -- so a page can
say how long an answer is cached and print the command that clears it.
The engine refuses the whole manifest when anything in it
is shaped like a credential. On Kubernetes the act is a Job:
[`deployments/kubernetes/endpoints-publish-job.yaml`](../deployments/kubernetes/endpoints-publish-job.yaml).

**Data → Endpoints** lists every published path; an endpoint's page is its
contract — parameters, limits, notes, the models it reads, a `curl` with the
key as a placeholder. A model's page lists the endpoints over it under
**Read by**. Traffic stays on `/metrics`, live.

**Try it** runs an endpoint from its page: one field per parameter, typed as
the parameter is, and the answer as a grid with its status, cache result and
time. The console calls the API from the server -- the browser never holds a
key -- with what the engine is given:

```bash
BREVIS_ENDPOINTS_URLS="metrics-api=https://api.internal:8080, other=http://other:8080"
BREVIS_ENDPOINTS_KEY=<a key from the API's key list>
```

**One key, so one tenant, for every console login.** That is why Try it
exists only on a console that asks who you are, as the SQL tabs do, and only
for an API it has an address for. Nothing is called until Run is pressed: a
call is a warehouse query. `make up-data` points the console at the example
API, which is local and open, so no key.

## What it does not do

- **No browser access.** A static key in a browser is public.
- **No writes, no ad-hoc SQL.** Arbitrary queries are `/sql`, behind a
  console login.
- **No pagination, no streaming.**
- **No connection pool yet.** Each request opens its own warehouse
  connection; `max_in_flight` keeps that under the database's limit.
