-- Client approvals bound to exact content (docs/tfg/CLIENT-PODS.md, M1 step 7).
-- An outbox row may belong to an approved sequence step; the gate then checks
-- the body is that step's template with only its {placeholders} filled in.
ALTER TABLE founderos_outbox ADD COLUMN IF NOT EXISTS sequence_key TEXT;

-- Pipeline contacts of a client pod carry venture 'client' (the pod is their
-- workspace); the two TFG ventures keep their own values.
ALTER TABLE founderos_funnel_contacts DROP CONSTRAINT IF EXISTS founderos_funnel_contacts_venture_check;
ALTER TABLE founderos_funnel_contacts ADD CONSTRAINT founderos_funnel_contacts_venture_check
    CHECK (venture IN ('vantage', 'launchpad-cohort', 'client'));
