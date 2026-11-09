-- Client pods follow-through (Codex open items, 9 Nov 2026).
-- CRM: later changes (meeting moved, cancelled, held, coaching, opt-outs) are
-- tracked as fingerprinted facts so each is noted once.
ALTER TABLE founderos_pod_crm_ids DROP CONSTRAINT IF EXISTS founderos_pod_crm_ids_local_kind_check;
ALTER TABLE founderos_pod_crm_ids ADD CONSTRAINT founderos_pod_crm_ids_local_kind_check
    CHECK (local_kind IN ('contact', 'email', 'meeting', 'note', 'fact'));

-- Stuck AI tasks: each enquiry, brief, coaching and review task is watched
-- until its work arrives. A slow one is nudged once, then marked stuck.
ALTER TABLE founderos_pod_task_owner ADD COLUMN IF NOT EXISTS nudged_at TIMESTAMPTZ;
ALTER TABLE founderos_pod_task_owner ADD COLUMN IF NOT EXISTS stuck_at TIMESTAMPTZ;
ALTER TABLE founderos_pod_task_owner ADD COLUMN IF NOT EXISTS settled_at TIMESTAMPTZ;
ALTER TABLE founderos_pod_task_owner ADD COLUMN IF NOT EXISTS stuck_reason TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS founderos_pod_task_owner_open
    ON founderos_pod_task_owner (created_at) WHERE settled_at IS NULL AND stuck_at IS NULL;

-- Health of each pod's connections, written by the loops that use them.
CREATE TABLE IF NOT EXISTS founderos_pod_health (
    pod_id     UUID NOT NULL REFERENCES founderos_client_pods(id) ON DELETE CASCADE,
    check_name TEXT NOT NULL,
    ok         BOOLEAN NOT NULL,
    detail     TEXT NOT NULL DEFAULT '',
    since      TIMESTAMPTZ NOT NULL DEFAULT now(),
    checked_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (pod_id, check_name)
);

-- Alerts raised to the owner: one row per open problem, pushed once.
CREATE TABLE IF NOT EXISTS founderos_pod_alerts (
    pod_id      UUID NOT NULL REFERENCES founderos_client_pods(id) ON DELETE CASCADE,
    key         TEXT NOT NULL,
    title       TEXT NOT NULL,
    detail      TEXT NOT NULL DEFAULT '',
    first_seen  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen   TIMESTAMPTZ NOT NULL DEFAULT now(),
    notified_at TIMESTAMPTZ,
    cleared_at  TIMESTAMPTZ,
    PRIMARY KEY (pod_id, key)
);

-- Replies are attributed to the exact email they answer: by the reply's thread
-- headers (each outbox email's Message-ID carries its outbox id), else the
-- latest email sent to that person before the reply.
ALTER TABLE founderos_pod_reply_messages ADD COLUMN IF NOT EXISTS outbox_id TEXT;
ALTER TABLE founderos_pod_reply_messages ADD COLUMN IF NOT EXISTS threaded BOOLEAN NOT NULL DEFAULT false;
CREATE INDEX IF NOT EXISTS founderos_pod_reply_messages_outbox ON founderos_pod_reply_messages (outbox_id) WHERE outbox_id IS NOT NULL;
