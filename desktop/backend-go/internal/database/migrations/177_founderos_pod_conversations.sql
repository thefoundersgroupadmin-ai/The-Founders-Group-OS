-- Tasks opened for a prospect's reply to a pod (scripts/sync-prospect-replies.py).
-- Drafts handed over on these tasks are conversation replies, not sequence
-- emails (Codex M1 review #8): the importer marks them sequence_key 'reply'.
CREATE TABLE IF NOT EXISTS founderos_pod_conversations (
    issue_id TEXT PRIMARY KEY,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    contact_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
