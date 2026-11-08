-- Whether each pod routine's schedule exists (Codex M1 review #10): a routine
-- created without its trigger is repaired on the next provisioning.
ALTER TABLE founderos_pod_agents ADD COLUMN IF NOT EXISTS research_trigger_ok BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE founderos_pod_agents ADD COLUMN IF NOT EXISTS outreach_trigger_ok BOOLEAN NOT NULL DEFAULT false;
