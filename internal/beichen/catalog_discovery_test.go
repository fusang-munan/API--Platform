package beichen

import "testing"

func TestDiagnoseGroupSeparatesRetainedEntryFromCurrentGroup(t *testing.T) {
	group := int64(60)
	key := discoveryKey{CatalogKey: CatalogKey{GroupID: &group}, groupCurrent: true}
	oldGroup := WebsiteLocation{HasPrice: true, Groups: []WebsiteGroup{{ID: 15}}}
	currentEmpty := WebsiteLocation{Groups: []WebsiteGroup{{ID: 60}}}
	if got := key.diagnoseGroup([]WebsiteLocation{oldGroup}, false); got != "model_outside_key_group" {
		t.Fatalf("retained row outside the selected group = %q, want model_outside_key_group", got)
	}
	if got := key.diagnoseGroup([]WebsiteLocation{oldGroup, currentEmpty}, false); got != "price_outside_key_group" {
		t.Fatalf("current group without a price = %q, want price_outside_key_group", got)
	}
}
