-- Preserve sourced research and next steps instead of truncating everything
-- into an activity label. Unknown dates stay NULL; no forecast is invented.
ALTER TABLE founderos_funnel_contacts
 ADD COLUMN IF NOT EXISTS website TEXT,
 ADD COLUMN IF NOT EXISTS location TEXT,
 ADD COLUMN IF NOT EXISTS buyer_role TEXT,
 ADD COLUMN IF NOT EXISTS research_evidence TEXT,
 ADD COLUMN IF NOT EXISTS next_action TEXT,
 ADD COLUMN IF NOT EXISTS next_action_at TIMESTAMPTZ,
 ADD COLUMN IF NOT EXISTS expected_close_at DATE,
 ADD COLUMN IF NOT EXISTS research_source_ref TEXT;
