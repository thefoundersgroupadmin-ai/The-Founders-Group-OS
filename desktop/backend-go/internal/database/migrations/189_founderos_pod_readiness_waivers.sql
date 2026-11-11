-- Go-live readiness covers the whole purchased service (gap list, 11 Nov 2026): product
-- knowledge, a provisioned AI agent, client portal access, a booking calendar, a rep for
-- hand-offs and a baseline. A requirement a client genuinely doesn't need is waived on purpose,
-- with a reason and who waived it: {"<item>": {"reason": "...", "by": "...", "at": "..."}}.
ALTER TABLE founderos_client_pods ADD COLUMN IF NOT EXISTS readiness_waivers JSONB NOT NULL DEFAULT '{}';
