-- +goose Up
-- One row per INSTANCE of a step, not per step.
--
-- A mapped step (`for_each:`) runs once per element of a list, and each of those
-- runs has its own exit code, its own log and its own duration. Without this
-- column they would collide on (run_id, node_id, attempt) and the last one to
-- finish would overwrite the rest -- which is the same table saying four things
-- happened and remembering one.
--
-- -1 means NOT MAPPED, which is every row written before today and most rows
-- after it. A nullable column would have been the other option; -1 keeps the
-- unique constraint working, because in SQL two NULLs are never equal and the
-- constraint would silently stop protecting unmapped steps. Airflow arrived at
-- the same column with the same sentinel.
ALTER TABLE task_runs ADD COLUMN map_index INT NOT NULL DEFAULT -1;

-- The unique moves with it. Dropping and recreating rather than adding a second
-- one: two constraints would let (run, node, attempt) stay unique and make the
-- whole feature impossible.
ALTER TABLE task_runs DROP CONSTRAINT task_runs_run_id_node_id_attempt_key;
ALTER TABLE task_runs ADD CONSTRAINT task_runs_instance_key
    UNIQUE (run_id, node_id, attempt, map_index);

COMMENT ON COLUMN task_runs.map_index IS
  'Indice da instancia de um passo mapeado (for_each). -1 = passo nao mapeado.';

-- +goose Down
ALTER TABLE task_runs DROP CONSTRAINT task_runs_instance_key;
ALTER TABLE task_runs ADD CONSTRAINT task_runs_run_id_node_id_attempt_key
    UNIQUE (run_id, node_id, attempt);
ALTER TABLE task_runs DROP COLUMN map_index;
