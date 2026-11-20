package beichen

import "testing"

func TestBillingPanelGroupFallsBackToKeyBilling(t *testing.T) {
	group := int64(23)
	for _, tc := range []struct {
		name   string
		access PanelAccessStatus
		info   billingInfo
		want   int64
	}{
		{"bound access", PanelAccessStatus{GroupID: 17}, billingInfo{GroupID: &group}, 17},
		{"key billing fallback", PanelAccessStatus{}, billingInfo{GroupID: &group}, 23},
		{"no group", PanelAccessStatus{}, billingInfo{}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := billingPanelGroup(tc.access, tc.info); got != tc.want {
				t.Fatalf("billingPanelGroup() = %d, want %d", got, tc.want)
			}
		})
	}
}
