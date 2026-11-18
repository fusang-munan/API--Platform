ALTER TABLE gateway_calls
  ADD COLUMN beichen_receipt_response_id uuid,
  ADD COLUMN beichen_receipt_api_key_id bigint,
  ADD COLUMN beichen_receipt_panel_revision timestamptz,
  ADD COLUMN beichen_receipt_conflict boolean NOT NULL DEFAULT false;

CREATE INDEX gateway_calls_beichen_receipt_pending
  ON gateway_calls (beichen_dispatch_credential_id, sent_at, id)
  WHERE beichen_state = 'pending_review' AND beichen_receipt_response_id IS NOT NULL
    AND beichen_receipt_api_key_id IS NOT NULL AND NOT beichen_receipt_conflict;

-- Retain conflicts even if an old gateway call ages out of the call log.
CREATE TABLE beichen_receipt_response_claims (
  response_id uuid PRIMARY KEY,
  first_call_id uuid NOT NULL,
  conflicting_call_id uuid,
  conflicted_at timestamptz,
  CHECK ((conflicting_call_id IS NULL) = (conflicted_at IS NULL))
);

CREATE TABLE beichen_call_receipts (
  row_id bigint PRIMARY KEY CHECK (row_id > 0),
  call_id uuid NOT NULL UNIQUE,
  credential_id uuid NOT NULL,
  response_id uuid NOT NULL UNIQUE,
  api_key_id bigint NOT NULL CHECK (api_key_id > 0),
  model text NOT NULL CHECK (model <> ''),
  occurred_at timestamptz NOT NULL,
  actual_cost numeric NOT NULL CHECK (actual_cost >= 0),
  input_tokens bigint NOT NULL CHECK (input_tokens >= 0),
  output_tokens bigint NOT NULL CHECK (output_tokens >= 0),
  cache_read_tokens bigint NOT NULL CHECK (cache_read_tokens >= 0),
  cache_write_tokens bigint NOT NULL CHECK (cache_write_tokens >= 0),
  credits_booked numeric NOT NULL CHECK (credits_booked >= 0),
  applied boolean NOT NULL,
  recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  CHECK (NOT applied OR credits_booked = actual_cost * 100)
);

CREATE VIEW beichen_receipt_active_bindings AS
  SELECT a.credential_id, a.upstream_key_id, a.updated_at
    FROM beichen_panel_access a
    JOIN provider_credentials c ON c.id = a.credential_id
    JOIN providers p ON p.id = c.provider_id
    WHERE a.upstream_key_id IS NOT NULL AND a.token_ciphertext IS NOT NULL
      AND a.updated_at = c.updated_at AND c.status = 'active' AND p.status = 'active'
      AND lower(btrim(p.base_url)) IN
        ('https://api.beichenapi.com','https://api.beichenapi.com/',
         'https://api.beichenapi.com/v1','https://api.beichenapi.com/v1/');

CREATE FUNCTION pin_beichen_receipt_mapping() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  NEW.beichen_receipt_response_id := NULL;
  NEW.beichen_receipt_api_key_id := NULL;
  NEW.beichen_receipt_panel_revision := NULL;
  -- A conflict on any attempt makes the call ineligible for automatic matching.
  IF NEW.beichen_dispatch_kind = 'beichen' THEN
    SELECT a.upstream_key_id, a.updated_at
      INTO NEW.beichen_receipt_api_key_id, NEW.beichen_receipt_panel_revision
      FROM beichen_receipt_active_bindings a
      WHERE a.credential_id = NEW.beichen_dispatch_credential_id;
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER gateway_calls_pin_beichen_receipt_mapping
  BEFORE UPDATE OF beichen_dispatch_at ON gateway_calls
  FOR EACH ROW EXECUTE FUNCTION pin_beichen_receipt_mapping();
