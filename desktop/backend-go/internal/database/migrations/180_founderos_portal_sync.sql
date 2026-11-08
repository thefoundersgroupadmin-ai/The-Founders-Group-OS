-- How far each pod's activity has been pushed to the client portal (M2).
CREATE TABLE IF NOT EXISTS founderos_portal_sync (
    pod_id UUID PRIMARY KEY REFERENCES founderos_client_pods(id) ON DELETE CASCADE,
    last_activity_id BIGINT NOT NULL DEFAULT 0,
    synced_at TIMESTAMPTZ
);
