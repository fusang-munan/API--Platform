package access

import "testing"

func TestPlatformGovernance(t *testing.T) {
	t.Parallel()
	if !PlatformGovernance(Actor{Role: RoleTeacher}) {
		t.Fatal("teacher should govern platform")
	}
	if PlatformGovernance(Actor{Role: RoleMentor}) {
		t.Fatal("mentor should not govern platform")
	}
}

func TestCanAccessProject(t *testing.T) {
	t.Parallel()
	teacher := Actor{Role: RoleTeacher}
	mentor := Actor{Role: RoleMentor}
	if !CanAccessProject(teacher, ScopeRead, false) {
		t.Fatal("teacher should read any project")
	}
	if CanAccessProject(teacher, ScopeWrite, false) {
		t.Fatal("teacher should not write projects")
	}
	if !CanAccessProject(mentor, ScopeWrite, true) {
		t.Fatal("mentor should write member projects")
	}
	if CanAccessProject(mentor, ScopeWrite, false) {
		t.Fatal("mentor should not write non-member projects")
	}
}
