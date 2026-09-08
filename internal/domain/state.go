package domain

import "strings"

const (
	KeyPendingMentor  = "pending_mentor"
	KeyPendingTeacher = "pending_teacher"
	KeyApproved       = "approved"
	KeyActive         = "active"
	KeyRejected       = "rejected"
	KeyInactive       = "inactive"
)

func ValidKeyTransition(from, to string) bool {
	switch from {
	case KeyPendingMentor:
		return to == KeyApproved || to == KeyRejected || to == KeyPendingTeacher
	case KeyPendingTeacher:
		return to == KeyApproved || to == KeyRejected || to == KeyPendingMentor
	case KeyApproved:
		return to == KeyActive
	case KeyActive:
		return to == KeyInactive
	case KeyInactive:
		return to == KeyActive
	default:
		return false
	}
}

func RequiresAuditReason(action string) bool {
	switch action {
	case "mentor_rejected", "teacher_rejected":
		return true
	default:
		return false
	}
}

func ValidateAuditComment(action, comment string) error {
	if RequiresAuditReason(action) && strings.TrimSpace(comment) == "" {
		return NewError(CodeValidation, "audit reason is required")
	}
	return nil
}
