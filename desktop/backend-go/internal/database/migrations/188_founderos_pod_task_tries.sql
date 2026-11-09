-- When each brief, coaching and review task was first tried. The board keeps
-- task keys for seven days, so work first tried over six days ago is never
-- retried blind: it is raised for a person instead (Codex round 5).
ALTER TABLE founderos_pod_meetings ADD COLUMN IF NOT EXISTS brief_task_tried_at TIMESTAMPTZ;
ALTER TABLE founderos_pod_meetings ADD COLUMN IF NOT EXISTS coaching_task_tried_at TIMESTAMPTZ;
ALTER TABLE founderos_pod_weekly ADD COLUMN IF NOT EXISTS review_task_tried_at TIMESTAMPTZ;
