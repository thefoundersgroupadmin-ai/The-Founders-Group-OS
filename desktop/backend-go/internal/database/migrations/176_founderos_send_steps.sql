-- One reservation per pod, recipient and sequence step (Codex M1 review #3):
-- a step (first email, follow-up 1, follow-up 2, or one conversation reply)
-- goes to a person once, whatever drafts exist. A reservation belongs to one
-- outbox row; retrying that row reuses it, rejecting the row releases it.
CREATE TABLE IF NOT EXISTS founderos_send_steps (
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    recipient TEXT NOT NULL CHECK (recipient = lower(recipient)),
    step TEXT NOT NULL,
    outbox_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, recipient, step)
);
CREATE INDEX IF NOT EXISTS founderos_send_steps_outbox ON founderos_send_steps(outbox_id);
