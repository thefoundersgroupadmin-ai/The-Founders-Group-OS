ALTER TABLE founderos_sales_automation ADD COLUMN IF NOT EXISTS source_workspace_ids UUID[] NOT NULL DEFAULT '{}';
CREATE TABLE IF NOT EXISTS founderos_commands (
 id UUID PRIMARY KEY,
 workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 user_id TEXT NOT NULL,
 user_email TEXT NOT NULL,
 user_name TEXT NOT NULL DEFAULT '',
 request TEXT NOT NULL,
 plan JSONB NOT NULL,
 steps JSONB NOT NULL DEFAULT '[]',
 status TEXT NOT NULL CHECK(status IN ('queued','running','waiting','complete','blocked','interrupted')),
 report TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS founderos_commands_recent ON founderos_commands(user_id,created_at DESC);
