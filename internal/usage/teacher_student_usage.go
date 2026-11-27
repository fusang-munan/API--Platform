package usage

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

type TeacherStudentUsageScope struct {
	Month     time.Time
	ProjectID uuid.NullUUID
}

type TeacherStudentUsageQuery struct {
	TeacherStudentUsageScope
	Search   string
	Sort     string
	Order    string
	Page     int
	PageSize int
}

type TeacherStudentIdentity struct {
	UserID    uuid.UUID `json:"user_id"`
	UserName  string    `json:"user_name"`
	UserEmail *string   `json:"user_email"`
}

type TeacherStudentUsageRow struct {
	TeacherStudentIdentity
	ProjectCount         int64      `json:"project_count"`
	KeyCount             int64      `json:"key_count"`
	Credits              string     `json:"credits"`
	Calls                int64      `json:"calls"`
	ZeroCostCalls        int64      `json:"zero_cost_calls"`
	TotalTokens          *string    `json:"total_tokens"`
	Completeness         string     `json:"completeness"`
	PendingReviewCalls   int64      `json:"pending_review_calls"`
	PendingReviewCredits string     `json:"pending_review_credits"`
	LastCallAt           *time.Time `json:"last_call_at"`
}

type TeacherUnattributedUpstream struct {
	Rows         int64   `json:"rows"`
	DebitRows    int64   `json:"debit_rows"`
	BalanceDebit *string `json:"balance_debit"`
}

type TeacherStudentUsagePage struct {
	Month                string                      `json:"month"`
	Items                []TeacherStudentUsageRow    `json:"items"`
	Page                 int                         `json:"page"`
	PageSize             int                         `json:"page_size"`
	Total                int64                       `json:"total"`
	UnattributedUpstream TeacherUnattributedUpstream `json:"unattributed_upstream"`
}

// The ledger and call snapshot carry the historical student assignment. The
// teacher's scope must not depend on current members, student profiles or keys.
const teacherStudentUsageScopeSQL = `
 c.month=$1 AND ($2::uuid IS NULL OR c.project_id=$2)
 AND c.user_id<>'00000000-0000-0000-0000-000000000000'::uuid`

func teacherStudentUsageFactsSQL(studentScope string) string {
	scope := teacherStudentUsageScopeSQL + studentScope
	return `
WITH ledger AS (
 SELECT c.user_id,c.project_id,c.api_key_id,c.model_id,c.user_name,c.project_name,c.api_key_name,c.model_name,
   c.charged_milli+c.charged_milli_remainder AS milli,c.charged_count+c.zero_cost_count AS calls,c.zero_cost_count
 FROM monthly_usage_cube c WHERE` + scope + `
), observations AS (
 SELECT c.user_id,c.project_id,c.api_key_id,c.model_id,max(c.user_name) AS user_name,
   max(c.project_name) AS project_name,max(c.api_key_name) AS api_key_name,max(c.model_name) AS model_name,
   max(c.created_at) AS last_call_at,` + studentUsageObservationStatsSQL + `
 FROM gateway_calls c` + studentTokenEvidenceSQL + `
 WHERE` + scope + ` AND ` + studentUsageObservationFilterSQL + `
 GROUP BY c.user_id,c.project_id,c.api_key_id,c.model_id
), leaves AS (
 SELECT user_id,project_id,api_key_id,model_id,
   COALESCE(l.user_name,o.user_name) AS user_name,COALESCE(l.project_name,o.project_name) AS project_name,
   COALESCE(l.api_key_name,o.api_key_name) AS api_key_name,COALESCE(l.model_name,o.model_name) AS model_name,
   o.last_call_at,l.zero_cost_count,` + studentUsageLeafStatsSQL + `
 FROM ledger l FULL JOIN observations o USING (user_id,project_id,api_key_id,model_id)
)`
}

var teacherStudentUsageSummariesSQL = teacherStudentUsageFactsSQL("") + `,
summaries AS (
 SELECT user_id,max(user_name) AS recorded_name,count(DISTINCT project_id) AS project_count,
   count(DISTINCT api_key_id) AS key_count,COALESCE(sum(milli),0) AS milli,COALESCE(sum(calls),0)::bigint AS calls,
   COALESCE(sum(zero_cost_count),0)::bigint AS zero_cost_calls,sum(tokens) AS tokens,
   sum(coverage_calls)::bigint AS coverage_calls,COALESCE(sum(known_calls),0)::bigint AS known_calls,
   COALESCE(sum(complete_calls),0)::bigint AS complete_calls,
   COALESCE(sum(review_calls),0)::bigint AS review_calls,COALESCE(sum(review_credits),0) AS review_credits,
   max(last_call_at) AS last_call_at
 FROM leaves GROUP BY user_id
), matches AS (
 SELECT s.*,COALESCE(u.name,s.recorded_name) AS user_name,u.email::text AS user_email
 FROM summaries s LEFT JOIN users u ON u.id=s.user_id
 WHERE $3='' OR strpos(lower(COALESCE(u.name,s.recorded_name)),lower($3))>0
   OR strpos(lower(u.email::text),lower($3))>0
), selected AS (
 SELECT * FROM matches r ORDER BY `

const teacherStudentUsagePageSQL = `
 LIMIT $4 OFFSET $5
)
SELECT t.total,r.user_id,COALESCE(r.user_name,''),r.user_email,COALESCE(r.project_count,0),COALESCE(r.key_count,0),
   trim_scale(COALESCE(r.milli,0)*0.001)::text,COALESCE(r.calls,0),COALESCE(r.zero_cost_calls,0),
   r.tokens::text,COALESCE(r.coverage_calls,0),COALESCE(r.known_calls,0),COALESCE(r.complete_calls,0),
   COALESCE(r.review_calls,0),trim_scale(COALESCE(r.review_credits,0))::text,r.last_call_at
FROM (SELECT count(*) AS total FROM matches) t LEFT JOIN selected r ON true
ORDER BY `

func teacherStudentUsageOrder(sort, order string) (string, error) {
	var column string
	switch sort {
	case "", "credits":
		column = "r.milli"
	case "calls":
		column = "r.calls"
	case "tokens":
		column = "r.tokens"
	default:
		return "", domain.NewError(domain.CodeValidation, "排序字段须为 credits、calls 或 tokens")
	}
	var direction string
	switch order {
	case "", "desc":
		direction = " DESC"
	case "asc":
		direction = " ASC"
	default:
		return "", domain.NewError(domain.CodeValidation, "排序方向须为 asc 或 desc")
	}
	return column + direction + " NULLS LAST, r.user_id", nil
}

// ListTeacherStudentUsage returns only the requested student page. A second
// read in the same snapshot summarizes the school's archived upstream rows.
func (s *Store) ListTeacherStudentUsage(ctx context.Context, q TeacherStudentUsageQuery) (TeacherStudentUsagePage, error) {
	orderBy, err := teacherStudentUsageOrder(q.Sort, q.Order)
	if err != nil {
		return TeacherStudentUsagePage{}, err
	}
	month := Month(q.Month)
	page := TeacherStudentUsagePage{
		Month: month.In(shanghai).Format("2006-01"), Page: q.Page, PageSize: q.PageSize,
		Items: []TeacherStudentUsageRow{},
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return TeacherStudentUsagePage{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := page.loadStudents(ctx, tx, q, orderBy); err != nil {
		return TeacherStudentUsagePage{}, err
	}
	// Upstream rows have no school project or student search scope.
	nextMonth := month.In(shanghai).AddDate(0, 1, 0)
	upstream := &page.UnattributedUpstream
	if err := tx.QueryRowContext(ctx, teacherUnattributedUpstreamSQL, month, nextMonth).
		Scan(&upstream.Rows, &upstream.DebitRows, &upstream.BalanceDebit); err != nil {
		return TeacherStudentUsagePage{}, err
	}
	if err := tx.Commit(); err != nil {
		return TeacherStudentUsagePage{}, err
	}
	return page, nil
}

func (p *TeacherStudentUsagePage) loadStudents(ctx context.Context, tx *sql.Tx, q TeacherStudentUsageQuery, orderBy string) error {
	// ORDER BY is selected from fixed SQL fragments. Values are bound separately.
	rows, err := tx.QueryContext(ctx, teacherStudentUsageSummariesSQL+orderBy+teacherStudentUsagePageSQL+orderBy,
		Month(q.Month), q.ProjectID, q.Search, q.PageSize, int64(q.Page-1)*int64(q.PageSize))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var student TeacherStudentUsageRow
		var userID uuid.NullUUID
		var metrics StudentUsageMetrics
		if err := rows.Scan(&p.Total, &userID, &student.UserName, &student.UserEmail, &student.ProjectCount, &student.KeyCount,
			&student.Credits, &student.Calls, &student.ZeroCostCalls, &student.TotalTokens,
			&metrics.TokenCoverage.Calls, &metrics.TokenCoverage.KnownCalls, &metrics.TokenCoverage.CompleteCalls,
			&metrics.PendingReviewCalls, &metrics.PendingReviewCredits, &student.LastCallAt); err != nil {
			return err
		}
		if !userID.Valid { // Preserve the matching total even past the last page.
			continue
		}
		metrics = metrics.forDisplay()
		student.UserID = userID.UUID
		student.Credits = displayCredits(student.Credits)
		student.Completeness = metrics.Completeness
		student.PendingReviewCalls, student.PendingReviewCredits = metrics.PendingReviewCalls, metrics.PendingReviewCredits
		p.Items = append(p.Items, student)
	}
	return rows.Err()
}

// The upstream archive can prove an owner through a verified receipt or the
// same unconflicted response claim and pinned dispatch details. Ownership does
// not require settlement. Unknown debits are distinct from observed zeroes.
const teacherUnattributedUpstreamSQL = `
SELECT count(*),count(debit),trim_scale(sum(debit))::text FROM (
 SELECT CASE WHEN u.billing_type=0 AND u.subscription_id IS NULL
    AND COALESCE(u.currency,'USD')='USD' THEN u.actual_cost END AS debit
 FROM beichen_upstream_usage_rows u
 WHERE u.occurred_at >= $1 AND u.occurred_at < $2
 AND NOT EXISTS (
   SELECT 1 FROM beichen_receipt_response_claims claim
   JOIN gateway_calls c ON c.id=claim.first_call_id AND c.beichen_receipt_response_id=claim.response_id
   WHERE claim.response_id=CASE
     WHEN u.request_id ~ '^client:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
     THEN substr(u.request_id,8)::uuid END
   AND claim.conflicted_at IS NULL AND NOT c.beichen_receipt_conflict
   AND c.user_id<>'00000000-0000-0000-0000-000000000000'::uuid
   AND (
     (c.beichen_dispatch_kind='beichen' AND c.beichen_dispatch_credential_id=u.credential_id
       AND c.beichen_receipt_api_key_id=u.upstream_key_id AND c.beichen_dispatch_model=u.model
       AND u.occurred_at BETWEEN c.sent_at-interval '2 seconds' AND c.finalized_at+interval '2 minutes')
     OR EXISTS (
       SELECT 1 FROM beichen_call_receipts r WHERE r.row_id=u.row_id AND r.call_id=c.id
         AND r.response_id=claim.response_id AND r.credential_id=u.credential_id
         AND r.api_key_id=u.upstream_key_id AND r.model=u.model AND r.occurred_at=u.occurred_at
     )
   )
 )
) archived`
