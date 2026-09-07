-- +goose Up
-- The limit on simultaneous runs PER WORKFLOW (Kestra's `concurrency.limit`).
--
-- 36 of the data repository's 51 flows declared that limit, five of them on a
-- 15- or 30-minute cadence. Without it, a `*/15` that takes 20 minutes overlaps
-- itself — two `dbt build`s on the SAME model, at the same time.
--
-- The column lives on the RUN, and not only on the workflow, for the same reason
-- `definicao` does: it is a snapshot. Lowering the limit from 3 to 1 must not
-- change the meaning of runs that were already queued. And in practice it takes
-- a JOIN with `workflows` off the system's hottest path — the claim query.
ALTER TABLE runs ADD COLUMN max_ativos INT NOT NULL DEFAULT 0;

COMMENT ON COLUMN runs.max_ativos IS
  'Execucoes simultaneas permitidas para este workflow. 0 = sem limite.';

-- The claim starts grouping claimed items by workflow.
CREATE INDEX queue_items_reivindicados_idx ON queue_items (run_id)
  WHERE reivindicado_em IS NOT NULL;

-- +goose Down
DROP INDEX queue_items_reivindicados_idx;
ALTER TABLE runs DROP COLUMN max_ativos;
