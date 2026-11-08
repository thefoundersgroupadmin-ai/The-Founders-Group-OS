-- Client approvals bound to exact content (docs/tfg/CLIENT-PODS.md, M1 step 7).
-- An outbox row may belong to an approved sequence step; the gate then checks
-- the body is that step's template with only its {placeholders} filled in.
ALTER TABLE founderos_outbox ADD COLUMN IF NOT EXISTS sequence_key TEXT;
