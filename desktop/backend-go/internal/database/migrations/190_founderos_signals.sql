-- Buying signals (/os/signals): why a prospect is worth contacting now.
-- Each row is one observed fact with where it came from; nothing is invented.
CREATE TABLE IF NOT EXISTS founderos_signals (
    id BIGSERIAL PRIMARY KEY,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    contact_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('hiring', 'funding', 'tech', 'engaged')),
    title TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL DEFAULT '',
    weight INTEGER NOT NULL DEFAULT 0,
    signal_date DATE,
    fingerprint TEXT NOT NULL,
    first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, contact_id, fingerprint)
);
CREATE INDEX IF NOT EXISTS founderos_signals_contact ON founderos_signals (workspace_id, contact_id);

-- When each prospect's public sources were last read, and what went wrong.
CREATE TABLE IF NOT EXISTS founderos_signal_checks (
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    contact_id TEXT NOT NULL,
    checked_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    sources TEXT NOT NULL DEFAULT '',
    problem TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (workspace_id, contact_id)
);

-- Paid contact lookups (Lusha), so a daily credit cap holds across restarts.
CREATE TABLE IF NOT EXISTS founderos_enrichments (
    id BIGSERIAL PRIMARY KEY,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    contact_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    at TIMESTAMPTZ NOT NULL DEFAULT now(),
    found_email TEXT NOT NULL DEFAULT '',
    found_phone TEXT NOT NULL DEFAULT '',
    problem TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS founderos_enrichments_day ON founderos_enrichments (provider, at);
