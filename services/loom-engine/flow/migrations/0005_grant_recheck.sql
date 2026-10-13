-- P5: scheduled runs re-check the grant actor's ongoing authorization.
ALTER TABLE flow_schedules ADD COLUMN IF NOT EXISTS disabled_reason TEXT;
ALTER TABLE flow_brief_schedules ADD COLUMN IF NOT EXISTS disabled_reason TEXT;
