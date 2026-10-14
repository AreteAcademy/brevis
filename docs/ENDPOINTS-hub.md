# brevis-sql-api

Declared, read-only SQL served over HTTP. One YAML file: a connection, each
endpoint's path, its typed parameters and its SQL. Each key is one tenant, and
an answer is cached until a model it reads is rebuilt.

```
GET /v1/orders?period=today  →  key → tenant  →  {{ x }} as $1, never as text  →  READ ONLY  →  JSON
                                                  └─ Redis, Brotli, keyed by each model's generation
```

```bash
docker run -p 8080:8080 \
  -v ./api:/etc/brevis:ro \
  -e WAREHOUSE_DSN=postgres://reader:...@db:5432/warehouse \
  -e BREVIS_API_KEYS="$(echo "$KEY" | docker run -i --rm areteacademy/brevis-sql-api:0.6.0 key hash --tenant acme)" \
  areteacademy/brevis-sql-api:0.6.0 \
  serve --config /etc/brevis/api.yaml --connections /etc/brevis/brevis.yaml --addr :8080
```

## Tags

| tag | carries |
|---|---|
| `0.6.0` | hooks: a before and an after per endpoint, in a binary of your own (this image has none) |
| `0.5.0` | Postgres and BigQuery, the Redis cache, Prometheus |

The same `sql/v*` tag and version as `areteacademy/brevis-sql`: one module,
two binaries. Distroless, non-root, about 15 MB. There is no `latest` to deploy.

## What it does

- **Values are bound, never written into the SQL.** The template is a Jinja
  subset; `{{ x }}` is a placeholder and its value a typed argument. Only an
  `enum` is inlined, after an exact match.
- **Each key is one tenant**, and `{{ tenant }}` must filter outside every
  condition. Outside `BREVIS_ENV=local` it refuses to start without keys.
- **Read-only by the warehouse**: a `READ ONLY` transaction on Postgres, a
  priced dry run on BigQuery.
- **Bounded**: timeout, rows, response bytes, bytes billed, requests in flight.
- **A cache that follows the data**: `brevis-sql build --generations-from`
  writes a generation per model; the next request after a rebuild is a miss.

## What it does not do

No writes, no ad-hoc SQL, no pagination, no browser access — a static key in a
browser is public.

## Documentation

**[docs/ENDPOINTS.md](https://github.com/AreteAcademy/brevis/blob/master/docs/ENDPOINTS.md)**

MIT licensed. Built at [Aretê Academy](https://areteacademy.com.br/).
