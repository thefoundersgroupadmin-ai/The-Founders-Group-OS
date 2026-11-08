-- Meetings booked on a client pod's own calendar, and their call briefs (M4).
CREATE TABLE IF NOT EXISTS founderos_pod_meetings (
    id BIGSERIAL PRIMARY KEY,
    pod_id UUID NOT NULL REFERENCES founderos_client_pods(id) ON DELETE CASCADE,
    booking_uid TEXT NOT NULL,
    contact_id TEXT,
    attendee_email TEXT NOT NULL DEFAULT '',
    attendee_name TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    starts_at TIMESTAMPTZ,
    status TEXT NOT NULL DEFAULT 'booked',
    brief_issue_id TEXT,
    brief TEXT NOT NULL DEFAULT '',
    brief_sent_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (pod_id, booking_uid)
);
