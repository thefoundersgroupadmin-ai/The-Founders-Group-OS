-- Client AI SDR pods (docs/tfg/CLIENT-PODS.md). A pod is a client workspace
-- plus everything its AI SDR needs: identity, policy, limits, knowledge,
-- approvals and an activity feed the client portal mirrors.

CREATE TABLE IF NOT EXISTS founderos_client_pods (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL UNIQUE REFERENCES workspaces(id) ON DELETE CASCADE,
    slug TEXT NOT NULL UNIQUE CHECK (slug ~ '^client-[a-z0-9][a-z0-9-]{1,60}$'),
    client_name TEXT NOT NULL CHECK (length(trim(client_name)) > 0),
    portal_client_id TEXT,
    package TEXT NOT NULL DEFAULT 'custom' CHECK (package IN ('custom', 'core', 'full')),
    reps INTEGER NOT NULL DEFAULT 1 CHECK (reps BETWEEN 1 AND 200),
    status TEXT NOT NULL DEFAULT 'setup'
        CHECK (status IN ('setup', 'dry_run', 'live', 'paused', 'handed_over')),
    -- who the emails come from
    sender_name TEXT NOT NULL DEFAULT '',
    sender_email TEXT NOT NULL DEFAULT '',
    sending_account_id TEXT,
    sending_domain TEXT NOT NULL DEFAULT '',
    -- where dry-run mail goes instead of the prospect
    dry_run_inbox TEXT NOT NULL DEFAULT '',
    approval_policy TEXT NOT NULL DEFAULT 'client_approves_each'
        CHECK (approval_policy IN ('client_approves_each', 'client_approves_sequences', 'auto')),
    first_email_daily_cap INTEGER NOT NULL DEFAULT 20 CHECK (first_email_daily_cap BETWEEN 0 AND 500),
    follow_up_daily_cap INTEGER NOT NULL DEFAULT 40 CHECK (follow_up_daily_cap BETWEEN 0 AND 1000),
    timezone TEXT NOT NULL DEFAULT 'Europe/London',
    send_window_start SMALLINT NOT NULL DEFAULT 9 CHECK (send_window_start BETWEEN 0 AND 23),
    send_window_end SMALLINT NOT NULL DEFAULT 17 CHECK (send_window_end BETWEEN 1 AND 24),
    send_weekends BOOLEAN NOT NULL DEFAULT false,
    crm_provider TEXT NOT NULL DEFAULT '' CHECK (crm_provider IN ('', 'hubspot', 'pipedrive', 'salesforce')),
    calendar_provider TEXT NOT NULL DEFAULT '' CHECK (calendar_provider IN ('', 'calcom', 'google', 'microsoft')),
    booking_url TEXT NOT NULL DEFAULT '',
    baseline JSONB NOT NULL DEFAULT '{}',
    targets JSONB NOT NULL DEFAULT '{}',
    paused_reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    went_live_at TIMESTAMPTZ,
    CHECK (send_window_end > send_window_start)
);

-- The pod's knowledge: what the agents may say and who they may target.
CREATE TABLE IF NOT EXISTS founderos_client_pod_docs (
    pod_id UUID NOT NULL REFERENCES founderos_client_pods(id) ON DELETE CASCADE,
    key TEXT NOT NULL CHECK (key IN ('icp', 'voice', 'product', 'objections', 'targets',
        'exclusions', 'playbook', 'qualification', 'handoff')),
    body TEXT NOT NULL DEFAULT '',
    version INTEGER NOT NULL DEFAULT 1,
    approved_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (pod_id, key)
);

-- Things the client signs off in the portal before they go live.
CREATE TABLE IF NOT EXISTS founderos_client_approvals (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    pod_id UUID NOT NULL REFERENCES founderos_client_pods(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('sequence_step', 'message', 'target_list', 'playbook', 'doc')),
    ref TEXT NOT NULL,
    title TEXT NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}',
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'changes_requested', 'withdrawn')),
    client_note TEXT NOT NULL DEFAULT '',
    decided_by TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at TIMESTAMPTZ,
    UNIQUE (pod_id, kind, ref)
);
CREATE INDEX IF NOT EXISTS idx_founderos_client_approvals_open
    ON founderos_client_approvals(pod_id, status);

-- What the pod did, newest first, for the portal feed and reports.
CREATE TABLE IF NOT EXISTS founderos_client_activity (
    id BIGSERIAL PRIMARY KEY,
    pod_id UUID NOT NULL REFERENCES founderos_client_pods(id) ON DELETE CASCADE,
    at TIMESTAMPTZ NOT NULL DEFAULT now(),
    kind TEXT NOT NULL,
    summary TEXT NOT NULL,
    ref TEXT NOT NULL DEFAULT '',
    detail JSONB NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_founderos_client_activity_recent
    ON founderos_client_activity(pod_id, at DESC);
