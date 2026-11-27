package usage

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"golang.org/x/sync/singleflight"
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
)`

const teacherStudentUsageColumnsSQL = `
   r.user_id,COALESCE(r.user_name,''),r.user_email,COALESCE(r.project_count,0),COALESCE(r.key_count,0),
   trim_scale(COALESCE(r.milli,0)*0.001)::text,COALESCE(r.calls,0),COALESCE(r.zero_cost_calls,0),
   r.tokens::text,COALESCE(r.coverage_calls,0),COALESCE(r.known_calls,0),COALESCE(r.complete_calls,0),
   COALESCE(r.review_calls,0),trim_scale(COALESCE(r.review_credits,0))::text,r.last_call_at`

var teacherStudentUsageSnapshotSQL = teacherStudentUsageSummariesSQL + `
SELECT ` + teacherStudentUsageColumnsSQL + `,
   COALESCE(lower(r.user_name),''),COALESCE(lower(r.user_email),'')
FROM matches r LIMIT $4`

func teacherStudentUsagePageSQL(orderBy string) string {
	// ORDER BY is selected from fixed SQL fragments. Values are bound separately.
	return teacherStudentUsageSummariesSQL + `,
selected AS (SELECT * FROM matches r ORDER BY ` + orderBy + ` LIMIT $4 OFFSET $5)
SELECT t.total,` + teacherStudentUsageColumnsSQL + `
FROM (SELECT count(*) AS total FROM matches) t LEFT JOIN selected r ON true
ORDER BY ` + orderBy
}

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

const (
	teacherUsageSnapshotTTL       = 5 * time.Second
	teacherUsageQueryTimeout      = 3 * time.Second
	teacherUsageMaxSnapshots      = 8
	teacherUsageMaxCachedStudents = 10_000
	teacherUsageMaxPage           = 1<<31 - 1
	teacherUsageMaxPageSize       = 100
)

type teacherUsageSnapshotKey struct {
	db    *sql.DB
	scope TeacherStudentUsageScope
}

type teacherUsageSnapshot struct {
	students    []teacherUsageSummary
	upstream    TeacherUnattributedUpstream
	uncacheable bool
	expiresAt   time.Time
}

// One report cache per process, with independent entries for each DB and
// report scope. Each snapshot is immutable after the read transaction commits.
var teacherUsageSnapshots = struct {
	sync.Mutex
	entries map[teacherUsageSnapshotKey]*teacherUsageSnapshot
	loads   singleflight.Group
}{entries: make(map[teacherUsageSnapshotKey]*teacherUsageSnapshot)}

func cachedTeacherUsageSnapshot(key teacherUsageSnapshotKey) *teacherUsageSnapshot {
	teacherUsageSnapshots.Lock()
	defer teacherUsageSnapshots.Unlock()
	snapshot := teacherUsageSnapshots.entries[key]
	if snapshot != nil && !time.Now().Before(snapshot.expiresAt) {
		delete(teacherUsageSnapshots.entries, key)
		return nil
	}
	return snapshot
}

func cacheTeacherUsageSnapshot(key teacherUsageSnapshotKey, snapshot *teacherUsageSnapshot) {
	teacherUsageSnapshots.Lock()
	defer teacherUsageSnapshots.Unlock()
	now := time.Now()
	oldest := key
	for candidate, value := range teacherUsageSnapshots.entries {
		if !now.Before(value.expiresAt) {
			delete(teacherUsageSnapshots.entries, candidate)
		} else if previous := teacherUsageSnapshots.entries[oldest]; previous == nil || value.expiresAt.Before(previous.expiresAt) {
			oldest = candidate
		}
	}
	if len(teacherUsageSnapshots.entries) >= teacherUsageMaxSnapshots {
		delete(teacherUsageSnapshots.entries, oldest)
	}
	teacherUsageSnapshots.entries[key] = snapshot
}

// ListTeacherStudentUsage pages over exact summaries. The students and the
// school's upstream totals come from a single repeatable-read snapshot.
func (s *Store) ListTeacherStudentUsage(ctx context.Context, q TeacherStudentUsageQuery) (TeacherStudentUsagePage, error) {
	orderBy, err := teacherStudentUsageOrder(q.Sort, q.Order)
	if err != nil {
		return TeacherStudentUsagePage{}, err
	}
	if q.Page < 1 || q.Page > teacherUsageMaxPage || q.PageSize < 1 || q.PageSize > teacherUsageMaxPageSize {
		return TeacherStudentUsagePage{}, domain.NewError(domain.CodeValidation, "分页参数无效")
	}
	q.Month = Month(q.Month)
	if !q.ProjectID.Valid {
		q.ProjectID = uuid.NullUUID{}
	}
	ctx, cancel := context.WithTimeout(ctx, teacherUsageQueryTimeout)
	defer cancel()
	snapshot, err := s.teacherUsageSnapshot(ctx, q.TeacherStudentUsageScope)
	if err != nil {
		return TeacherStudentUsagePage{}, err
	}
	page := TeacherStudentUsagePage{
		Month: q.Month.In(shanghai).Format("2006-01"), Page: q.Page, PageSize: q.PageSize,
		Items: []TeacherStudentUsageRow{},
	}
	// Large schools still get an exact report. Leave filtering and pagination
	// in PostgreSQL when a complete report would exceed the cache budget.
	if snapshot.uncacheable {
		if err := s.loadTeacherUsagePage(ctx, &page, q, orderBy); err != nil {
			return TeacherStudentUsagePage{}, err
		}
		return page, nil
	}
	search := q.Search
	if search != "" {
		// Keep the database's case rules for both the search term and names.
		if err := s.db.QueryRowContext(ctx, `SELECT lower($1::text)`, search).Scan(&search); err != nil {
			return TeacherStudentUsagePage{}, err
		}
	}
	snapshot.fillPage(&page, q, search)
	return page, nil
}

func (s *Store) teacherUsageSnapshot(ctx context.Context, scope TeacherStudentUsageScope) (*teacherUsageSnapshot, error) {
	key := teacherUsageSnapshotKey{db: s.db, scope: scope}
	if snapshot := cachedTeacherUsageSnapshot(key); snapshot != nil {
		return snapshot, nil
	}
	load := teacherUsageSnapshots.loads.DoChan(fmt.Sprintf("%p/%s/%v", key.db, scope.Month, scope.ProjectID), func() (any, error) {
		if snapshot := cachedTeacherUsageSnapshot(key); snapshot != nil {
			return snapshot, nil
		}
		// A cancelled client must not cancel the same snapshot for other
		// waiters. Bound the shared transaction independently.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), teacherUsageQueryTimeout)
		defer cancel()
		snapshot, err := s.loadTeacherUsageSnapshot(ctx, scope)
		if err == nil {
			cacheTeacherUsageSnapshot(key, snapshot)
		}
		return snapshot, err
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-load:
		if result.Err != nil {
			return nil, result.Err
		}
		return result.Val.(*teacherUsageSnapshot), nil
	}
}

func (s *Store) beginTeacherUsageReport(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL statement_timeout = '3s'`); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func (s *Store) loadTeacherUsageSnapshot(ctx context.Context, scope TeacherStudentUsageScope) (*teacherUsageSnapshot, error) {
	snapshot := &teacherUsageSnapshot{expiresAt: time.Now().Add(teacherUsageSnapshotTTL)}
	tx, err := s.beginTeacherUsageReport(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := snapshot.loadStudents(ctx, tx, scope); err != nil {
		return nil, err
	}
	if !snapshot.uncacheable {
		if err := snapshot.upstream.load(ctx, tx, scope.Month); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (s *Store) loadTeacherUsagePage(ctx context.Context, page *TeacherStudentUsagePage, q TeacherStudentUsageQuery, orderBy string) error {
	tx, err := s.beginTeacherUsageReport(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := page.loadStudents(ctx, tx, q, orderBy); err != nil {
		return err
	}
	if err := page.UnattributedUpstream.load(ctx, tx, q.Month); err != nil {
		return err
	}
	return tx.Commit()
}

func (u *TeacherUnattributedUpstream) load(ctx context.Context, tx *sql.Tx, month time.Time) error {
	// Upstream rows have no school project or student search scope.
	nextMonth := month.In(shanghai).AddDate(0, 1, 0)
	return tx.QueryRowContext(ctx, teacherUnattributedUpstreamSQL, month, nextMonth).Scan(&u.Rows, &u.DebitRows, &u.BalanceDebit)
}

type teacherUsageSummary struct {
	row                     TeacherStudentUsageRow
	userID                  uuid.NullUUID
	email, tokens           sql.NullString
	lastCallAt              sql.NullTime
	metrics                 StudentUsageMetrics
	searchName, searchEmail string
	creditsOrder            big.Rat
	tokensOrder             big.Int
}

func (s *teacherUsageSummary) scanFields() []any {
	r, m := &s.row, &s.metrics
	return []any{&s.userID, &r.UserName, &s.email, &r.ProjectCount, &r.KeyCount,
		&r.Credits, &r.Calls, &r.ZeroCostCalls, &s.tokens,
		&m.TokenCoverage.Calls, &m.TokenCoverage.KnownCalls, &m.TokenCoverage.CompleteCalls,
		&m.PendingReviewCalls, &m.PendingReviewCredits, &s.lastCallAt}
}

func (s *teacherUsageSummary) result() TeacherStudentUsageRow {
	row, metrics := s.row, s.metrics.forDisplay()
	row.UserID, row.Credits = s.userID.UUID, displayCredits(row.Credits)
	row.Completeness = metrics.Completeness
	row.PendingReviewCalls, row.PendingReviewCredits = metrics.PendingReviewCalls, metrics.PendingReviewCredits
	if s.email.Valid {
		email := s.email.String
		row.UserEmail = &email
	}
	if s.tokens.Valid {
		tokens := s.tokens.String
		row.TotalTokens = &tokens
	}
	if s.lastCallAt.Valid {
		lastCallAt := s.lastCallAt.Time
		row.LastCallAt = &lastCallAt
	}
	return row
}

func (s *teacherUsageSnapshot) loadStudents(ctx context.Context, tx *sql.Tx, scope TeacherStudentUsageScope) error {
	rows, err := tx.QueryContext(ctx, teacherStudentUsageSnapshotSQL, scope.Month, scope.ProjectID, "", teacherUsageMaxCachedStudents+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if len(s.students) == teacherUsageMaxCachedStudents {
			s.students = nil
			s.uncacheable = true
			break
		}
		var summary teacherUsageSummary
		if err := rows.Scan(append(summary.scanFields(), &summary.searchName, &summary.searchEmail)...); err != nil {
			return err
		}
		// Use the exact decimal and integer values for sorting, including
		// tiny credit differences and token totals wider than 64 bits.
		if _, ok := summary.creditsOrder.SetString(summary.row.Credits); !ok {
			return fmt.Errorf("invalid credits in student usage report: %q", summary.row.Credits)
		}
		if summary.tokens.Valid {
			if _, ok := summary.tokensOrder.SetString(summary.tokens.String, 10); !ok {
				return fmt.Errorf("invalid tokens in student usage report: %q", summary.tokens.String)
			}
		}
		s.students = append(s.students, summary)
	}
	return rows.Err()
}

func (s *teacherUsageSnapshot) fillPage(p *TeacherStudentUsagePage, q TeacherStudentUsageQuery, search string) {
	p.UnattributedUpstream = s.upstream
	if s.upstream.BalanceDebit != nil {
		debit := *s.upstream.BalanceDebit
		p.UnattributedUpstream.BalanceDebit = &debit
	}
	offset := int64(q.Page-1) * int64(q.PageSize)
	if search == "" && offset >= int64(len(s.students)) {
		p.Total = int64(len(s.students))
		return
	}
	matches := make([]*teacherUsageSummary, 0, len(s.students))
	for i := range s.students {
		student := &s.students[i]
		if strings.Contains(student.searchName, search) || strings.Contains(student.searchEmail, search) {
			matches = append(matches, student)
		}
	}
	p.Total = int64(len(matches))
	if offset >= p.Total {
		return
	}
	slices.SortFunc(matches, func(a, b *teacherUsageSummary) int {
		var order int
		switch q.Sort {
		case "calls":
			order = cmp.Compare(a.row.Calls, b.row.Calls)
		case "tokens":
			switch {
			case a.tokens.Valid && !b.tokens.Valid:
				return -1
			case !a.tokens.Valid && b.tokens.Valid:
				return 1
			default:
				order = a.tokensOrder.Cmp(&b.tokensOrder)
			}
		default:
			order = a.creditsOrder.Cmp(&b.creditsOrder)
		}
		if order == 0 {
			return bytes.Compare(a.userID.UUID[:], b.userID.UUID[:])
		}
		if q.Order == "asc" {
			return order
		}
		return -order
	})
	start := int(offset)
	for _, summary := range matches[start:min(start+q.PageSize, len(matches))] {
		p.Items = append(p.Items, summary.result())
	}
}

func (p *TeacherStudentUsagePage) loadStudents(ctx context.Context, tx *sql.Tx, q TeacherStudentUsageQuery, orderBy string) error {
	rows, err := tx.QueryContext(ctx, teacherStudentUsagePageSQL(orderBy),
		q.Month, q.ProjectID, q.Search, q.PageSize, int64(q.Page-1)*int64(q.PageSize))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var student teacherUsageSummary
		if err := rows.Scan(append([]any{&p.Total}, student.scanFields()...)...); err != nil {
			return err
		}
		if student.userID.Valid { // Preserve the matching total even past the last page.
			p.Items = append(p.Items, student.result())
		}
	}
	return rows.Err()
}

// A verified receipt proves ownership for its archived row. Without a
// receipt, the response claim and pinned dispatch must identify exactly one
// archived row, including candidates across ledger months.
const teacherUnattributedUpstreamSQL = `
SELECT count(*),count(debit),trim_scale(sum(debit))::text FROM (
 SELECT CASE WHEN u.billing_type=0 AND u.subscription_id IS NULL
    AND COALESCE(u.currency,'USD')='USD' THEN u.actual_cost END AS debit
 FROM beichen_upstream_usage_rows u
 WHERE u.occurred_at >= $1 AND u.occurred_at < $2
 AND NOT EXISTS (
   SELECT 1 FROM beichen_call_receipts r
   JOIN beichen_receipt_response_claims claim ON claim.response_id=r.response_id AND claim.first_call_id=r.call_id
   JOIN gateway_calls c ON c.id=r.call_id AND c.beichen_receipt_response_id=r.response_id
   WHERE r.row_id=u.row_id AND r.credential_id=u.credential_id
   AND r.api_key_id=u.upstream_key_id AND r.model=u.model AND r.occurred_at=u.occurred_at
   AND claim.conflicted_at IS NULL AND NOT c.beichen_receipt_conflict
   AND c.user_id<>'00000000-0000-0000-0000-000000000000'::uuid
 )
 AND NOT EXISTS (
   SELECT 1 FROM beichen_receipt_response_claims claim
   JOIN gateway_calls c ON c.id=claim.first_call_id AND c.beichen_receipt_response_id=claim.response_id
   WHERE claim.response_id=CASE
     WHEN u.request_id ~ '^client:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
     THEN substr(u.request_id,8)::uuid END
   AND claim.conflicted_at IS NULL AND NOT c.beichen_receipt_conflict
   AND c.user_id<>'00000000-0000-0000-0000-000000000000'::uuid
   AND c.beichen_dispatch_kind='beichen' AND c.beichen_dispatch_credential_id=u.credential_id
   AND c.beichen_receipt_api_key_id=u.upstream_key_id AND c.beichen_dispatch_model=u.model
   AND u.occurred_at BETWEEN c.sent_at-interval '2 seconds' AND c.finalized_at+interval '2 minutes'
   AND NOT EXISTS (
     SELECT 1 FROM beichen_call_receipts r WHERE r.call_id=c.id OR r.response_id=claim.response_id
     OR (r.row_id=u.row_id AND r.credential_id=u.credential_id AND r.api_key_id=u.upstream_key_id)
   )
   AND NOT EXISTS (
     SELECT 1 FROM beichen_upstream_usage_rows other
     WHERE other.credential_id=u.credential_id AND other.upstream_key_id=u.upstream_key_id
     AND other.model=u.model AND other.request_id=u.request_id
     AND other.occurred_at BETWEEN c.sent_at-interval '2 seconds' AND c.finalized_at+interval '2 minutes'
     AND (other.upstream_user_id,other.row_id)<>(u.upstream_user_id,u.row_id)
   )
 )
) archived`
