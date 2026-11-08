-- Codex M1 fix review (8 Nov 2026) follow-ups.

-- An email whose delivery was ever uncertain may have arrived: it keeps
-- counting against caps and keeps its sequence step even if later dismissed.
ALTER TABLE founderos_outbox ADD COLUMN IF NOT EXISTS maybe_delivered BOOLEAN NOT NULL DEFAULT false;
UPDATE founderos_outbox SET maybe_delivered = true WHERE status = 'uncertain';

-- Going live needs a test run after the latest change to who sends and how.
ALTER TABLE founderos_client_pods ADD COLUMN IF NOT EXISTS config_changed_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- Routine schedules are kept in step with the pod's timezone and weekends.
ALTER TABLE founderos_pod_agents ADD COLUMN IF NOT EXISTS research_trigger_id TEXT NOT NULL DEFAULT '';
ALTER TABLE founderos_pod_agents ADD COLUMN IF NOT EXISTS outreach_trigger_id TEXT NOT NULL DEFAULT '';
ALTER TABLE founderos_pod_agents ADD COLUMN IF NOT EXISTS trigger_signature TEXT NOT NULL DEFAULT '';
