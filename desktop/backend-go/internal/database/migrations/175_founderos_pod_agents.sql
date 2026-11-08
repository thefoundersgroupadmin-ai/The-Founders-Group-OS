-- Each pod's AI SDR on the task board (docs/tfg/CLIENT-PODS.md, M1 step 10):
-- its own Paperclip project, agent and daily routines. The OS keeps the ids,
-- so provisioning is idempotent and task output is routed by project to the
-- pod's workspace, never by text an agent wrote.
CREATE TABLE IF NOT EXISTS founderos_pod_agents (
    pod_id UUID PRIMARY KEY REFERENCES founderos_client_pods(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL DEFAULT '',
    agent_id TEXT NOT NULL DEFAULT '',
    research_routine_id TEXT NOT NULL DEFAULT '',
    outreach_routine_id TEXT NOT NULL DEFAULT '',
    brief_hash TEXT NOT NULL DEFAULT '',
    routines_active BOOLEAN NOT NULL DEFAULT false,
    synced_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS founderos_pod_agents_project ON founderos_pod_agents(project_id) WHERE project_id <> '';
