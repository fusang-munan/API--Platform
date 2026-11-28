package controlplane

import (
	"testing"

	"github.com/starprince1234/Nebula-api/internal/catalog"
)

func TestMergeStudentPlazaModelsDedupesCommonAndLibrary(t *testing.T) {
	plaza := StudentModelPlazaView{
		CommonModels: []ModelView{{ModelID: "gpt-4o", DisplayName: "GPT-4o", IsCommon: true}},
		LibraryModels: []ModelView{
			{ModelID: "gpt-4o", DisplayName: "GPT-4o"},
			{ModelID: "claude-3", DisplayName: "Claude 3"},
		},
	}
	merged := mergeStudentPlazaModels(plaza)
	if len(merged) != 2 {
		t.Fatalf("expected 2 merged models, got %d", len(merged))
	}
	if merged[0].ModelID != "claude-3" || merged[1].ModelID != "gpt-4o" {
		t.Fatalf("unexpected merge order: %#v", merged)
	}
}

func TestFilterStudentVisibleModelsRequiresConfiguredCredits(t *testing.T) {
	multiplier := "1.00"
	items := []ModelView{
		{ModelID: "ready", Status: "active", RouteReady: true, CreditMultiplier: &multiplier},
		{ModelID: "missing", Status: "active", RouteReady: true, CreditMultiplier: nil},
	}
	filtered := filterStudentVisibleModels(items)
	if len(filtered) != 1 || filtered[0].ModelID != "ready" {
		t.Fatalf("expected only configured model, got %#v", filtered)
	}
}

func TestStudentDirectorySharesAddEligibilityWithCatalogRows(t *testing.T) {
	multiplier := "1.00"
	item := func(id, state string, ready bool) ModelView {
		view := ModelView{ModelID: id, Status: "active", RouteReady: ready, CreditMultiplier: &multiplier}
		view.ModelAvailability = &catalog.ModelAvailability{Availability: state, AvailabilityState: state}
		return view
	}
	cases := []struct {
		name, withGrant, withoutGrant string
		addable                       bool
		view                          ModelView
	}{
		{"available", "callable", "addable", true, item("available", catalog.AvailabilityAvailable, true)},
		{"unverified stays addable", "unverified", "addable", true, item("unverified", catalog.AvailabilityUnverified, true)},
		{"failed probe", "historical", "maintenance", false, item("failed", "probe_failed", true)},
		{"no route", "historical", "maintenance", false, item("no-route", catalog.AvailabilityMaintenance, false)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := studentDirectoryModel(tc.view)
			if got.WithGrant != tc.withGrant || got.WithoutGrant != tc.withoutGrant || got.Addable != tc.addable {
				t.Fatalf("groups/addable = %q/%q/%t, want %q/%q/%t", got.WithGrant, got.WithoutGrant, got.Addable,
					tc.withGrant, tc.withoutGrant, tc.addable)
			}
			if raw, ok := got.RawEntry["addable"].(bool); !ok || raw != tc.addable {
				t.Fatalf("raw addable = %#v, want %t", got.RawEntry["addable"], tc.addable)
			}
		})
	}
	unpriced := item("unpriced", catalog.AvailabilityAvailable, true)
	unpriced.CreditMultiplier = nil
	if studentModelAddable(unpriced) {
		t.Fatal("model without a student price must not be addable")
	}
}
