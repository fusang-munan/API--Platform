package beichen

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
)

type WebsiteSyncStatus struct {
	Channels                int64      `json:"channels"`
	PricedChannels          int64      `json:"priced_channels"`
	PriceLastSeen           *time.Time `json:"price_last_seen"`
	ReceiptCheckedChannels  int64      `json:"receipt_checked_channels"`
	ReceiptLastChecked      *time.Time `json:"receipt_last_checked"`
	ReceiptCaughtUpChannels int64      `json:"receipt_caught_up_channels"`
}

func readWebsiteSyncStatus(ctx context.Context, tx *sql.Tx, providerID uuid.UUID) (WebsiteSyncStatus, error) {
	var status WebsiteSyncStatus
	err := tx.QueryRowContext(ctx, `
SELECT count(DISTINCT school.channel),
       count(DISTINCT school.channel) FILTER (
           WHERE latest.effective_at = observed.effective_at AND latest.expires_at > statement_timestamp()),
       max(observed.effective_at) AT TIME ZONE 'UTC',
       count(DISTINCT school.channel) FILTER (WHERE cp.last_success IS NOT NULL),
       max(cp.last_success) AT TIME ZONE 'UTC',
       count(DISTINCT school.channel) FILTER (WHERE cp.last_success IS NOT NULL AND cp.caught_up AND cp.last_error IS NULL)
FROM beichen_provision_channels school
JOIN provider_credentials c ON c.id = school.credential_id
JOIN providers p ON p.id = c.provider_id
LEFT JOIN LATERAL (
    -- A later empty or revoked snapshot supersedes a previously usable one.
    SELECT effective_at, expires_at FROM beichen_panel_snapshots
    WHERE credential_id = c.id AND effective_at <= statement_timestamp()
    ORDER BY effective_at DESC LIMIT 1
) latest ON true
LEFT JOIN LATERAL (
    -- Keep the last actual observation for this revision even after expiry or revocation.
    SELECT s.effective_at FROM beichen_panel_snapshots s
    WHERE s.credential_id = c.id AND s.credential_revision = c.updated_at
      AND s.effective_at <= statement_timestamp() AND s.expires_at IS NOT NULL
      AND (EXISTS (
          SELECT 1 FROM jsonb_each(s.model_pricing) AS model(name, terms)
          WHERE btrim(model.name) <> '' AND jsonb_typeof(model.terms) = 'object'
            AND (jsonb_build_array(model.terms) || jsonb_path_query_array(model.terms, '$.intervals[*]')) @?
                'strict $[*] ? (@.type() == "object").keyvalue() ? (
                    @.key like_regex "^(input|output|cache_read|cache_write(_[0-9]+[hm])?|image_input|image_output|per_request)_price$"
                    && ((@.value.type() == "number" && @.value >= 0) ||
                        (@.value.type() == "string" && @.value like_regex "^(0|[1-9][0-9]*)([.][0-9]+)?([eE][+-]?[0-9]+)?$")))')
        OR EXISTS (
          -- Before model_pricing, verified per-model rates were stored alongside the snapshot.
          SELECT 1 FROM beichen_price_versions rate
          WHERE rate.credential_id = s.credential_id AND rate.effective_at = s.effective_at
            AND rate.track = 'upstream' AND rate.source = 'panel'))
    ORDER BY s.effective_at DESC LIMIT 1
) observed ON true
LEFT JOIN beichen_receipt_active_bindings binding ON binding.credential_id = c.id
LEFT JOIN beichen_usage_checkpoints cp ON cp.credential_id = binding.credential_id
    AND cp.revision = binding.updated_at AND cp.upstream_key_id = binding.upstream_key_id
WHERE c.provider_id = $1 AND c.status = 'active' AND p.status = 'active'`, providerID).Scan(
		&status.Channels, &status.PricedChannels, &status.PriceLastSeen,
		&status.ReceiptCheckedChannels, &status.ReceiptLastChecked, &status.ReceiptCaughtUpChannels)
	return status, err
}
