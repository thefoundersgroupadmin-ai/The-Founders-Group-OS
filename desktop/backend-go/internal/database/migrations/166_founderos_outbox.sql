-- 166: the outbox. Agent-drafted email (Paperclip today) waits here until the
-- operator approves it in /os/outbox; the bridge then sends it through the
-- email connector (guarded by FOUNDEROS_WRITES and the mail guard, the
-- approval being that one call's recipient sign-off). 'sending' is a claim:
-- an approve flips pending/failed -> sending in one UPDATE, so a double click
-- cannot send twice; attempt counts claims. 'uncertain' means the server may
-- have accepted the message (lost acknowledgement or an interrupted send):
-- it is never retried automatically. reported_attempt/reported_status record
-- which outcome was handed back to the source, so a retry is reported again.
CREATE TABLE IF NOT EXISTS founderos_outbox (
    id TEXT PRIMARY KEY,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    source TEXT NOT NULL,
    source_ref TEXT,
    contact_id TEXT,
    account_id TEXT,
    to_address TEXT NOT NULL,
    to_name TEXT,
    company TEXT,
    subject TEXT NOT NULL,
    body TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sending', 'sent', 'rejected', 'failed', 'uncertain')),
    error TEXT,
    attempt INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at TIMESTAMPTZ,
    sent_at TIMESTAMPTZ,
    reported_attempt INTEGER,
    reported_status TEXT
);
CREATE INDEX IF NOT EXISTS idx_founderos_outbox_workspace_status ON founderos_outbox(workspace_id, status);
