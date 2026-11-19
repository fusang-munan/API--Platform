-- Per-key display terms from the same channel, sample and multiplier as the
-- effective price. Only verified flat token rates enter beichen_price_versions.
ALTER TABLE beichen_panel_snapshots
  ADD COLUMN model_pricing jsonb NOT NULL DEFAULT '{}'::jsonb
  CHECK (jsonb_typeof(model_pricing) = 'object');
