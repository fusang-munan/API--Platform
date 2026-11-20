package beichen

import "testing"

func TestBillingPanelGroupRequiresCurrentBillingID(t *testing.T) {
	group, unsafe := int64(23), int64(1<<53)
	for _, tc := range []struct {
		name string
		info billingInfo
		want int64
	}{
		{"billing group", billingInfo{GroupID: &group}, 23},
		{"group omitted", billingInfo{}, 0},
		{"unsafe numeric ID", billingInfo{GroupID: &unsafe}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := billingPanelGroup(tc.info); got != tc.want {
				t.Fatalf("billingPanelGroup() = %d, want %d", got, tc.want)
			}
		})
	}
}
