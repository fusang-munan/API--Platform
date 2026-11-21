package controlplane

import "testing"

func TestProgressForTeacherRejection(t *testing.T) {
	progress := progressForStatus("rejected", []AuditView{{Action: "teacher_rejected"}})
	if progress.Current != "rejected_teacher" || len(progress.CompletedSteps) != 2 {
		t.Fatalf("unexpected progress: %#v", progress)
	}
}

func TestProgressForStudentWithdrawn(t *testing.T) {
	progress := progressForStatus("rejected", []AuditView{{Action: "student_withdrawn"}})
	if progress.Current != "withdrawn" || len(progress.CompletedSteps) != 1 || progress.CompletedSteps[0] != "submitted" {
		t.Fatalf("unexpected progress: %#v", progress)
	}
}
