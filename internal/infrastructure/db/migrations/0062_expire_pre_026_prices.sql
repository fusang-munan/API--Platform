-- Require a fresh sync before pre-0.26 multipliers and panel quotes can be
-- used for new calls. Keep the rows and their earlier validity boundaries
-- for historical lookups by the original call time (p_time).
UPDATE beichen_multiplier_versions
SET stale_at = now()
WHERE stale_at IS NULL OR stale_at > now();

UPDATE beichen_panel_snapshots
SET expires_at = now()
WHERE expires_at > now();
