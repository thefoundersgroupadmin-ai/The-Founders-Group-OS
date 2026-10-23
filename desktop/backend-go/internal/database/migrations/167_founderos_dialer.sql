-- 167: the dialer. One row per outbound call placed from /os/dialer through
-- Twilio (browser to phone): who called whom, the Twilio call SID, how it
-- ended (from Twilio's status callback), and the caller's outcome and notes.
-- A claim (claimed_by/claimed_until on the queue) keeps two callers off the
-- same prospect. founderos_dnc is the do-not-call list: a number on it is
-- never queued or dialled again.
CREATE TABLE IF NOT EXISTS founderos_calls (
    id TEXT PRIMARY KEY,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    contact_id TEXT,
    caller_email TEXT NOT NULL,
    to_number TEXT NOT NULL,
    from_number TEXT,
    twilio_sid TEXT,
    status TEXT NOT NULL DEFAULT 'dialing',
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    answered_at TIMESTAMPTZ,
    ended_at TIMESTAMPTZ,
    duration_s INTEGER,
    outcome TEXT CHECK (outcome IN ('meeting', 'callback', 'interested', 'not_interested', 'voicemail', 'no_answer', 'wrong_number', 'do_not_call', 'gatekeeper')),
    notes TEXT,
    summary TEXT,
    callback_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_founderos_calls_contact ON founderos_calls(contact_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_founderos_calls_caller ON founderos_calls(caller_email, started_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_founderos_calls_sid ON founderos_calls(twilio_sid) WHERE twilio_sid IS NOT NULL;

CREATE TABLE IF NOT EXISTS founderos_dnc (
    number TEXT PRIMARY KEY,
    reason TEXT,
    added_by TEXT,
    added_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The queue's per-contact state: a claim while someone is on the line or
-- about to be, the next time this person may be called, and where the number
-- came from.
CREATE TABLE IF NOT EXISTS founderos_dial_state (
    contact_id TEXT PRIMARY KEY,
    claimed_by TEXT,
    claimed_until TIMESTAMPTZ,
    next_call_at TIMESTAMPTZ,
    attempts INTEGER NOT NULL DEFAULT 0,
    phone_source TEXT,
    apollo_id TEXT,
    apollo_requested_at TIMESTAMPTZ,
    brief TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
