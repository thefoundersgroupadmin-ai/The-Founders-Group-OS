-- Codex M2-M4 review: conversation source and human takeover; hand-off retries.
ALTER TABLE founderos_pod_conversations ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'email'
    CHECK (source IN ('email', 'form'));
ALTER TABLE founderos_pod_conversations ADD COLUMN IF NOT EXISTS taken_over_at TIMESTAMPTZ;
ALTER TABLE founderos_pod_handoffs ADD COLUMN IF NOT EXISTS attempts INTEGER NOT NULL DEFAULT 0;
