-- Sales automation state belongs to the OS, not to the knowledge engine.
CREATE TABLE IF NOT EXISTS founderos_sales_automation (
 workspace_id UUID PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
 user_id TEXT NOT NULL,
 user_email TEXT NOT NULL,
 user_name TEXT NOT NULL DEFAULT '',
 profile TEXT NOT NULL,
 enabled BOOLEAN NOT NULL DEFAULT false,
 timezone TEXT NOT NULL DEFAULT 'Europe/London',
 checkpoint JSONB NOT NULL DEFAULT '{}',
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS founderos_sales_automation_runs (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 batch_key TEXT NOT NULL,
 routines JSONB NOT NULL,
 skills JSONB NOT NULL,
 source_ids JSONB NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('running','complete','error')),
 markdown TEXT NOT NULL DEFAULT '',
 error TEXT NOT NULL DEFAULT '',
 started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 completed_at TIMESTAMPTZ,
 UNIQUE(workspace_id,batch_key)
);
CREATE INDEX IF NOT EXISTS founderos_sales_automation_runs_recent ON founderos_sales_automation_runs(workspace_id,started_at DESC);
