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
	GroupStatus string    `json:"group_status"`
	CheckedAt   time.Time `json:"checked_at"`
	Revision    time.Time `json:"credential_revision"`
}

func saveBillingGroup(ctx context.Context, tx *sql.Tx, credential uuid.UUID, revision, startedAt time.Time, directory websiteKeyCatalog, readErr error) error {
	observation := billingGroupObservation{GroupStatus: "pending", CheckedAt: startedAt, Revision: revision}
	switch {
	case directory.keyGroup != nil:
		observation.WebsiteGroup, observation.CheckedAt = *directory.keyGroup, directory.keyGroupAt
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
	// Billing and Key usage may finish in a different order from the Key list reads.
	_, err = tx.ExecContext(ctx, `
INSERT INTO beichen_datasets AS prior (credential_id,kind,website_group_observation)
VALUES ($1,'billing',$2::jsonb)
ON CONFLICT (credential_id,kind) DO UPDATE SET website_group_observation=excluded.website_group_observation
WHERE (prior.website_group_observation->>'credential_revision')::timestamptz IS DISTINCT FROM $4::timestamptz
   OR (prior.website_group_observation->>'checked_at')::timestamptz <= $3`, credential, string(raw), observation.CheckedAt, revision)
	return err
}

func (item *Dataset) applyBillingGroup(raw []byte, revision time.Time, inventory WebsiteKey) error {
	var observed billingGroupObservation
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &observed); err != nil {
			return err
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
	}
	if inventory.GroupID != nil && inventory.LastSeen != nil &&
		(item.GroupStatus == "" || inventory.LastSeen.After(observed.CheckedAt)) {
		status := "assigned"
		if item.GroupStatus == "not_allowed" && item.GroupID != nil && *item.GroupID == *inventory.GroupID {
			status = "not_allowed"
		}
		item.setBillingGroup(inventory, status)
	}
	return nil
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
