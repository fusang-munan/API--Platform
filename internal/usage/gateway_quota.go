package usage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

const (
	StudentConcurrencyExceeded = "student_concurrency_exceeded"
	PendingReviewLimit         = "pending_review_limit"
	UnresolvedExposureLimit    = "unresolved_exposure_limit"
)

// Take the student row lock in both Reserve and StartAttempt. The limit check
// must run in a later statement so it sees the dispatch that held the lock.
const activeCallAccessSQL = `
FROM projects p
JOIN api_keys k ON k.project_id=p.id
JOIN organizations o ON o.id=p.organization_id
JOIN users u ON u.id=k.owner_user_id
JOIN project_members pm ON pm.project_id=p.id AND pm.user_id=u.id
JOIN api_key_models km ON km.api_key_id=k.id
JOIN models m ON m.id=km.model_id
WHERE k.id=$1 AND m.id=$2 AND k.status='active' AND m.status='active'
  AND u.status='active' AND p.status='active' AND o.status='active'
FOR NO KEY UPDATE OF u FOR SHARE OF p,k,o,pm,km,m`

const callDispatchStartedSQL = `c.sent_at IS NOT NULL OR c.beichen_dispatch_at IS NOT NULL OR EXISTS (
	SELECT 1 FROM gateway_call_attempts a WHERE a.call_id=c.id AND a.status='connecting'
)`

func callAccessError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NewError(domain.CodeNotFound, "active gateway access is unavailable")
	}
	return err
}

func studentCallLimit(name string, fallback int) int {
	limit, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || limit < 1 {
		return fallback
	}
	return limit
}

func pendingReserveCredits() int {
	return studentCallLimit("NEBULA_STUDENT_PENDING_RESERVE_CREDITS", 10)
}

func checkStudentCallLimits(ctx context.Context, tx *sql.Tx, studentID, ownCallID uuid.UUID) error {
	concurrency := studentCallLimit("NEBULA_STUDENT_CONCURRENCY_LIMIT", 2)
	exposureLimit := studentCallLimit("NEBULA_STUDENT_UNRESOLVED_CREDIT_LIMIT", 1000)
	var concurrentFull, exposureFull bool
	err := tx.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM gateway_calls c WHERE c.user_id=$1 AND c.id<>$2
			AND c.billing_state='pending' AND (`+callDispatchStartedSQL+`) OFFSET $3),
		(SELECT COALESCE(SUM(c.unresolved_credit_milli),0) + $4::bigint >= $5::bigint
		 FROM gateway_calls c WHERE c.user_id=$1 AND c.beichen_state='pending_review')`,
		studentID, ownCallID, concurrency-1, pendingReserveCredits(), exposureLimit).Scan(&concurrentFull, &exposureFull)
	if err != nil {
		return err
	}
	if concurrentFull {
		return &QuotaError{Code: StudentConcurrencyExceeded}
	}
	if exposureFull {
		return &QuotaError{Code: UnresolvedExposureLimit}
	}
	return nil
}

// StartAttempt takes an in-flight slot and saves the exact outgoing limit
// before any request bytes can be sent. A reservation alone takes no slot.
func (s *Store) StartAttempt(ctx context.Context, r Reservation, route CallRoute, quotaBudgetTokens int64) (uuid.UUID, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback()
	var studentID uuid.UUID
	if err = tx.QueryRowContext(ctx, `SELECT u.id `+activeCallAccessSQL, r.APIKeyID, r.ModelID).Scan(&studentID); err != nil {
		return uuid.Nil, callAccessError(err)
	}
	if err = checkStudentCallLimits(ctx, tx, studentID, r.CallID); err != nil {
		return uuid.Nil, err
	}
	id := uuid.Must(uuid.NewV7())
	result, err := tx.ExecContext(ctx, `WITH started AS (
		INSERT INTO gateway_call_attempts(id,call_id,provider_id,binding_id,provider_name,status,created_at)
		VALUES($1,$2,$3,$4,$5,'connecting',now()) RETURNING call_id
	)
	UPDATE gateway_calls SET provider_id=$3,binding_id=$4,provider_name=$5,
		provider_credential_id=$6,upstream_model_name=NULLIF($7,''),
		quota_budget_tokens=NULLIF($8::bigint,0),lease_expires_at=now()+interval '15 minutes'
	WHERE id=(SELECT call_id FROM started) AND user_id=$9 AND api_key_id=$10 AND model_id=$11 AND billing_state='pending'`,
		id, r.CallID, route.ProviderID, route.BindingID, route.ProviderName, nullableUUID(route.CredentialID), route.UpstreamModelName,
		quotaBudgetTokens, studentID, r.APIKeyID, r.ModelID)
	if err := requireLedgerRow(result, err); err != nil {
		return uuid.Nil, err
	}
	if err := tx.Commit(); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// BeichenOutputPrice returns a confirmed, effective student price in yuan per
// million output tokens. Follow rates are upstream micro-units and manual
// rates are micro-credits, in the same proportions as the billing ledger.
func (s *Store) BeichenOutputPrice(ctx context.Context, route CallRoute) (string, error) {
	var price sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT
		trim_scale(CASE WHEN p.mode='manual' OR version.source IS DISTINCT FROM $4 THEN
			p.school_output * CASE WHEN p.mode='manual' THEN 0.00000001 ELSE 0.000001 END *
			CASE WHEN EXISTS (
				SELECT 1 FROM beichen_provision_channels school
				JOIN provider_credentials pc ON pc.id=school.credential_id
				WHERE school.credential_id=$1 AND pc.provider_id=$3
			) THEN 1.10 ELSE 1 END
		END)::text
		FROM beichen_price_at($1,$2,statement_timestamp()) p
		LEFT JOIN beichen_price_versions version ON version.id=p.upstream_version`,
		route.CredentialID, route.UpstreamModelName, route.ProviderID, derivedPriceSource).
		Scan(&price)
	return price.String, err
}
