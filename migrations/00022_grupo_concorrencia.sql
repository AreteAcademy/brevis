-- +goose Up
-- The concurrency GROUP a run contends in (#32).
--
-- Two workflows that build the same dbt models -- a daily and a */15 -- each
-- had `concurrency: 1`, which limits runs OF ONE workflow. A manual run of the
-- daily landed on a slot of the other, one dropped the `__dbt_tmp` table the
-- other was reading, and dbt failed. A group names the shared resource, and
-- the claim counts every run in it against one limit.
--
-- On the RUN, as a snapshot, for the reasons `max_ativos` is (00005): changing
-- the group in a file must not change runs already queued, and the claim -- the
-- hottest query there is -- must not join `workflows` to find it.
--
-- NULL, not '', when a workflow names no group: the claim partitions by
-- COALESCE(grupo_concorrencia, workflow_slug), so a workflow without one keeps
-- contending with itself, as it always has.
ALTER TABLE runs ADD COLUMN grupo_concorrencia TEXT;

COMMENT ON COLUMN runs.grupo_concorrencia IS
  'Grupo de concorrencia do run; NULL = o workflow contende so consigo mesmo.';

-- +goose Down
ALTER TABLE runs DROP COLUMN grupo_concorrencia;
