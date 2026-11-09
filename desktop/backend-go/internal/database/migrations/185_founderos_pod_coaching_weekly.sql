-- Client pods M6: call coaching from recordings, weekly pipeline review and
-- weekly results against the client's month-one baseline (docs/tfg/CLIENT-PODS.md).

-- After a call: what happened, the recording's transcript, and the AI SDR's
-- coaching notes for the client's rep.
ALTER TABLE founderos_pod_meetings ADD COLUMN IF NOT EXISTS outcome TEXT NOT NULL DEFAULT ''
    CHECK (outcome IN ('', 'held', 'no_show'));
ALTER TABLE founderos_pod_meetings ADD COLUMN IF NOT EXISTS recording_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE founderos_pod_meetings ADD COLUMN IF NOT EXISTS transcript TEXT NOT NULL DEFAULT '';
ALTER TABLE founderos_pod_meetings ADD COLUMN IF NOT EXISTS coaching_issue_id TEXT;
ALTER TABLE founderos_pod_meetings ADD COLUMN IF NOT EXISTS coaching TEXT NOT NULL DEFAULT '';
ALTER TABLE founderos_pod_meetings ADD COLUMN IF NOT EXISTS coaching_sent_at TIMESTAMPTZ;

-- One row per pod per week (Monday, in the pod's timezone): the numbers TFG OS
-- counted, and the AI SDR's review of them with what to work on next.
CREATE TABLE IF NOT EXISTS founderos_pod_weekly (
    id BIGSERIAL PRIMARY KEY,
    pod_id UUID NOT NULL REFERENCES founderos_client_pods(id) ON DELETE CASCADE,
    week_start DATE NOT NULL,
    metrics JSONB NOT NULL DEFAULT '{}'::jsonb,
    review_issue_id TEXT,
    review TEXT NOT NULL DEFAULT '',
    review_sent_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (pod_id, week_start)
);
CREATE INDEX IF NOT EXISTS founderos_pod_weekly_issue ON founderos_pod_weekly (review_issue_id);
CREATE INDEX IF NOT EXISTS founderos_pod_meetings_coaching_issue ON founderos_pod_meetings (coaching_issue_id);
