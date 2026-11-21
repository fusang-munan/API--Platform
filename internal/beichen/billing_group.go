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

func saveBillingGroup(ctx context.Context, tx *sql.Tx, credential uuid.UUID, revision time.Time, directory websiteKeyCatalog, err error) error {
	if directory.keyGroup == nil {
		if errors.Is(err, errKeyMatch) || errors.Is(err, errPanelGroupUnverified) {
			_, err := tx.ExecContext(ctx, `UPDATE beichen_datasets SET website_group_observation=NULL
WHERE credential_id=$1 AND kind='billing'`, credential)
			return err
		}
		return nil
	}
	status := "assigned"
	switch {
	case directory.keyGroup.ID == 0:
		status = "not_found"
	case errors.Is(err, errKeyGroupNotAllowed):
		status = "not_allowed"
	}
	observation, err := json.Marshal(billingGroupObservation{
		WebsiteGroup: *directory.keyGroup, GroupStatus: status, CheckedAt: directory.keyGroupAt, Revision: revision,
	})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
UPDATE beichen_datasets SET website_group_observation=$2::jsonb
WHERE credential_id=$1 AND kind='billing'`, credential, string(observation))
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
