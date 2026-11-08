-- What each client pod has already written to the client's CRM (M5), so the
-- sync creates nothing twice.
CREATE TABLE IF NOT EXISTS founderos_pod_crm_ids (
    pod_id UUID NOT NULL REFERENCES founderos_client_pods(id) ON DELETE CASCADE,
    local_kind TEXT NOT NULL CHECK (local_kind IN ('contact', 'email', 'meeting', 'note')),
    local_id TEXT NOT NULL,
    remote_id TEXT NOT NULL,
    synced_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (pod_id, local_kind, local_id)
);
