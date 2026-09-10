-- +goose Up
-- What every load already measured, kept so the TREND can be read.
--
-- Not a new measurement. Every number here already crosses the `@brevis:` pipe
-- and is already stored, in `task_runs.etapas` -- the same JSONB the run's page
-- draws its phase boxes from. What was missing was the ability to ask "is this
-- pipeline getting slower" without reading a year of it.
--
-- A TABLE and not a query over that JSONB, and the difference was measured
-- rather than assumed. The SAME question -- ninety days of one workflow,
-- grouped by day, every aggregate this screen draws -- on a probe seeded with a
-- year of hourly runs across forty workflows, warm cache, median of three:
--
--     jsonb_array_elements over task_runs      14,913 buffers   22 ms
--     this table                                2,243 buffers    5 ms
--
-- Both readings give the JSONB version its BEST case: an index on
-- runs (workflow_slug, criado_em) that does not exist in this schema. Adding it
-- buys around 20%, because the cost is not the scan over `runs`. It is that
-- `task_runs` is a WIDE row -- `etapas` sits beside `log` and `saida` -- and
-- reading nine numbers means touching all of it: 222 MB of table to answer a
-- question whose answer is 81 MB. See docs/plan/2026-09-10-load-trend.md.
--
-- DENORMALISED on purpose. `workflow_slug` and the timestamp both live on
-- `runs`, and joining to get them is precisely what made the JSONB version
-- slow: the index that answers this in one scan has to be on the table being
-- scanned.
CREATE TABLE load_metrics (
    run_id        UUID        NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    node_id       TEXT        NOT NULL,

    -- -1 for an unmapped step, matching task_runs.map_index. A mapped step
    -- contributes one row per instance, which is what makes "rows loaded" the
    -- sum of what actually ran rather than the average of a fan-out.
    map_index     INT         NOT NULL DEFAULT -1,

    workflow_slug TEXT        NOT NULL,

    -- When the RUN was created, not when this step ran. The trend is read per
    -- day and a step that starts at 23:58 and ends at 00:04 belongs to the run
    -- that asked for it, in the same bucket as every other step of that run.
    em            TIMESTAMPTZ NOT NULL,

    -- The load's own numbers.
    linhas        BIGINT      NOT NULL DEFAULT 0,
    registros     BIGINT      NOT NULL DEFAULT 0,
    ignorados     BIGINT      NOT NULL DEFAULT 0,
    bytes_saida   BIGINT      NOT NULL DEFAULT 0,
    load_ms       BIGINT      NOT NULL DEFAULT 0,

    -- The extract's.
    bytes_entrada BIGINT      NOT NULL DEFAULT 0,
    paginas       INT         NOT NULL DEFAULT 0,
    tentativas    INT         NOT NULL DEFAULT 0,
    extract_ms    BIGINT      NOT NULL DEFAULT 0,

    PRIMARY KEY (run_id, node_id, map_index)
);

-- The one access path there is: one workflow, newest first, bounded by time.
CREATE INDEX load_metrics_workflow_idx ON load_metrics (workflow_slug, em DESC);

COMMENT ON TABLE load_metrics IS
  'Numeros que cada carga do SDK ja produzia, guardados para leitura de tendencia. Derivado de task_runs.etapas.';

-- Backfill from the history that is already there.
--
-- A trend screen that starts empty on an installation with a year of runs would
-- be the screen lying about a fleet it can already see. The numbers exist; this
-- only moves them somewhere they can be read.
--
-- Phases are matched by NAME and not by position: a pipeline with two Map
-- stages pushes `load` to index three, and `etapas->1` would then read a
-- transform's numbers into a load's column. `ms` on the phase, everything else
-- under `numeros` -- the shape the collector writes.
INSERT INTO load_metrics (
    run_id, node_id, map_index, workflow_slug, em,
    linhas, registros, ignorados, bytes_saida, load_ms,
    bytes_entrada, paginas, tentativas, extract_ms)
SELECT t.run_id, t.node_id, t.map_index, r.workflow_slug, r.criado_em,
       COALESCE((carga->'numeros'->>'rows')::bigint, 0),
       COALESCE((carga->'numeros'->>'records')::bigint, 0),
       COALESCE((carga->'numeros'->>'ignored')::bigint, 0),
       COALESCE((carga->'numeros'->>'load_bytes')::bigint, 0),
       COALESCE((carga->>'ms')::bigint, 0),
       COALESCE((extracao->'numeros'->>'bytes')::bigint, 0),
       COALESCE((extracao->'numeros'->>'pages')::int, 0),
       COALESCE((extracao->'numeros'->>'http_attempts')::int, 0),
       COALESCE((extracao->>'ms')::bigint, 0)
FROM task_runs t
JOIN runs r ON r.id = t.run_id
LEFT JOIN LATERAL (
    SELECT e FROM jsonb_array_elements(t.etapas) e
    WHERE e->>'nome' = 'load' AND e->>'estado' = 'done'
    ORDER BY (e->>'indice')::int LIMIT 1
) l(carga) ON true
LEFT JOIN LATERAL (
    SELECT e FROM jsonb_array_elements(t.etapas) e
    WHERE e->>'nome' = 'extract' ORDER BY (e->>'indice')::int LIMIT 1
) x(extracao) ON true
-- Only a load that FINISHED, and this is the same rule the runner applies on
-- the write path -- internal/application/execution/stages.go, loadNumbers().
-- Two implementations of one rule is the shape that drifts, so both are tested
-- against the same cases.
--
-- A step that is not an SDK pipeline has nothing to contribute to a load trend.
-- Neither does a load that died halfway: its numbers are half of something that
-- never happened, and averaging them in would read as a dataset shrinking. The
-- run's failure is already on the calendar, which is where it belongs.
WHERE carga IS NOT NULL
ON CONFLICT (run_id, node_id, map_index) DO NOTHING;

-- +goose Down
DROP TABLE load_metrics;
