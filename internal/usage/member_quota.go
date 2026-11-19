package usage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

const memberAllocatedSQL = `SELECT COALESCE(sum(monthly_credit_quota_milli),0) FROM project_members WHERE project_id=$1`

func (s *Store) MemberQuotaMilli(ctx context.Context, projectID, userID uuid.UUID) (int64, error) {
	var quota int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(monthly_credit_quota_milli,0) FROM project_members WHERE project_id=$1 AND user_id=$2`, projectID, userID).Scan(&quota)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return quota, err
}

func ensureMemberMonthBucketTx(ctx context.Context, tx *sql.Tx, projectID, userID uuid.UUID, month, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO project_member_month_credit_buckets(id,project_id,user_id,month,quota_milli,charged_milli,pending_milli,created_at,updated_at)
SELECT gen_random_uuid(),pm.project_id,pm.user_id,$3,pm.monthly_credit_quota_milli,0,0,$4,$4
FROM project_members pm
WHERE pm.project_id=$1 AND pm.user_id=$2
ON CONFLICT(project_id,user_id,month) DO UPDATE
SET quota_milli=EXCLUDED.quota_milli,updated_at=EXCLUDED.updated_at`, projectID, userID, month, now)
	return err
}

func syncProjectAllocatedTx(ctx context.Context, tx *sql.Tx, projectID uuid.UUID, month, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
UPDATE project_month_credit_buckets pb
SET allocated_milli=COALESCE(sub.total,0),updated_at=$3
FROM (SELECT COALESCE(sum(monthly_credit_quota_milli),0) AS total FROM project_members WHERE project_id=$1) sub
WHERE pb.project_id=$1 AND pb.month=$2`, projectID, month, now)
	return err
}

func insertProjectMonthBucketTx(ctx context.Context, tx *sql.Tx, projectID uuid.UUID, month, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO project_month_credit_buckets(id,project_id,month,quota_milli,allocated_milli,charged_milli,pending_milli,created_at,updated_at)
SELECT gen_random_uuid(),p.id,$2,p.monthly_credit_quota_milli,COALESCE((SELECT sum(monthly_credit_quota_milli) FROM project_members WHERE project_id=p.id),0),0,0,$3,$3
FROM projects p WHERE p.id=$1
ON CONFLICT(project_id,month) DO NOTHING`, projectID, month, now)
	return err
}

func (s *Store) UpdateMemberQuota(ctx context.Context, mentorID, projectID, userID uuid.UUID, quota int64, reason string) error {
	return s.updateMemberQuota(ctx, mentorID, projectID, userID, quota, reason, false)
}

func (s *Store) UpdateStudentQuotaAsTeacher(ctx context.Context, teacherID, projectID, userID uuid.UUID, quota int64, reason string) error {
	return s.updateMemberQuota(ctx, teacherID, projectID, userID, quota, reason, true)
}

func (s *Store) updateMemberQuota(ctx context.Context, actorID, projectID, userID uuid.UUID, quota int64, reason string, asTeacher bool) error {
	if quota < 0 || quota > domain.MaxCreditMilli {
		return domain.NewError(domain.CodeValidation, "monthly credits are out of range")
	}
	reason = strings.TrimSpace(reason)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var allowed bool
	if asTeacher {
		err = tx.QueryRowContext(ctx, `SELECT true FROM projects p
			JOIN users actor ON actor.id=$2 AND actor.role='teacher' AND actor.status='active'
			JOIN users student ON student.id=$3 AND student.role='student' AND student.status='active'
			JOIN student_profiles sp ON sp.user_id=student.id AND sp.status='active'
			WHERE p.id=$1 AND p.status='active' FOR UPDATE OF p FOR SHARE OF actor,student,sp`, projectID, actorID, userID).Scan(&allowed)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT true FROM projects p
			JOIN project_members pm ON pm.project_id=p.id AND pm.user_id=$2
			WHERE p.id=$1 FOR UPDATE OF p FOR SHARE OF pm`, projectID, actorID).Scan(&allowed)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NewError(domain.CodeNotFound, "project member not found")
	}
	if err != nil {
		return err
	}
	var oldQuota int64
	var teacherAssigned bool
	if err = tx.QueryRowContext(ctx, `SELECT monthly_credit_quota_milli,teacher_quota_assigned FROM project_members WHERE project_id=$1 AND user_id=$2 FOR UPDATE`, projectID, userID).Scan(&oldQuota, &teacherAssigned); errors.Is(err, sql.ErrNoRows) {
		return domain.NewError(domain.CodeNotFound, "project member not found")
	} else if err != nil {
		return err
	}
	month, now := Month(time.Now()), time.Now().UTC()
	if err = insertProjectMonthBucketTx(ctx, tx, projectID, month, now); err != nil {
		return err
	}
	var projectQuota int64
	if err = tx.QueryRowContext(ctx, `SELECT quota_milli FROM project_month_credit_buckets WHERE project_id=$1 AND month=$2 FOR UPDATE`, projectID, month).Scan(&projectQuota); err != nil {
		return err
	}
	if err = ensureMemberMonthBucketTx(ctx, tx, projectID, userID, month, now); err != nil {
		return err
	}
	var coversUsage bool
	if err = tx.QueryRowContext(ctx, `SELECT $4::numeric >= charged_milli+charged_milli_remainder+pending_milli FROM project_member_month_credit_buckets WHERE project_id=$1 AND user_id=$2 AND month=$3 FOR UPDATE`, projectID, userID, month, quota).Scan(&coversUsage); err != nil {
		return err
	}
	if !coversUsage {
		return domain.NewError(domain.CodeValidation, "member quota cannot be lower than charged plus pending credits")
	}
	var allocated int64
	if err = tx.QueryRowContext(ctx, memberAllocatedSQL, projectID).Scan(&allocated); err != nil {
		return err
	}
	if allocated-oldQuota+quota > projectQuota {
		return domain.NewError(domain.CodeProjectQuotaAllocation, "project quota allocation exceeded")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE project_members SET monthly_credit_quota_milli=$1,teacher_quota_assigned=(teacher_quota_assigned OR $4) WHERE project_id=$2 AND user_id=$3`, quota, projectID, userID, asTeacher); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE project_member_month_credit_buckets SET quota_milli=$1,updated_at=$2 WHERE project_id=$3 AND user_id=$4 AND month=$5`, quota, now, projectID, userID, month); err != nil {
		return err
	}
	if err = syncProjectAllocatedTx(ctx, tx, projectID, month, now); err != nil {
		return err
	}
	if asTeacher && teacherAssigned && oldQuota == quota {
		return tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO quota_adjustment_audits(id,owner_type,owner_id,actor_user_id,old_quota_milli,new_quota_milli,reason,created_at,project_id) VALUES(gen_random_uuid(),'member',$1,$2,$3,$4,NULLIF($5,''),$6,$7)`, userID, actorID, oldQuota, quota, reason, now, projectID); err != nil {
		return err
	}
	return tx.Commit()
}
