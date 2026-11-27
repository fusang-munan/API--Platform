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
	GroupID   *int64    `json:"group_id,omitempty"` // Zero records a confirmed unassignment.
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

func saveBillingGroup(ctx context.Context, tx *sql.Tx, credential uuid.UUID, revision, startedAt time.Time, directory websiteKeyCatalog, readErr error, revokeLease bool) (bool, error) {
	observation := billingGroupObservation{
		GroupStatus: "pending", CheckedAt: startedAt, Revision: revision, WebsiteKeyID: directory.keyID,
	}
	if !directory.keyGroupAt.IsZero() {
		observation.CheckedAt = directory.keyGroupAt
	}
	var groupID *int64
	switch {
	case directory.keyGroup != nil:
		observation.WebsiteGroup = *directory.keyGroup
		groupID = &directory.keyGroup.ID
		switch {
		case *groupID == 0:
			observation.GroupStatus = "not_found"
		case errors.Is(readErr, errKeyGroupNotAllowed):
			observation.GroupStatus = "not_allowed"
		default:
			observation.GroupStatus = "assigned"
		}
	case !errors.Is(readErr, errKeyMatch) && !errors.Is(readErr, errPanelGroupUnverified):
		return true, nil
	}
	// Check the binding before writing the display observation or usage snapshot.
	if observation.WebsiteKeyID != 0 {
		accepted, err := saveConfirmedKeyBinding(ctx, tx, credential, websiteKeyBindingObservation{
			KeyID: observation.WebsiteKeyID, GroupID: groupID, CheckedAt: observation.CheckedAt, Revision: revision,
		}, revokeLease)
		if err != nil || !accepted {
			return false, err
		}
	}
	raw, err := json.Marshal(observation)
	if err != nil {
		return false, err
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
	return true, err
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
				KeyID: observed.WebsiteKeyID, GroupID: &observed.ID, CheckedAt: observed.CheckedAt, Revision: revision,
			}
		}
	}
	if binding := observed.ConfirmedBinding; binding != nil && binding.Revision.Equal(revision) {
		item.confirmedKeyBinding = binding
	}
	if inventory.ID > 0 && inventory.GroupID != nil && inventory.LastSeen != nil &&
		(item.confirmedKeyBinding == nil || inventory.LastSeen.After(item.confirmedKeyBinding.CheckedAt)) {
		item.confirmedKeyBinding = &websiteKeyBindingObservation{
			KeyID: inventory.ID, GroupID: inventory.GroupID, CheckedAt: *inventory.LastSeen, Revision: revision,
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
	if binding := item.confirmedKeyBinding; binding != nil && !binding.CheckedAt.Before(observed.CheckedAt) {
		switch {
		case binding.GroupID == nil:
			item.setBillingGroup(WebsiteKey{}, "pending")
		case *binding.GroupID == 0:
			item.setBillingGroup(WebsiteKey{}, "not_found")
		}
	}
	evidence := observed.BillingAccountRate
	if evidence == nil || !evidence.Revision.Equal(revision) || item.LastSuccess == nil ||
		!evidence.LastSuccess.Equal(*item.LastSuccess) {
		return nil, nil
	}
	return evidence, nil
}

// The caller holds the credential lock shared by billing, usage, and inventory.
// An external binding change also revokes billing work that has no current quote.
func saveConfirmedKeyBinding(ctx context.Context, tx *sql.Tx, credential uuid.UUID, binding websiteKeyBindingObservation, revokeLease bool) (bool, error) {
	var rawPrevious []byte
	err := tx.QueryRowContext(ctx, `
SELECT website_group_observation->'confirmed_key_binding'
FROM beichen_datasets WHERE credential_id=$1 AND kind='billing'`, credential).Scan(&rawPrevious)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	var previous *websiteKeyBindingObservation
	if len(rawPrevious) > 0 {
		if err := json.Unmarshal(rawPrevious, &previous); err != nil {
			return false, err
		}
	}
	changed := previous != nil && (previous.KeyID != binding.KeyID ||
		(binding.GroupID != nil && (previous.GroupID == nil || *previous.GroupID != *binding.GroupID)))
	if previous != nil && previous.Revision.Equal(binding.Revision) {
		if binding.CheckedAt.Before(previous.CheckedAt) || (changed && binding.CheckedAt.Equal(previous.CheckedAt)) {
			return !changed, nil
		}
		if binding.KeyID == previous.KeyID && binding.GroupID == nil && previous.GroupID != nil {
			return true, nil
		}
	}
	raw, err := json.Marshal(binding)
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO beichen_datasets AS prior (credential_id,kind,website_group_observation)
VALUES ($1,'billing',jsonb_build_object('confirmed_key_binding',$2::jsonb))
ON CONFLICT (credential_id,kind) DO UPDATE SET website_group_observation=
  COALESCE(prior.website_group_observation,'{}') || EXCLUDED.website_group_observation`, credential, string(raw))
	if err != nil {
		return false, err
	}
	retired, err := invalidateChangedBillingRate(ctx, tx, credential, binding, changed)
	if err != nil {
		return false, err
	}
	if retired || (revokeLease && changed) {
		return true, schedulePanelRefresh(ctx, tx, credential)
	}
	return true, nil
}

func invalidateChangedBillingRate(ctx context.Context, tx *sql.Tx, credential uuid.UUID, binding websiteKeyBindingObservation, changed bool) (bool, error) {
	var staleAt time.Time
	err := tx.QueryRowContext(ctx, `
UPDATE beichen_multiplier_versions m SET stale_at=clock_timestamp()
FROM beichen_datasets d
WHERE m.id=(SELECT id FROM beichen_multiplier_versions WHERE credential_id=$1 AND effective_at<=statement_timestamp()
            ORDER BY effective_at DESC,id DESC LIMIT 1)
  AND m.stale_at IS NULL AND d.credential_id=$1 AND d.kind='billing'
  AND ($5::boolean
    OR ($4::bigint IS NOT NULL AND ($4=0 OR m.group_id IS DISTINCT FROM $4))
    OR (m.account_rate_source='key_billing' AND (
      (d.website_group_observation#>>'{billing_account_rate,credential_revision}')::timestamptz IS DISTINCT FROM $2
      OR (d.website_group_observation#>>'{billing_account_rate,key_id}')::bigint IS DISTINCT FROM $3)))
RETURNING m.stale_at`, credential, binding.Revision, binding.KeyID, binding.GroupID, changed).Scan(&staleAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, invalidateChangedPanelQuote(ctx, tx, credential, binding.Revision, staleAt, 0, false)
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
