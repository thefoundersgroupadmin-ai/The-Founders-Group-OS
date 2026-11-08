-- Inbound enquiries and hand-offs per client pod (docs/tfg/CLIENT-PODS.md, M3).
ALTER TABLE founderos_client_pods ADD COLUMN IF NOT EXISTS inbound_token_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE founderos_client_pods ADD COLUMN IF NOT EXISTS handoff_email TEXT NOT NULL DEFAULT '';
ALTER TABLE founderos_client_pods ADD COLUMN IF NOT EXISTS inbound_auto_reply BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS founderos_pod_inbound (
    id BIGSERIAL PRIMARY KEY,
    pod_id UUID NOT NULL REFERENCES founderos_client_pods(id) ON DELETE CASCADE,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    name TEXT NOT NULL DEFAULT '',
    email TEXT NOT NULL,
    company TEXT NOT NULL DEFAULT '',
    phone TEXT NOT NULL DEFAULT '',
    message TEXT NOT NULL DEFAULT '',
    source_hash TEXT NOT NULL DEFAULT '',
    contact_id TEXT,
    issue_id TEXT,
    status TEXT NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'tasked', 'failed')),
    error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS founderos_pod_inbound_new ON founderos_pod_inbound(status, received_at);

CREATE TABLE IF NOT EXISTS founderos_pod_handoffs (
    id BIGSERIAL PRIMARY KEY,
    pod_id UUID NOT NULL REFERENCES founderos_client_pods(id) ON DELETE CASCADE,
    issue_id TEXT NOT NULL UNIQUE,
    contact_id TEXT,
    summary TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    notified_at TIMESTAMPTZ,
    error TEXT NOT NULL DEFAULT ''
);
