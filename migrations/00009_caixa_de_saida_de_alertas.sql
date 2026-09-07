-- +goose Up
-- The alerts outbox.
--
-- Today the dispatcher calls notify.Slack directly and, if the call fails, logs
-- and carries on. That is the right behaviour -- a Slack outage must not stop a
-- pipeline -- and it is also how an alert is LOST: no retry, no record, and
-- nothing on the screen to say one was meant to be sent. Whoever was supposed
-- to be woken up simply is not.
--
-- The shape is queue_items', deliberately. That table is already a durable,
-- claimable, retryable work queue with a visibility timeout, it has been in
-- production, and its failure modes are understood. An alert wants exactly
-- that, so it gets the same columns with the same names rather than a second
-- vocabulary for the same idea.
--
-- What makes it an OUTBOX rather than just another queue is where the row is
-- written: in the same transaction as the failure that justifies it. Either the
-- run is recorded as out of attempts and the alert exists, or neither happened.
-- There is no window in which one is true.
CREATE TABLE alertas (
    id      BIGSERIAL PRIMARY KEY,
    run_id  UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,

    -- What raised it: 'run' when a run ran out of attempts, 'step' when a step
    -- declared on_error. The distinction is on the row and not inferred from
    -- node_id being empty, because "a run failed" and "a step failed" are
    -- different messages and a reader of this table should not have to guess.
    tipo    TEXT NOT NULL,

    -- The step, for tipo = 'step'. Empty otherwise.
    node_id TEXT NOT NULL DEFAULT '',

    -- Where it goes: 'SLACK' today. Validated at publish against a closed
    -- vocabulary, so an unknown channel is refused when the workflow is
    -- published and not discovered on the night it was needed.
    canal   TEXT NOT NULL,

    -- The whole message, resolved at WRITE time and self-contained.
    --
    -- The alert pod never reads runs or task_runs to build it, and that is the
    -- point: the workflow's tags, the failing step and the last lines of its
    -- log are what they were at the instant of the failure. Rebuilding the
    -- message at delivery time would let a republished workflow change the text
    -- of an alert about a run that used the old definition.
    payload JSONB NOT NULL,

    -- The claim, exactly as queue_items does it.
    disponivel_em    TIMESTAMPTZ NOT NULL DEFAULT now(),
    reivindicado_em  TIMESTAMPTZ,
    reivindicado_por TEXT,

    -- Delivery, and its three terminal states.
    --
    -- entregue_em set  -> it arrived
    -- desistiu_em set  -> it never will, and `erro` says why
    -- both NULL        -> still trying, `tentativas` says how hard
    --
    -- The failure is KEPT rather than deleted. "Raised, not delivered, 4
    -- attempts, 403 from Slack" is the most useful row this table produces:
    -- it is the case where somebody is waiting for a message that is not
    -- coming, and deleting it makes that indistinguishable from an alert that
    -- was never raised.
    tentativas   INT         NOT NULL DEFAULT 0,
    erro         TEXT        NOT NULL DEFAULT '',
    entregue_em  TIMESTAMPTZ,
    desistiu_em  TIMESTAMPTZ,

    criado_em    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The hot path: free, available, not yet terminal, oldest first. Partial so it
-- does not index the history, which is most of the table after a month.
CREATE INDEX alertas_pendentes_idx
    ON alertas (disponivel_em, id)
    WHERE reivindicado_em IS NULL AND entregue_em IS NULL AND desistiu_em IS NULL;

-- The run's screen asks "were there alerts for this run, and did they arrive?"
CREATE INDEX alertas_por_run_idx ON alertas (run_id);

COMMENT ON TABLE alertas IS
  'Caixa de saida de alertas. A linha e escrita na MESMA transacao da falha que a justifica.';

-- +goose Down
DROP TABLE alertas;
