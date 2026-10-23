-- +goose Up
-- The claim's heartbeat (#121).
--
-- The sweep reclaimed any item claimed longer ago than the visibility window,
-- and nothing renewed a claim while its run went on -- so a run longer than
-- fifteen minutes was requeued by the scheduler still running it, and ran
-- twice. The holder now renews here, and the sweep measures SILENCE: the last
-- renewal, or the claim itself when there has been none.
--
-- A column of its own rather than rewriting reivindicado_em, because that one
-- has a second reader: WaitingOn reports it as "held since", and a holder four
-- hours into a run renewed a second ago has been holding for four hours.
--
-- NULL whenever the item is not claimed, and on every new claim: a heartbeat
-- belongs to one claim, and a new holder inheriting the last one's would have
-- its silence measured from somebody else's renewal.
ALTER TABLE queue_items ADD COLUMN renovado_em TIMESTAMPTZ;

COMMENT ON COLUMN queue_items.renovado_em IS
  'Ultima renovacao do claim pelo seu dono; NULL = nenhuma desde o claim.';

-- +goose Down
ALTER TABLE queue_items DROP COLUMN renovado_em;
