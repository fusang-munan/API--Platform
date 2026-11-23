-- Keep the raw usage object when upstream counters contradict each other.
ALTER TABLE beichen_probe_usage ADD COLUMN raw_usage jsonb;
