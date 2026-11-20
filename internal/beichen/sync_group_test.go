package beichen

import "testing"

func TestBillingPanelGroupFallsBackToKeyBilling(t *testing.T) {
	group := int64(23)
	for _, tc := range []struct {
		name   string
		mapped int64
		info   billingInfo
		want   int64
	}{
		{"billing group supersedes stale mapping", 17, billingInfo{GroupID: &group}, 23},
		{"key billing without mapping", 0, billingInfo{GroupID: &group}, 23},
		{"trusted mapping when billing omits group", 17, billingInfo{}, 17},
		{"no group", 0, billingInfo{}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := billingPanelGroup(tc.mapped, tc.info); got != tc.want {
				t.Fatalf("billingPanelGroup() = %d, want %d", got, tc.want)
			}
		})
	}
}
