package usage

import (
	"context"
	"database/sql"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

type TeacherStudentUsageTotals struct {
	StudentUsageTotals
	ZeroCostCalls int64 `json:"zero_cost_calls"`
}

type TeacherStudentUsageModel struct {
	StudentModelUsage
	ZeroCostCalls int64 `json:"zero_cost_calls"`
}

type TeacherStudentUsageKey struct {
	ID              string                     `json:"id"`
	Name            string                     `json:"name"`
	Status          string                     `json:"status"`
	Used            string                     `json:"used"`
	Pending         string                     `json:"pending"`
	Available       string                     `json:"available,omitempty"`
	PendingReview   bool                       `json:"pending_review,omitempty"`
	InFlightCredits string                     `json:"in_flight_credits"`
	Calls           int64                      `json:"calls"`
	ZeroCostCalls   int64                      `json:"zero_cost_calls"`
	PendingCalls    int64                      `json:"pending_calls"`
	Models          []TeacherStudentUsageModel `json:"models"`
	StudentUsageMetrics
}

type TeacherStudentUsageProject struct {
	ProjectID       string                   `json:"project_id"`
	ProjectName     string                   `json:"project_name"`
	Quota           string                   `json:"quota"`
	Used            string                   `json:"used"`
	Pending         string                   `json:"pending"`
	Available       string                   `json:"available"`
	PendingReview   bool                     `json:"pending_review,omitempty"`
	InFlightCredits string                   `json:"in_flight_credits"`
	Calls           int64                    `json:"calls"`
	ZeroCostCalls   int64                    `json:"zero_cost_calls"`
	Keys            []TeacherStudentUsageKey `json:"keys"`
	StudentUsageMetrics
}

type TeacherStudentUsageDetail struct {
	TeacherStudentIdentity
	Month    string                       `json:"month"`
	Projects []TeacherStudentUsageProject `json:"projects"`
	Totals   TeacherStudentUsageTotals    `json:"totals"`
	Models   []TeacherStudentUsageModel   `json:"models"`
}

var teacherStudentUsageDetailSQL = teacherStudentUsageFactsSQL(" AND c.user_id=$3") + `,
rollups AS (
 SELECT project_id,api_key_id,model_id,max(user_name) AS user_name,max(project_name) AS project_name,
   max(api_key_name) AS api_key_name,max(model_name) AS model_name,
   COALESCE(sum(zero_cost_count),0)::bigint AS zero_cost_calls,` + studentUsageSumsSQL + `
 FROM leaves
 GROUP BY GROUPING SETS ((project_id,api_key_id,model_id),(project_id,api_key_id),(project_id),(model_id),())
)
SELECT r.project_id,r.api_key_id,r.model_id,COALESCE(u.name,r.user_name,''),u.email::text,
   COALESCE(p.name::text,r.project_name,''),COALESCE(k.name,r.api_key_name,''),COALESCE(k.status,'unknown'),
   COALESCE(m.model_id,r.model_id::text,''),COALESCE(r.model_name,m.display_name,'') AS model_name,
   COALESCE(mb.quota_milli,pm.monthly_credit_quota_milli,0),COALESCE(mb.pending_milli,kb.pending_milli,0),
   CASE WHEN r.project_id IS NOT NULL AND r.api_key_id IS NULL THEN
     trim_scale(GREATEST(0,COALESCE(mb.quota_milli,pm.monthly_credit_quota_milli,0)::numeric
       -COALESCE(mb.charged_milli,0)-COALESCE(mb.charged_milli_remainder,0)-COALESCE(mb.pending_milli,0))*0.001)::text
     WHEN kb.id IS NOT NULL THEN trim_scale(GREATEST(0,kb.quota_milli::numeric
       -kb.charged_milli-kb.charged_milli_remainder-kb.pending_milli)*0.001)::text END,
   r.zero_cost_calls,` + studentUsageTotalsSQL + `
FROM rollups r
LEFT JOIN users u ON u.id=$3 AND r.project_id IS NULL AND r.model_id IS NULL
LEFT JOIN projects p ON p.id=r.project_id AND r.api_key_id IS NULL
LEFT JOIN project_members pm ON pm.project_id=p.id AND pm.user_id=$3
LEFT JOIN project_member_month_credit_buckets mb ON mb.project_id=p.id AND mb.user_id=$3 AND mb.month=$1
LEFT JOIN api_keys k ON k.id=r.api_key_id AND k.project_id=r.project_id AND k.owner_user_id=$3 AND r.model_id IS NULL
LEFT JOIN api_key_month_credit_buckets kb ON kb.api_key_id=k.id AND kb.project_id=r.project_id AND kb.month=$1
LEFT JOIN models m ON m.id=r.model_id
ORDER BY r.project_id NULLS FIRST,r.api_key_id NULLS FIRST,r.model_id IS NOT NULL,r.milli DESC,model_name,r.model_id`

// TeacherStudentUsage reads historical facts in a single snapshot. Live
// project, member and key rows only supply labels, statuses and balances.
func (s *Store) TeacherStudentUsage(ctx context.Context, userID uuid.UUID, scope TeacherStudentUsageScope) (TeacherStudentUsageDetail, error) {
	month := Month(scope.Month)
	rows, err := s.db.QueryContext(ctx, teacherStudentUsageDetailSQL, month, scope.ProjectID, userID)
	if err != nil {
		return TeacherStudentUsageDetail{}, err
	}
	defer rows.Close()
	detail := TeacherStudentUsageDetail{
		TeacherStudentIdentity: TeacherStudentIdentity{UserID: userID},
		Month:                  month.In(shanghai).Format("2006-01"),
		Projects:               []TeacherStudentUsageProject{},
		Models:                 []TeacherStudentUsageModel{},
	}
	// The query places each parent immediately before its children, with
	// the student total and cross-project models first.
	var project *TeacherStudentUsageProject
	var key *TeacherStudentUsageKey
	for rows.Next() {
		var projectID, keyID, modelID uuid.NullUUID
		var userName, projectName, keyName, keyStatus, modelCode, modelName string
		var email *string
		var quotaMilli, pendingMilli int64
		var available sql.NullString
		var stats TeacherStudentUsageTotals
		fields := []any{&projectID, &keyID, &modelID, &userName, &email,
			&projectName, &keyName, &keyStatus, &modelCode, &modelName,
			&quotaMilli, &pendingMilli, &available, &stats.ZeroCostCalls}
		if err := rows.Scan(append(fields, stats.scanFields()...)...); err != nil {
			return TeacherStudentUsageDetail{}, err
		}
		stats.formatForDisplay()
		switch {
		case modelID.Valid:
			model := TeacherStudentUsageModel{
				StudentModelUsage: StudentModelUsage{
					UsageSlice:          UsageSlice{ID: modelCode, Name: modelName, Credits: stats.Credits, Calls: stats.Calls},
					StudentUsageMetrics: stats.StudentUsageMetrics,
				},
				ZeroCostCalls: stats.ZeroCostCalls,
			}
			if keyID.Valid {
				key.Models = append(key.Models, model)
			} else {
				detail.Models = append(detail.Models, model)
			}
		case keyID.Valid:
			pending := domain.FormatCredits(pendingMilli)
			project.Keys = append(project.Keys, TeacherStudentUsageKey{
				ID: keyID.UUID.String(), Name: keyName, Status: keyStatus, Used: stats.Credits,
				Pending: pending, InFlightCredits: pending, PendingReview: stats.PendingReviewCalls > 0,
				Calls: stats.Calls, ZeroCostCalls: stats.ZeroCostCalls, PendingCalls: stats.PendingReviewCalls,
				StudentUsageMetrics: stats.StudentUsageMetrics, Models: []TeacherStudentUsageModel{},
			})
			key = &project.Keys[len(project.Keys)-1]
			if available.Valid {
				key.Available = displayCredits(available.String)
			}
		case projectID.Valid:
			pending := domain.FormatCredits(pendingMilli)
			detail.Projects = append(detail.Projects, TeacherStudentUsageProject{
				ProjectID: projectID.UUID.String(), ProjectName: projectName, Quota: domain.FormatCredits(quotaMilli),
				Used: stats.Credits, Pending: pending, InFlightCredits: pending, Available: displayCredits(available.String),
				PendingReview: stats.PendingReviewCalls > 0, Calls: stats.Calls, ZeroCostCalls: stats.ZeroCostCalls,
				StudentUsageMetrics: stats.StudentUsageMetrics, Keys: []TeacherStudentUsageKey{},
			})
			project = &detail.Projects[len(detail.Projects)-1]
		default:
			detail.UserName, detail.UserEmail, detail.Totals = userName, email, stats
		}
	}
	if err := rows.Err(); err != nil {
		return TeacherStudentUsageDetail{}, err
	}
	if len(detail.Projects) == 0 {
		return TeacherStudentUsageDetail{}, domain.NewError(domain.CodeNotFound, "该学生在所选范围内没有用量记录")
	}
	return detail, nil
}
