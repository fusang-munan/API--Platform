package controlplane

import "testing"

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
