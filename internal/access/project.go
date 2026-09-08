package access

// Role identifies a platform actor.
type Role string

const (
	RoleTeacher Role = "teacher"
	RoleMentor  Role = "mentor"
	RoleStudent Role = "student"
)

// Scope is project-level access mode.
type Scope string

const (
	ScopeRead  Scope = "read"
	ScopeWrite Scope = "write"
)

// Actor is the authenticated user performing an action.
type Actor struct {
	Role Role
}

// PlatformGovernance reports whether the actor may manage platform resources
// (providers, models, organizations, project budgets).
func PlatformGovernance(actor Actor) bool {
	return actor.Role == RoleTeacher
}

// CanAccessProject reports whether the actor may read or write a project.
// Teachers may read any project; mentors may read and write projects they belong to.
func CanAccessProject(actor Actor, scope Scope, isProjectMember bool) bool {
	switch actor.Role {
	case RoleTeacher:
		return scope == ScopeRead
	case RoleMentor:
		return isProjectMember
	default:
		return false
	}
}
