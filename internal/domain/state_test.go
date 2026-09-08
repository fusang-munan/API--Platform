package domain

import "testing"

func TestValidKeyTransition(t *testing.T) {
	t.Parallel()
	valid := [][2]string{
		{KeyPendingMentor, KeyApproved},
		{KeyPendingMentor, KeyRejected},
		{KeyPendingMentor, KeyPendingTeacher},
		{KeyPendingTeacher, KeyApproved},
		{KeyPendingTeacher, KeyRejected},
		{KeyPendingTeacher, KeyPendingMentor},
		{KeyApproved, KeyActive},
		{KeyActive, KeyInactive},
		{KeyInactive, KeyActive},
	}
	for _, transition := range valid {
		if !ValidKeyTransition(transition[0], transition[1]) {
			t.Fatalf("expected transition %s -> %s to be valid", transition[0], transition[1])
		}
	}
	for _, transition := range [][2]string{
		{KeyPendingMentor, KeyActive},
		{KeyPendingTeacher, KeyActive},
		{KeyApproved, KeyRejected},
	} {
		if ValidKeyTransition(transition[0], transition[1]) {
			t.Fatalf("expected transition %s -> %s to be invalid", transition[0], transition[1])
		}
	}
}

func TestAuditReasonRequired(t *testing.T) {
	t.Parallel()
	if err := ValidateAuditComment("mentor_deactivated", ""); err != nil {
		t.Fatalf("deactivation comment should be optional: %v", err)
	}
	if err := ValidateAuditComment("teacher_rejected", "model is not approved"); err != nil {
		t.Fatalf("expected rejection reason to be accepted: %v", err)
	}
	if err := ValidateAuditComment("teacher_approved", ""); err != nil {
		t.Fatalf("approval comment should be optional: %v", err)
	}
}
