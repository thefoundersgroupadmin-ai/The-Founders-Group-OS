CREATE TABLE IF NOT EXISTS founderos_tool_calls (
 id UUID PRIMARY KEY, command_id UUID NOT NULL REFERENCES founderos_commands(id),
 task_id TEXT NOT NULL, agent_id TEXT NOT NULL, run_id TEXT NOT NULL,
 request_id TEXT NOT NULL, tool TEXT NOT NULL, input JSONB NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('needs_approval','running','succeeded','failed','uncertain','rejected','missing_credentials')),
 output JSONB NOT NULL DEFAULT '{}'::jsonb,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(task_id,request_id)
);
CREATE INDEX IF NOT EXISTS founderos_tool_calls_command ON founderos_tool_calls(command_id,created_at);
