-- The pod send gate (docs/tfg/CLIENT-PODS.md, M1 steps 5, 6, 8, 9).

-- A dry-run send is delivered to the pod's approved sink, never the prospect,
-- and is recorded as simulated: it never counts as a delivery or against caps.
ALTER TABLE founderos_outbox DROP CONSTRAINT IF EXISTS founderos_outbox_status_check;
ALTER TABLE founderos_outbox ADD CONSTRAINT founderos_outbox_status_check
    CHECK (status IN ('pending', 'sending', 'sent', 'rejected', 'failed', 'uncertain', 'simulated'));

-- Email suppression: an address or a whole domain, for one workspace (a pod)
-- or, with workspace_id NULL, for every workspace.
CREATE TABLE IF NOT EXISTS founderos_email_suppression (
    id BIGSERIAL PRIMARY KEY,
    workspace_id UUID REFERENCES workspaces(id) ON DELETE CASCADE,
    address TEXT,
    domain TEXT,
    reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((address IS NULL) <> (domain IS NULL)),
    CHECK (address IS NULL OR address = lower(trim(address))),
    CHECK (domain IS NULL OR domain = lower(trim(domain)))
);
CREATE UNIQUE INDEX IF NOT EXISTS founderos_email_suppression_unique
    ON founderos_email_suppression (coalesce(workspace_id::text, '*'), coalesce(address, ''), coalesce(domain, ''));
