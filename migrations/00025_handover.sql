-- +goose Up
-- Where a step that outlived its scheduler goes on running (#125).
--
-- A shutdown leaves a `host:` step running on its agent instead of cancelling
-- it, and the next scheduler follows it there: the agent instance that holds
-- it, its execution id, the attempt it is and the last line read. The row is
-- NOT ended -- the step is running -- so this column is the only thing that
-- says it was handed over.
--
-- On the step's row because that is what the next scheduler reads before it
-- starts the step's attempt, which overwrites the row (IniciarTask upserts).
-- NULL whenever the attempt is not handed over: starting or ending it clears
-- the column.
ALTER TABLE task_runs ADD COLUMN handover JSONB;

COMMENT ON COLUMN task_runs.handover IS
  'Onde o step continua rodando depois de entregue no desligamento; NULL = nao entregue.';

-- +goose Down
ALTER TABLE task_runs DROP COLUMN handover;
