-- 仅扩展来源枚举，保留来源与基础积分同时为空的约束以及原有扣费恒等式。
DO $$
DECLARE
  source_check record;
BEGIN
  FOR source_check IN
    SELECT conname FROM pg_constraint
    WHERE conrelid = 'gateway_calls'::regclass AND contype = 'c'
      AND pg_get_constraintdef(oid) LIKE '%beichen_base_source%'
      AND pg_get_constraintdef(oid) LIKE '%gateway_usage_price%'
  LOOP
    EXECUTE format('ALTER TABLE gateway_calls DROP CONSTRAINT %I', source_check.conname);
  END LOOP;

  ALTER TABLE gateway_calls ADD CONSTRAINT gateway_calls_beichen_base_source_check
    CHECK (beichen_base_source IN ('gateway_usage_price','receipt_usage_price','receipt_actual_cost','teacher_manual_price'));
END
$$;
