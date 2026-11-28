package testutil

// IsIsolatedPostgresDatabase checks the disposable database names allowed by
// the local PostgreSQL fixtures. Callers also validate the connection.
func IsIsolatedPostgresDatabase(name string) bool {
	switch name {
	case "nebula_test", "nebula_test_hjb", "nebula_test_hjc1", "nebula_test_hjc2",
		"nebula_test_rev_b", "nebula_test_rev_c1", "nebula_test_rev_c1_2",
		"nebula_test_integration", "nebula_test_rev_integration", "nebula_test_rev_state":
		return true
	default:
		return false
	}
}
