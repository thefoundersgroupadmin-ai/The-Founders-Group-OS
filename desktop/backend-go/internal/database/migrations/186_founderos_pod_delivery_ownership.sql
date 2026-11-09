-- Codex final review: notification delivery states with retries, task
-- ownership that a moved task cannot change, durable reply messages, and the
-- numbers a weekly review was written from.

ALTER TABLE founderos_pod_meetings ADD COLUMN IF NOT EXISTS brief_delivery TEXT NOT NULL DEFAULT '';
ALTER TABLE founderos_pod_meetings ADD COLUMN IF NOT EXISTS brief_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE founderos_pod_meetings ADD COLUMN IF NOT EXISTS coaching_delivery TEXT NOT NULL DEFAULT '';
ALTER TABLE founderos_pod_meetings ADD COLUMN IF NOT EXISTS coaching_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE founderos_pod_weekly ADD COLUMN IF NOT EXISTS review_delivery TEXT NOT NULL DEFAULT '';
ALTER TABLE founderos_pod_weekly ADD COLUMN IF NOT EXISTS review_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE founderos_pod_weekly ADD COLUMN IF NOT EXISTS review_metrics JSONB;

-- Existing sent rows are sent.
UPDATE founderos_pod_meetings SET brief_delivery = 'sent' WHERE brief_sent_at IS NOT NULL AND brief_delivery = '';
UPDATE founderos_pod_meetings SET coaching_delivery = 'sent' WHERE coaching_sent_at IS NOT NULL AND coaching_delivery = '';
UPDATE founderos_pod_weekly SET review_delivery = 'sent' WHERE review_sent_at IS NOT NULL AND review_delivery = '';

-- The pod (or HQ, pod_id NULL) a board task first belonged to. A task whose
-- project later changes is quarantined, never imported into the new owner.
CREATE TABLE IF NOT EXISTS founderos_pod_task_owner (
    issue_id TEXT PRIMARY KEY,
    pod_id UUID REFERENCES founderos_client_pods(id) ON DELETE CASCADE,
    kind TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Each inbound reply message once per pod mailbox, with its real sender.
CREATE TABLE IF NOT EXISTS founderos_pod_reply_messages (
    id BIGSERIAL PRIMARY KEY,
    pod_id UUID NOT NULL REFERENCES founderos_client_pods(id) ON DELETE CASCADE,
    message_id TEXT NOT NULL,
    sender TEXT NOT NULL,
    contact_id TEXT,
    received_at TIMESTAMPTZ,
    issue_id TEXT,
    routed TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (pod_id, message_id)
);
