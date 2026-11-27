package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

type billingGroupObservation struct {
	WebsiteGroup
	GroupStatus        string                         `json:"group_status"`
	CheckedAt          time.Time                      `json:"checked_at"`
	Revision           time.Time                      `json:"credential_revision"`
	WebsiteKeyID       int64                          `json:"website_key_id,omitempty"`
	BillingAccountRate *billingAccountRateObservation `json:"billing_account_rate,omitempty"`
	ConfirmedBinding   *websiteKeyBindingObservation  `json:"confirmed_key_binding,omitempty"`
}

type websiteKeyBindingObservation struct {
	KeyID     int64     `json:"key_id"`
	GroupID   int64     `json:"group_id"` // Zero means the Key list explicitly reported an unassigned Key.
	CheckedAt time.Time `json:"checked_at"`
	Revision  time.Time `json:"credential_revision"`
}

type billingAccountRateObservation struct {
	GroupID     int64       `json:"group_id"`
	KeyID       int64       `json:"key_id,omitempty"`
	Rate        json.Number `json:"rate"`
	Source      string      `json:"source"`
	KeyChecked  time.Time   `json:"key_checked_at"`
	Revision    time.Time   `json:"credential_revision"`
	LastSuccess time.Time   `json:"last_success"`
}

func saveBillingGroup(ctx context.Context, tx *sql.Tx, credential uuid.UUID, revision, startedAt time.Time, directory websiteKeyCatalog, readErr error) error {
	observation := billingGroupObservation{GroupStatus: "pending", CheckedAt: startedAt, Revision: revision}
	switch {
	case directory.keyGroup != nil:
		observation.WebsiteGroup, observation.CheckedAt = *directory.keyGroup, directory.keyGroupAt
		observation.WebsiteKeyID = directory.keyID
		switch {
		case directory.keyGroup.ID == 0:
			observation.GroupStatus = "not_found"
		case errors.Is(readErr, errKeyGroupNotAllowed):
			observation.GroupStatus = "not_allowed"
		default:
			observation.GroupStatus = "assigned"
		}
	case !errors.Is(readErr, errKeyMatch) && !errors.Is(readErr, errPanelGroupUnverified):
		return nil
	}
	raw, err := json.Marshal(observation)
	if err != nil {
		return err
	}
	// Preserve the last billing result when refreshing only the Key identity.
	_, err = tx.ExecContext(ctx, `
INSERT INTO beichen_datasets AS prior (credential_id,kind,website_group_observation)
VALUES ($1,'billing',$2::jsonb)
ON CONFLICT (credential_id,kind) DO UPDATE SET website_group_observation=excluded.website_group_observation ||
  jsonb_strip_nulls(jsonb_build_object(
    'billing_account_rate',prior.website_group_observation->'billing_account_rate',
    'confirmed_key_binding',prior.website_group_observation->'confirmed_key_binding'))
WHERE (prior.website_group_observation->>'credential_revision')::timestamptz IS DISTINCT FROM $4::timestamptz
   OR (prior.website_group_observation->>'checked_at')::timestamptz <= $3`, credential, string(raw), observation.CheckedAt, revision)
	if err != nil || observation.WebsiteKeyID == 0 ||
		(observation.ID == 0 && !errors.Is(readErr, errKeyGroupMissing)) {
		return err
	}
	_, err = saveConfirmedKeyBinding(ctx, tx, credential, websiteKeyBindingObservation{
		KeyID: observation.WebsiteKeyID, GroupID: observation.ID, CheckedAt: observation.CheckedAt, Revision: revision,
	})
	return err
}

func (item *Dataset) applyBillingGroup(raw []byte, revision time.Time, inventory WebsiteKey) (*billingAccountRateObservation, error) {
	var observed billingGroupObservation
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &observed); err != nil {
			return nil, err
		}
	}
	if observed.Revision.Equal(revision) && observed.GroupStatus != "" {
		view := WebsiteKey{}
		if validWebsiteGroupID(observed.ID) {
			view.GroupID = &observed.ID
		}
		view.GroupName = websiteKeyText(observed.Name)
		if observed.Rate != nil {
			view.Rate = websiteGroupRate(*observed.Rate)
		}
		item.setBillingGroup(view, observed.GroupStatus)
		if observed.WebsiteKeyID > 0 && (view.GroupID != nil || observed.GroupStatus == "not_found") {
			item.confirmedKeyBinding = &websiteKeyBindingObservation{
				KeyID: observed.WebsiteKeyID, GroupID: observed.ID, CheckedAt: observed.CheckedAt, Revision: revision,
			}
		}
	}
	if binding := observed.ConfirmedBinding; binding != nil && binding.Revision.Equal(revision) {
		item.confirmedKeyBinding = binding
	}
	if inventory.ID > 0 && inventory.GroupID != nil && inventory.LastSeen != nil &&
		(item.confirmedKeyBinding == nil || inventory.LastSeen.After(item.confirmedKeyBinding.CheckedAt)) {
		item.confirmedKeyBinding = &websiteKeyBindingObservation{
			KeyID: inventory.ID, GroupID: *inventory.GroupID, CheckedAt: *inventory.LastSeen, Revision: revision,
		}
	}
	if inventory.GroupID != nil && inventory.LastSeen != nil &&
		(item.GroupStatus == "" || inventory.LastSeen.After(observed.CheckedAt)) {
		status := "assigned"
		if item.GroupStatus == "not_allowed" && item.GroupID != nil && *item.GroupID == *inventory.GroupID {
			status = "not_allowed"
		}
		item.setBillingGroup(inventory, status)
	}
	if binding := item.confirmedKeyBinding; binding != nil && binding.GroupID == 0 &&
		!binding.CheckedAt.Before(observed.CheckedAt) {
		item.setBillingGroup(WebsiteKey{}, "not_found")
	}
	evidence := observed.BillingAccountRate
	if evidence == nil || !evidence.Revision.Equal(revision) || item.LastSuccess == nil ||
		!evidence.LastSuccess.Equal(*item.LastSuccess) {
		return nil, nil
	}
	return evidence, nil
}

// A billing fetch may finish after a newer Key check. The caller holds the
// credential lock, shared by billing, Key usage, and the website inventory.
func personalRateBindingChanged(ctx context.Context, tx *sql.Tx, credential uuid.UUID, evidence *billingAccountRateObservation) (bool, error) {
	var changed bool
	err := tx.QueryRowContext(ctx, `
SELECT COALESCE((
  SELECT (key_id,group_id) IS DISTINCT FROM ($4::bigint,$5::bigint)
  FROM (
    SELECT (e.value->>'key_id')::bigint AS key_id, (e.value->>'group_id')::bigint AS group_id,
           (e.value->>'checked_at')::timestamptz AS checked_at
    FROM beichen_datasets d
    CROSS JOIN LATERAL (SELECT COALESCE(
      d.website_group_observation->'confirmed_key_binding',
      jsonb_build_object(
        'key_id',d.website_group_observation->'website_key_id',
        'group_id',CASE WHEN d.website_group_observation->>'group_status'='not_found'
          THEN '0'::jsonb ELSE d.website_group_observation->'id' END,
        'checked_at',d.website_group_observation->'checked_at',
        'credential_revision',d.website_group_observation->'credential_revision')
    ) AS value) e
    WHERE d.credential_id=$1 AND d.kind='billing' AND (e.value->>'credential_revision')::timestamptz=$2
    UNION ALL
    SELECT k.upstream_key_id,k.group_id,inv.last_seen FROM provider_credentials c
    JOIN beichen_website_keys k ON k.credential_id=c.id AND k.provider_id=c.provider_id
      AND k.ciphertext_digest=sha256(c.credential_ciphertext) AND k.match_status='matched'
    JOIN beichen_website_key_sync inv ON inv.provider_id=c.provider_id
    WHERE c.id=$1 AND k.group_id IS NOT NULL
  ) known
  WHERE checked_at >= $3 AND key_id IS NOT NULL AND group_id IS NOT NULL
  ORDER BY checked_at DESC, ((key_id,group_id) IS DISTINCT FROM ($4::bigint,$5::bigint)) DESC
  LIMIT 1
),false)`, credential, evidence.Revision, evidence.KeyChecked, evidence.KeyID, evidence.GroupID).Scan(&changed)
	return changed, err
}

// A pending or failed read cannot erase a verified Key binding, including a
// confirmed unassignment, while an older billing fetch is still in flight.
func saveConfirmedKeyBinding(ctx context.Context, tx *sql.Tx, credential uuid.UUID, binding websiteKeyBindingObservation) (bool, error) {
	raw, err := json.Marshal(binding)
	if err != nil {
		return false, err
	}
	var saved bool
	err = tx.QueryRowContext(ctx, `
INSERT INTO beichen_datasets AS prior (credential_id,kind,website_group_observation)
VALUES ($1,'billing',jsonb_build_object('confirmed_key_binding',$2::jsonb))
ON CONFLICT (credential_id,kind) DO UPDATE SET website_group_observation=
  COALESCE(prior.website_group_observation,'{}') || EXCLUDED.website_group_observation
WHERE (prior.website_group_observation#>>'{confirmed_key_binding,credential_revision}')::timestamptz IS DISTINCT FROM $3
   OR (prior.website_group_observation#>>'{confirmed_key_binding,checked_at}')::timestamptz <= $4
RETURNING true`, credential, string(raw), binding.Revision, binding.CheckedAt).Scan(&saved)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return saved, invalidateChangedPersonalRate(ctx, tx, credential, binding)
}

func invalidateChangedPersonalRate(ctx context.Context, tx *sql.Tx, credential uuid.UUID, binding websiteKeyBindingObservation) error {
	var staleAt time.Time
	err := tx.QueryRowContext(ctx, `
UPDATE beichen_multiplier_versions m SET stale_at=clock_timestamp()
FROM beichen_datasets d
WHERE m.id=(SELECT id FROM beichen_multiplier_versions WHERE credential_id=$1 ORDER BY effective_at DESC,id DESC LIMIT 1)
  AND m.account_rate_source='key_billing' AND m.stale_at IS NULL
  AND d.credential_id=$1 AND d.kind='billing'
  AND ((d.website_group_observation#>>'{billing_account_rate,key_id}') IS NULL
    OR (d.website_group_observation#>>'{billing_account_rate,credential_revision}')::timestamptz IS DISTINCT FROM $2
    OR ((d.website_group_observation#>>'{billing_account_rate,key_checked_at}')::timestamptz <= $4
      AND ((d.website_group_observation#>>'{billing_account_rate,key_id}')::bigint,
           (d.website_group_observation#>>'{billing_account_rate,group_id}')::bigint) IS DISTINCT FROM ($3::bigint,$5::bigint)))
RETURNING m.stale_at`, credential, binding.Revision, binding.KeyID, binding.CheckedAt, binding.GroupID).Scan(&staleAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := invalidateChangedPanelQuote(ctx, tx, credential, binding.Revision, staleAt, 0, false); err != nil {
		return err
	}
	return schedulePanelRefresh(ctx, tx, credential)
}

func (item *Dataset) setBillingGroup(key WebsiteKey, status string) {
	sameGroup := item.GroupID != nil && key.GroupID != nil && *item.GroupID == *key.GroupID
	if key.Rate != nil || !sameGroup {
		item.GroupRate = key.Rate
	}
	if key.GroupName != nil || !sameGroup {
		item.GroupName = key.GroupName
	}
	item.GroupID, item.GroupStatus = key.GroupID, status
}

func (item Dataset) groupReadError(message *string) *string {
	// Errors from the old channel lookup can survive an upgrade in several datasets.
	if message == nil || *message != "北辰面板找不到当前 Key 分组" {
		return message
	}
	display := "分组待同步"
	switch item.GroupStatus {
	case "not_found":
		display = errKeyGroupMissing.Error()
	case "not_allowed":
		if item.GroupID != nil {
			display = deniedKeyGroup(*item.GroupID).Error()
		}
	}
	return &display
}
