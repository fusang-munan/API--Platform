package usage

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

type StudentTokens struct {
	Input      *string `json:"input"`
	Output     *string `json:"output"`
	CacheRead  *string `json:"cache_read"`
	CacheWrite *string `json:"cache_write"`
	Total      *string `json:"total"`
}

type StudentTokenCoverage struct {
	Calls              int64   `json:"calls"`
	KnownCalls         int64   `json:"known_calls"`
	CompleteCalls      int64   `json:"complete_calls"`
	UnclassifiedTokens *string `json:"unclassified_tokens"`
}

type StudentCacheHitCoverage struct {
	SampleCalls              int64   `json:"sample_calls"`
	CacheReadTokens          *string `json:"cache_read_tokens"`
	InputWithCacheReadTokens *string `json:"input_with_cache_read_tokens"`
}

type StudentUsageMetrics struct {
	Tokens               StudentTokens           `json:"tokens"`
	Completeness         string                  `json:"completeness"`
	TokenCoverage        StudentTokenCoverage    `json:"token_coverage"`
	CacheHitRate         *float64                `json:"cache_hit_rate"`
	CacheHitCoverage     StudentCacheHitCoverage `json:"cache_hit_coverage"`
	PendingReviewCalls   int64                   `json:"pending_review_calls"`
	PendingReviewCredits string                  `json:"pending_review_credits"`
}

type StudentModelUsage struct {
	UsageSlice
	StudentUsageMetrics
}

type StudentUsageTotals struct {
	Credits string `json:"credits"`
	Calls   int64  `json:"calls"`
	StudentUsageMetrics
}

type KeyUsage struct {
	ID              string              `json:"id"`
	Name            string              `json:"name"`
	Status          string              `json:"status"`
	Used            string              `json:"used"`
	Pending         string              `json:"pending"`
	Available       string              `json:"available,omitempty"`
	PendingReview   bool                `json:"pending_review,omitempty"`
	InFlightCredits string              `json:"in_flight_credits"`
	Calls           int64               `json:"calls"`
	PendingCalls    int64               `json:"pending_calls"`
	Models          []StudentModelUsage `json:"models"`
	StudentUsageMetrics
}

type StudentProjectUsage struct {
	ProjectID       string     `json:"project_id"`
	ProjectName     string     `json:"project_name"`
	Quota           string     `json:"quota"`
	Used            string     `json:"used"`
	Pending         string     `json:"pending"`
	Available       string     `json:"available"`
	PendingReview   bool       `json:"pending_review,omitempty"`
	InFlightCredits string     `json:"in_flight_credits"`
	Calls           int64      `json:"calls"`
	Keys            []KeyUsage `json:"keys"`
	StudentUsageMetrics
}

type StudentUsage struct {
	Month    string                `json:"month"`
	Projects []StudentProjectUsage `json:"projects"`
	Totals   StudentUsageTotals    `json:"totals"`
	Models   []StudentModelUsage   `json:"models"`
}

func (s *Store) StudentUsage(ctx context.Context, userID uuid.UUID, month time.Time) (StudentUsage, error) {
	if Month(month).Equal(Month(time.Now())) {
		if err := s.EnsureCurrentBuckets(ctx); err != nil {
			return StudentUsage{}, err
		}
	}
	return s.AggregateStudentUsage(ctx, userID, month)
}

// AggregateStudentUsage reads one student's Beijing ledger month. Callers
// authorize access to userID. The two batched reads share a snapshot with
// each other so a concurrent settlement cannot split the view across states.
func (s *Store) AggregateStudentUsage(ctx context.Context, userID uuid.UUID, month time.Time) (StudentUsage, error) {
	month = Month(month)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return StudentUsage{}, err
	}
	defer func() { _ = tx.Rollback() }()

	view := StudentUsage{
		Month: month.In(shanghai).Format("2006-01"), Projects: []StudentProjectUsage{}, Models: []StudentModelUsage{},
	}
	rollups, err := view.loadRollups(ctx, tx, userID, month)
	if err != nil {
		return StudentUsage{}, err
	}
	if err := view.loadBalances(ctx, tx, userID, month, rollups); err != nil {
		return StudentUsage{}, err
	}
	if err := tx.Commit(); err != nil {
		return StudentUsage{}, err
	}
	return view, nil
}

// Use the stored ledger month, which is pinned during admission even when
// the call is dispatched or settled in a later month. Both facts are first
// reduced to one row per project/key/model for this student.
const studentUsageRollupsSQL = `
WITH owned_keys AS (
 SELECT k.id,k.project_id
 FROM api_keys k JOIN project_members pm ON pm.project_id=k.project_id AND pm.user_id=k.owner_user_id
 WHERE k.owner_user_id=$1
), ledger AS (
 SELECT c.project_id,c.api_key_id,c.model_id,c.model_name,
   c.charged_milli+c.charged_milli_remainder AS milli,c.charged_count+c.zero_cost_count AS calls
 FROM monthly_usage_cube c JOIN owned_keys k ON k.id=c.api_key_id AND k.project_id=c.project_id
 WHERE c.user_id=$1 AND c.month=$2
), observations AS (
 SELECT c.project_id,c.api_key_id,c.model_id,max(c.model_name) AS model_name,
   sum(parts.input_tokens) AS input,sum(parts.output_tokens) AS output,
   sum(parts.cache_read_tokens) AS cache_read,sum(parts.cache_write_tokens) AS cache_write,
   sum(token_evidence.known_total) AS tokens,sum(token_evidence.known_total-breakdown.classified) AS unclassified,
   count(*) AS coverage_calls,count(token_evidence.known_total) AS known_calls,
   count(*) FILTER (WHERE token_evidence.complete) AS complete_calls,
   count(*) FILTER (WHERE c.billing_state='charged') AS recorded_charged_calls,
   count(*) FILTER (WHERE c.beichen_state='pending_review') AS review_calls,
   sum(c.unresolved_credit_milli) FILTER (WHERE c.beichen_state='pending_review') AS review_credits,
   count(*) FILTER (WHERE token_evidence.cache_rate_input>0) AS cache_sample_calls,
   sum(parts.cache_read_tokens) FILTER (WHERE token_evidence.cache_rate_input>0) AS cache_sample_read,
   sum(token_evidence.cache_rate_input) FILTER (WHERE token_evidence.cache_rate_input>0) AS cache_sample_input
 FROM gateway_calls c JOIN owned_keys k ON k.id=c.api_key_id AND k.project_id=c.project_id
` + studentTokenEvidenceSQL + `
 WHERE c.user_id=$1 AND c.month=$2 AND (
   c.billing_state='charged' OR c.beichen_state='pending_review'
   OR (c.outcome<>'pending' AND token_evidence.known_total IS NOT NULL)
 )
 GROUP BY c.project_id,c.api_key_id,c.model_id
), leaves AS (
 SELECT project_id,api_key_id,model_id,COALESCE(l.model_name,o.model_name) AS name,l.milli,l.calls,
   o.input,o.output,o.cache_read,o.cache_write,o.tokens,o.unclassified,o.known_calls,o.complete_calls,
   COALESCE(o.coverage_calls,0)+GREATEST(0,COALESCE(l.calls,0)-COALESCE(o.recorded_charged_calls,0)) AS coverage_calls,
   o.review_calls,o.review_credits,o.cache_sample_calls,o.cache_sample_read,o.cache_sample_input
 FROM ledger l FULL JOIN observations o USING (project_id,api_key_id,model_id)
), rollups AS (
 SELECT project_id,api_key_id,model_id,max(name) AS name,
   COALESCE(sum(milli),0) AS milli,COALESCE(sum(calls),0)::bigint AS calls,
   sum(input) AS input,sum(output) AS output,sum(cache_read) AS cache_read,sum(cache_write) AS cache_write,
   sum(tokens) AS tokens,sum(unclassified) AS unclassified,
   COALESCE(sum(coverage_calls),0)::bigint AS coverage_calls,COALESCE(sum(known_calls),0)::bigint AS known_calls,
   COALESCE(sum(complete_calls),0)::bigint AS complete_calls,
   COALESCE(sum(review_calls),0)::bigint AS review_calls,COALESCE(sum(review_credits),0) AS review_credits,
   COALESCE(sum(cache_sample_calls),0)::bigint AS cache_sample_calls,
   sum(cache_sample_read) AS cache_sample_read,sum(cache_sample_input) AS cache_sample_input
 FROM leaves
 GROUP BY GROUPING SETS ((project_id,api_key_id,model_id),(project_id,api_key_id),(project_id),(model_id),())
)
SELECT r.project_id,r.api_key_id,r.model_id,COALESCE(m.model_id,r.model_id::text,''),COALESCE(r.name,m.display_name,'') AS name,
   trim_scale(r.milli*0.001)::text,r.calls,
   r.input::text,r.output::text,r.cache_read::text,r.cache_write::text,r.tokens::text,
   r.coverage_calls,r.known_calls,r.complete_calls,r.unclassified::text,
   r.review_calls,trim_scale(r.review_credits)::text,
   (r.cache_sample_read/NULLIF(r.cache_sample_input,0))::double precision,
   r.cache_sample_calls,r.cache_sample_read::text,r.cache_sample_input::text
FROM rollups r LEFT JOIN models m ON m.id=r.model_id
ORDER BY r.milli DESC,name,r.model_id`

type studentRollups struct {
	projects map[uuid.UUID]StudentUsageTotals
	keys     map[uuid.UUID]StudentUsageTotals
	models   map[uuid.UUID][]StudentModelUsage
}

func (v *StudentUsage) loadRollups(ctx context.Context, tx *sql.Tx, userID uuid.UUID, month time.Time) (studentRollups, error) {
	rows, err := tx.QueryContext(ctx, studentUsageRollupsSQL, userID, month)
	if err != nil {
		return studentRollups{}, err
	}
	defer rows.Close()

	rollups := studentRollups{
		projects: make(map[uuid.UUID]StudentUsageTotals), keys: make(map[uuid.UUID]StudentUsageTotals),
		models: make(map[uuid.UUID][]StudentModelUsage),
	}
	for rows.Next() {
		var projectID, keyID, modelID uuid.NullUUID
		var id, name string
		var usage StudentUsageTotals
		tokens, coverage, cache := &usage.Tokens, &usage.TokenCoverage, &usage.CacheHitCoverage
		if err := rows.Scan(&projectID, &keyID, &modelID, &id, &name, &usage.Credits, &usage.Calls,
			&tokens.Input, &tokens.Output, &tokens.CacheRead, &tokens.CacheWrite, &tokens.Total,
			&coverage.Calls, &coverage.KnownCalls, &coverage.CompleteCalls, &coverage.UnclassifiedTokens,
			&usage.PendingReviewCalls, &usage.PendingReviewCredits, &usage.CacheHitRate,
			&cache.SampleCalls, &cache.CacheReadTokens, &cache.InputWithCacheReadTokens); err != nil {
			return studentRollups{}, err
		}
		usage.Credits = displayCredits(usage.Credits)
		usage.StudentUsageMetrics = usage.StudentUsageMetrics.forDisplay()
		switch {
		case modelID.Valid:
			model := StudentModelUsage{UsageSlice: UsageSlice{ID: id, Name: name, Credits: usage.Credits, Calls: usage.Calls},
				StudentUsageMetrics: usage.StudentUsageMetrics}
			if keyID.Valid {
				rollups.models[keyID.UUID] = append(rollups.models[keyID.UUID], model)
			} else {
				v.Models = append(v.Models, model)
			}
		case keyID.Valid:
			rollups.keys[keyID.UUID] = usage
		case projectID.Valid:
			rollups.projects[projectID.UUID] = usage
		default:
			v.Totals = usage
		}
	}
	return rollups, rows.Err()
}

func (m StudentUsageMetrics) forDisplay() StudentUsageMetrics {
	switch {
	case m.TokenCoverage.KnownCalls == 0:
		m.Completeness = "unknown"
	case m.TokenCoverage.CompleteCalls == m.TokenCoverage.Calls:
		m.Completeness = "complete"
	default:
		m.Completeness = "partial"
	}
	if m.PendingReviewCredits == "" {
		m.PendingReviewCredits = "0"
	}
	m.PendingReviewCredits = displayCredits(m.PendingReviewCredits)
	return m
}

const studentUsageBalancesSQL = `
SELECT p.id,p.name,COALESCE(mb.quota_milli,pm.monthly_credit_quota_milli,0),COALESCE(mb.pending_milli,0),
   trim_scale((COALESCE(mb.charged_milli,0)+COALESCE(mb.charged_milli_remainder,0))*0.001)::text,
   trim_scale(GREATEST(0,COALESCE(mb.quota_milli,pm.monthly_credit_quota_milli,0)::numeric-
     COALESCE(mb.charged_milli,0)-COALESCE(mb.pending_milli,0)-COALESCE(mb.charged_milli_remainder,0))*0.001)::text,
   pm.monthly_credit_quota_milli>0 OR mb.id IS NOT NULL,
   k.id,k.name,k.status,COALESCE(b.pending_milli,0),
   trim_scale((COALESCE(b.charged_milli,0)+COALESCE(b.charged_milli_remainder,0))*0.001)::text,
   CASE WHEN b.id IS NOT NULL THEN
     trim_scale(GREATEST(0,b.quota_milli::numeric-b.charged_milli-b.pending_milli-b.charged_milli_remainder)*0.001)::text END
FROM project_members pm JOIN projects p ON p.id=pm.project_id
LEFT JOIN project_member_month_credit_buckets mb ON mb.project_id=p.id AND mb.user_id=pm.user_id AND mb.month=$2
LEFT JOIN api_keys k ON k.project_id=p.id AND k.owner_user_id=pm.user_id
LEFT JOIN api_key_month_credit_buckets b ON b.api_key_id=k.id AND b.month=$2
WHERE pm.user_id=$1
ORDER BY p.name,p.id,k.created_at DESC,k.id`

func (v *StudentUsage) loadBalances(ctx context.Context, tx *sql.Tx, userID uuid.UUID, month time.Time, rollups studentRollups) error {
	rows, err := tx.QueryContext(ctx, studentUsageBalancesSQL, userID, month)
	if err != nil {
		return err
	}
	defer rows.Close()

	var project StudentProjectUsage
	var showProject bool
	for rows.Next() {
		var projectID uuid.UUID
		var keyID uuid.NullUUID
		var projectName, projectUsed, projectAvailable, keyUsed string
		var keyName, keyStatus, keyAvailable sql.NullString
		var projectQuota, projectPending, keyPending int64
		var hasProjectQuota bool
		if err := rows.Scan(&projectID, &projectName, &projectQuota, &projectPending, &projectUsed, &projectAvailable,
			&hasProjectQuota, &keyID, &keyName, &keyStatus, &keyPending, &keyUsed, &keyAvailable); err != nil {
			return err
		}
		if project.ProjectID != projectID.String() {
			if showProject {
				v.Projects = append(v.Projects, project)
			}
			stats := rollups.projects[projectID]
			project = StudentProjectUsage{
				ProjectID: projectID.String(), ProjectName: projectName,
				Quota: domain.FormatCredits(projectQuota), Used: displayCredits(projectUsed), Available: displayCredits(projectAvailable),
				Pending: domain.FormatCredits(projectPending), InFlightCredits: domain.FormatCredits(projectPending),
				Calls: stats.Calls, PendingReview: stats.PendingReviewCalls > 0, Keys: []KeyUsage{},
				StudentUsageMetrics: stats.StudentUsageMetrics.forDisplay(),
			}
			showProject = hasProjectQuota
		}
		if !keyID.Valid {
			continue
		}
		status := keyStatus.String
		stats, hasHistory := rollups.keys[keyID.UUID]
		active := status == "approved" || status == "active"
		visible := active || hasHistory || keyUsed != "0" || keyPending > 0
		showProject = showProject || visible || status == "inactive"
		if !visible {
			continue
		}
		key := KeyUsage{
			ID: keyID.UUID.String(), Name: keyName.String, Status: status,
			Used: displayCredits(keyUsed), Pending: domain.FormatCredits(keyPending), InFlightCredits: domain.FormatCredits(keyPending),
			Calls: stats.Calls, PendingCalls: stats.PendingReviewCalls, PendingReview: stats.PendingReviewCalls > 0,
			Models: rollups.models[keyID.UUID], StudentUsageMetrics: stats.StudentUsageMetrics.forDisplay(),
		}
		if keyAvailable.Valid {
			key.Available = displayCredits(keyAvailable.String)
		}
		if key.Models == nil {
			key.Models = []StudentModelUsage{}
		}
		project.Keys = append(project.Keys, key)
	}
	if showProject {
		v.Projects = append(v.Projects, project)
	}
	return rows.Err()
}
