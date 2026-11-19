package usage

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
)

type BeichenSpend struct {
	BaseCredits      *string `json:"beichen_base_credits"`
	BaseSource       *string `json:"beichen_base_source"`
	SchoolMultiplier *string `json:"beichen_school_multiplier"`
	ChargedCredits   *string `json:"beichen_charged_credits"`
	ActualCost       *string `json:"beichen_actual_cost"`
	UnmatchedCalls   int64   `json:"beichen_unmatched_calls"`
}

type ProjectModelUsage struct {
	UsageSlice
	BeichenSpend
}

// Key indexes are local to the owning member's Keys slice.
type memberKey struct{ userID, keyID uuid.UUID }

func (v *ProjectUsage) loadProjectKeys(ctx context.Context, tx *sql.Tx, projectID uuid.UUID, month time.Time, members map[uuid.UUID]int) (map[memberKey]int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT k.owner_user_id,k.id,k.name,k.status,
		trim_scale((b.charged_milli+b.charged_milli_remainder)*0.001)::text,
		EXISTS(SELECT 1 FROM gateway_calls review WHERE review.beichen_state='pending_review'
		       AND review.month=b.month AND review.api_key_id=k.id)
		FROM api_keys k JOIN api_key_month_credit_buckets b ON b.api_key_id=k.id AND b.month=$2
		WHERE k.project_id=$1 AND k.status IN ('approved','active','inactive') ORDER BY k.name,k.id`, projectID, month)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keyIndexes := make(map[memberKey]int)
	for rows.Next() {
		var userID, keyID uuid.UUID
		var key KeyMemberUsage
		if err := rows.Scan(&userID, &keyID, &key.Name, &key.Status, &key.Used, &key.PendingReview); err != nil {
			return nil, err
		}
		if i, ok := members[userID]; ok {
			key.ID = keyID.String()
			key.Used = displayCredits(key.Used)
			key.Models = []ProjectModelUsage{}
			keyIndexes[memberKey{userID, keyID}] = len(v.Members[i].Keys)
			v.Members[i].Keys = append(v.Members[i].Keys, key)
		}
	}
	return keyIndexes, rows.Err()
}

// Aggregate each source before combining dimensions. Joining individual receipts
// to the cube would multiply the school charge by the number of matching calls.
const projectModelUsageSQL = `
WITH facts AS (
	SELECT user_id,api_key_id,model_id,model_name,charged_milli+charged_milli_remainder AS milli,
	       charged_count,zero_cost_count,NULL::numeric AS actual,0 AS unmatched,
	       NULL::numeric AS base,NULL::numeric AS booked,NULL::numeric AS min_factor,NULL::numeric AS max_factor,
	       NULL::text AS min_source,NULL::text AS max_source
	FROM monthly_usage_cube WHERE project_id=$1 AND month=$2
	UNION ALL
	SELECT c.user_id,c.api_key_id,c.model_id,NULL,0,0,0,
	       sum(r.actual_cost),count(*)-count(r.call_id),sum(coalesce(c.beichen_base_credits,c.beichen_credit_exact)),
	       sum(c.beichen_credit_exact),min(coalesce(c.beichen_school_multiplier,1)),max(coalesce(c.beichen_school_multiplier,1)),
	       min(coalesce(c.beichen_base_source,'legacy')) FILTER (WHERE c.beichen_state='charged'),
	       max(coalesce(c.beichen_base_source,'legacy')) FILTER (WHERE c.beichen_state='charged')
	FROM gateway_calls c
	LEFT JOIN beichen_receipt_response_claims claim
	  ON claim.first_call_id=c.id AND claim.response_id=c.beichen_receipt_response_id
	  AND claim.conflicted_at IS NULL AND NOT c.beichen_receipt_conflict
	LEFT JOIN beichen_call_receipts r ON r.call_id=c.id AND r.response_id=claim.response_id
	WHERE c.project_id=$1 AND c.month=$2 AND c.beichen_dispatch_kind='beichen'
	  AND c.beichen_state IN ('charged','pending_review')
	GROUP BY c.user_id,c.api_key_id,c.model_id
)
SELECT f.user_id,f.api_key_id,m.model_id,coalesce(max(f.model_name),max(m.display_name),'') AS name,
       trim_scale(sum(f.milli)*0.001)::text,
       sum(f.charged_count) + CASE WHEN grouping(f.user_id)=1 THEN 0 ELSE sum(f.zero_cost_count) END,
       trim_scale(sum(f.actual))::text,sum(f.unmatched),trim_scale(sum(f.base))::text,trim_scale(sum(f.booked))::text,
       CASE WHEN min(f.min_factor)=max(f.max_factor) THEN min(f.min_factor)::text END,
       CASE WHEN min(f.min_source)=max(f.max_source) THEN min(f.min_source) WHEN min(f.min_source) IS NOT NULL THEN 'mixed' END
FROM facts f LEFT JOIN models m ON m.id=f.model_id
GROUP BY GROUPING SETS ((f.user_id,f.api_key_id,m.model_id), (f.user_id), (m.model_id))
HAVING grouping(m.model_id)=1 OR (m.model_id IS NOT NULL AND
       (grouping(f.user_id)=0 OR sum(f.milli)>0 OR count(f.actual)>0 OR sum(f.unmatched)>0))
ORDER BY sum(f.milli) DESC,name,m.model_id`

func (v *ProjectUsage) loadProjectModelUsage(ctx context.Context, tx *sql.Tx, projectID uuid.UUID, month time.Time, members map[uuid.UUID]int, keys map[memberKey]int) error {
	rows, err := tx.QueryContext(ctx, projectModelUsageSQL, projectID, month)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var userID, keyID uuid.NullUUID
		var modelID sql.NullString
		var model ProjectModelUsage
		if err := rows.Scan(&userID, &keyID, &modelID, &model.Name, &model.Credits, &model.Calls, &model.ActualCost, &model.UnmatchedCalls,
			&model.BaseCredits, &model.ChargedCredits, &model.SchoolMultiplier, &model.BaseSource); err != nil {
			return err
		}
		model.ID = modelID.String
		model.Credits = displayCredits(model.Credits)
		if !userID.Valid {
			v.Models = append(v.Models, model)
			continue
		}
		i, ok := members[userID.UUID]
		if !ok {
			continue
		}
		member := &v.Members[i]
		if !keyID.Valid {
			member.Credits = model.Credits
			member.BeichenSpend = model.BeichenSpend
		} else if j, ok := keys[memberKey{userID.UUID, keyID.UUID}]; ok {
			member.Keys[j].Models = append(member.Keys[j].Models, model)
		}
	}
	return rows.Err()
}
