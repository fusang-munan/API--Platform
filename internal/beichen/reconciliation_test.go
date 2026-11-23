package beichen

import "testing"

func TestReconciliationCoverageSeparatesSyncAndMoney(t *testing.T) {
	for _, tc := range []struct {
		present           int64
		amountsComparable bool
		kind              string
	}{
		{0, false, "none"},
		{1, false, "partial"},
		{2, false, "full"},
		{2, true, "full"},
	} {
		view := ReconciliationView{Coverage: ReconciliationCoverage{ExpectedDays: 2, SyncedDays: tc.present}}
		view.Totals.Money.MarginAvailable = tc.amountsComparable
		view.describeCoverage()
		if view.Coverage.CoverageKind != tc.kind || view.Coverage.Comparable != tc.amountsComparable {
			t.Fatalf("同步覆盖与金额可比分别取证: %+v", view.Coverage)
		}
	}
}
