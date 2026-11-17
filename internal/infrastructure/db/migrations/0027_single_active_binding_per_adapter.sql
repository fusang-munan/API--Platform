-- Keep one active upstream per model+adapter; deactivate duplicates.
UPDATE model_bindings mb
SET status = 'inactive',
    updated_at = now()
WHERE mb.status = 'active'
  AND mb.id NOT IN (
    SELECT DISTINCT ON (model_id, adapter) id
    FROM model_bindings
    WHERE status = 'active'
    ORDER BY model_id, adapter, priority ASC, id ASC
  );
