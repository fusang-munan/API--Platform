package beichen

import (
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

func (f firstConnectFixture) addSchoolModel(t *testing.T, modelID, status string) uuid.UUID {
	t.Helper()
	return f.addSchoolModelWithID(t, uuid.New(), modelID, status)
}

func (f firstConnectFixture) addSchoolModelWithID(t *testing.T, id uuid.UUID, modelID, status string) uuid.UUID {
	t.Helper()
	execCatalogTestSQL(t, f.ctx, f.db, `INSERT INTO models
(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
VALUES($1,$2,$3,$4::text,CASE WHEN $4::text='active' THEN 1000 END,now(),now())`, id, modelID, firstConnectUpstream, status)
	return id
}

func (f firstConnectFixture) putCatalogAlias(t *testing.T, key uuid.UUID, school string) {
	t.Helper()
	execCatalogTestSQL(t, f.ctx, f.db, `INSERT INTO upstream_model_aliases
(provider_id,credential_id,upstream_model,school_model,created_by)
VALUES($1,$2,$3,$4,$5)`, f.provider, key, firstConnectUpstream, school, f.actor)
}

func TestEnableModelMissingAliasFallsBackToUpstreamPostgres(t *testing.T) {
	f := newFirstConnectFixture(t)
	school := f.addSchoolModel(t, firstConnectUpstream, "active")
	f.configureProductionBothCatalog(t)
	f.putCatalogAlias(t, f.keys[0], "missing-old-school")

	id, err := f.service.EnableModel(f.ctx, f.provider, f.actor, firstConnectUpstream, "")
	if err != nil || id != school {
		t.Fatalf("missing alias did not fall back to the upstream target: id=%s want=%s err=%v", id, school, err)
	}
	var status string
	var bindings, matching, audits int
	if err := f.db.QueryRowContext(f.ctx, `SELECT m.status,
 (SELECT count(*) FROM model_bindings b WHERE b.model_id=m.id),
 (SELECT count(*) FROM model_bindings b WHERE b.model_id=m.id AND b.provider_id=$2 AND b.credential_id=$3
   AND b.upstream_model_name=$4 AND b.adapter=ANY($5::text[]) AND b.status='active'),
 (SELECT count(*) FROM model_multiplier_audits a WHERE a.model_id=m.id)
FROM models m WHERE m.id=$1`, school, f.provider, f.keys[0], firstConnectUpstream, firstConnectAdapters).
		Scan(&status, &bindings, &matching, &audits); err != nil || status != "active" || bindings != len(firstConnectAdapters) ||
		matching != bindings || audits != 0 {
		t.Fatalf("fallback did not publish two bindings on the active target: status=%s bindings=%d matching=%d audits=%d err=%v",
			status, bindings, matching, audits, err)
	}
}

func TestEnableModelStaleCandidateFallsBackToUniqueSelectableTargetPostgres(t *testing.T) {
	f := newFirstConnectFixture(t)
	targetID := uuid.MustParse("01a2068e-1111-7222-8333-444455556666")
	f.addSchoolModelWithID(t, targetID, firstConnectUpstream, "active")
	oldID := f.addSchoolModel(t, "old-school-id", "inactive")
	f.configureProductionBothCatalog(t)
	f.putCatalogAlias(t, f.keys[0], "old-school-id")

	tx, err := f.db.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	models, err := loadActivationCatalog(f.ctx, tx, f.provider, firstConnectUpstream)
	if err != nil || len(models) != 1 {
		t.Fatalf("read production-shaped catalog: models=%d err=%v", len(models), err)
	}
	m := models[0]
	target := m.schools[targetID]
	if target == nil || len(m.selectableSchools()) != 1 {
		t.Fatalf("expected one selectable target: %+v", m.schools)
	}
	target.hasActivationMapping = false
	for _, candidates := range m.defaults {
		for i := range candidates {
			if candidates[i].schoolID != oldID {
				t.Fatalf("expected stale candidate school %s, got %+v", oldID, candidates[i])
			}
			candidates[i].fallbackSchoolID = uuid.Nil
		}
	}
	m.updateCandidateDiagnostics()
	wantMissing := map[string]int{
		diagnosticCandidateRows: len(firstConnectAdapters), diagnosticMatchingSchool: 0,
		diagnosticFallbackSchool: 0, diagnosticSchools: 1,
	}
	for key, value := range wantMissing {
		if m.diagnostics[key] != value {
			t.Fatalf("missing fallback diagnostic %s=%d, want %d", key, m.diagnostics[key], value)
		}
	}
	if plan := m.plan(target); plan.blocker.Code != blockerNoCandidate {
		t.Fatalf("stale candidate should currently produce no_candidate: %+v", plan)
	}
	m.applyFallbackSchools()
	m.updateCandidateDiagnostics()
	if m.diagnostics[diagnosticFallbackSchool] != len(firstConnectAdapters) || m.diagnostics[diagnosticSchools] != 1 {
		t.Fatalf("fallback diagnostics did not follow the unique target: %+v", m.diagnostics)
	}
	if plan := m.plan(target); plan.blocker.Code != "" || len(plan.writes) != len(firstConnectAdapters) {
		t.Fatalf("fallback did not restore both protocol candidates: %+v", plan)
	}

	id, err := f.service.EnableModel(f.ctx, f.provider, f.actor, firstConnectUpstream, "")
	if err != nil || id != targetID {
		t.Fatalf("service did not fall back to the unique target: id=%s want=%s err=%v", id, targetID, err)
	}
	var bindings int
	if err := f.db.QueryRowContext(f.ctx, `SELECT count(*) FROM model_bindings
WHERE model_id=$1 AND provider_id=$2 AND credential_id=$3 AND adapter=ANY($4::text[]) AND status='active'`,
		targetID, f.provider, f.keys[0], firstConnectAdapters).Scan(&bindings); err != nil || bindings != len(firstConnectAdapters) {
		t.Fatalf("expected two protocol bindings, got %d err=%v", bindings, err)
	}
}

func TestEnableModelAliasAndUpstreamDifferentLiveTargetsPostgres(t *testing.T) {
	for _, status := range []string{"active", "pending_configuration"} {
		t.Run(status, func(t *testing.T) {
			f := newFirstConnectFixture(t)
			upstreamSchool := f.addSchoolModel(t, firstConnectUpstream, status)
			aliasSchool := f.addSchoolModel(t, "alias-live-school", status)
			f.configureProductionBothCatalog(t)
			f.putCatalogAlias(t, f.keys[0], "alias-live-school")

			view, err := f.service.Overview(f.ctx, f.provider, "")
			if err != nil {
				t.Fatal(err)
			}
			seen := map[uuid.UUID]bool{}
			for _, row := range view.ModelCatalog {
				if row.SchoolModelID == nil || (*row.SchoolModelID != upstreamSchool && *row.SchoolModelID != aliasSchool) {
					continue
				}
				seen[*row.SchoolModelID] = true
				if row.CanEnable || row.Activation != "blocked" ||
					!slices.Contains(row.Blockers, CatalogBlocker{blockerMultiSchool, multiSchoolMessage}) {
					t.Fatalf("%s target escaped the alias conflict: %+v", status, row)
				}
			}
			if !seen[upstreamSchool] || !seen[aliasSchool] {
				t.Fatalf("expected both conflicted targets in overview: %+v", view.ModelCatalog)
			}

			f.assertEnableRejected(t, "", blockerMultiSchool, multiSchoolMessage)
			f.assertSkipped(t, "route_not_ready", blockerMultiSchool, multiSchoolMessage, firstConnectUpstream)
			f.assertEnableRejected(t, aliasSchool.String(), blockerMultiSchool, multiSchoolMessage)
			f.assertSkipped(t, "route_not_ready", blockerMultiSchool, multiSchoolMessage, aliasSchool.String())
			var bindings, audits int
			if err := f.db.QueryRowContext(f.ctx, `SELECT
 (SELECT count(*) FROM model_bindings WHERE model_id IN ($1,$2) AND provider_id=$3),
 (SELECT count(*) FROM model_multiplier_audits WHERE model_id IN ($1,$2))`, upstreamSchool, aliasSchool, f.provider).
				Scan(&bindings, &audits); err != nil || bindings != 0 || audits != 0 {
				t.Fatalf("ambiguous alias changed a target: bindings=%d audits=%d err=%v", bindings, audits, err)
			}
		})
	}
}

func TestEnableModelValidAliasTakesPriorityPostgres(t *testing.T) {
	f := newFirstConnectFixture(t)
	aliasSchool := f.addSchoolModel(t, "alias-existing-school", "pending_configuration")
	f.configureProductionBothCatalog(t)
	f.putCatalogAlias(t, f.keys[0], "alias-existing-school")

	id, err := f.service.EnableModel(f.ctx, f.provider, f.actor, firstConnectUpstream, "")
	if err != nil || id != aliasSchool {
		t.Fatalf("valid alias did not take priority: id=%s want=%s err=%v", id, aliasSchool, err)
	}
	f.assertPublished(t, aliasSchool, f.keys[0], firstConnectAdapters)
}

func TestActivationNoCandidateDiagnosticsPostgres(t *testing.T) {
	t.Run("missing alias with upstream target", func(t *testing.T) {
		f := newFirstConnectFixture(t)
		school := f.addSchoolModel(t, firstConnectUpstream, "pending_configuration")
		f.configureProductionBothCatalog(t)
		f.putCatalogAlias(t, f.keys[0], "missing-old-school")
		execCatalogTestSQL(t, f.ctx, f.db, "UPDATE beichen_website_keys SET enabled=false WHERE provider_id=$1 AND credential_id=$2", f.provider, f.keys[0])

		id, err := f.service.EnableModel(f.ctx, f.provider, f.actor, firstConnectUpstream, "")
		if id != uuid.Nil || domain.ErrorCode(err) != domain.CodeValidation || activationErrorCode(err) != blockerNoCandidate {
			t.Fatalf("expected no_candidate, got id=%s err=%v", id, err)
		}
		want := map[string]int{
			diagnosticRows: 1, diagnosticAvailable: 1, diagnosticCatalogCurrent: 0,
			diagnosticAliasMissing: 1, diagnosticTargetMissing: 0, diagnosticKeyListed: 1, diagnosticGroupAllowed: 0,
			diagnosticCandidateRows: 0, diagnosticMatchingSchool: 0, diagnosticFallbackSchool: 0, diagnosticSchools: 1,
		}
		assertActivationDiagnostics(t, activationErrorDetails(err), want)
		assertActivationPlanDiagnostics(t, activationErrorDetails(err), false, 0, 0)

		result, err := f.service.EnableModelsBatch(f.ctx, f.provider, f.actor, []string{school.String()})
		if err != nil || len(result.Skipped) != 1 || result.Skipped[0].Code != blockerNoCandidate {
			t.Fatalf("expected one diagnostic skip: %+v err=%v", result, err)
		}
		if !mapsEqual(result.Skipped[0].Details, want) {
			t.Fatalf("batch diagnostics = %+v, want %+v", result.Skipped[0].Details, want)
		}
		assertActivationPlanDiagnostics(t, result.Skipped[0].Details, false, 0, 0)
		f.assertPending(t, school)
	})

	t.Run("missing alias and upstream target", func(t *testing.T) {
		f := newFirstConnectFixture(t)
		f.configureProductionBothCatalog(t)
		f.putCatalogAlias(t, f.keys[0], "missing-old-school")
		tx, readErr := f.db.BeginTx(f.ctx, nil)
		if readErr != nil {
			t.Fatal(readErr)
		}
		loaded, loadErr := loadActivationCatalog(f.ctx, tx, f.provider, firstConnectUpstream)
		tx.Rollback()
		if loadErr != nil || len(loaded) != 1 {
			t.Fatalf("load targetless catalog: models=%d err=%v", len(loaded), loadErr)
		}
		if len(loaded[0].schools) != 0 {
			t.Fatalf("fixture unexpectedly has schools: %+v", loaded[0].schools)
		}
		for adapter, candidates := range loaded[0].defaults {
			for _, candidate := range candidates {
				if !candidate.unresolvedAlias {
					t.Fatalf("targetless alias candidate is not unresolved on %s: %+v", adapter, candidate)
				}
			}
		}
		target := loaded[0].target("")
		if target == nil {
			t.Fatal("targetless catalog did not produce a placeholder target")
		}
		if plan := loaded[0].plan(target); plan.blocker.Code != blockerNoCandidate {
			t.Fatalf("targetless plan = %+v, want no_candidate", plan)
		}

		id, err := f.service.EnableModel(f.ctx, f.provider, f.actor, firstConnectUpstream, "")
		if activationErrorCode(err) != blockerNoCandidate {
			t.Fatalf("expected no_candidate without a target, got id=%s err=%v", id, err)
		}
		want := map[string]int{
			diagnosticRows: 1, diagnosticAvailable: 1, diagnosticCatalogCurrent: 1,
			diagnosticAliasMissing: 1, diagnosticTargetMissing: 1, diagnosticKeyListed: 1, diagnosticGroupAllowed: 1,
			diagnosticCandidateRows: 2, diagnosticMatchingSchool: 0, diagnosticFallbackSchool: 0, diagnosticSchools: 0,
		}
		assertActivationDiagnostics(t, activationErrorDetails(err), want)
		assertActivationPlanDiagnostics(t, activationErrorDetails(err), false, 0, 0)
	})
}

func TestDefaultRoutesUniqueNilCandidateUsesFallback(t *testing.T) {
	target := &activationSchool{id: uuid.New(), name: "school-target", status: "active"}
	provider, key := uuid.New(), uuid.New()
	m := &activationModel{
		provider: provider, multiSchoolTargets: false, diagnostics: newActivationDiagnostics(),
		schools: map[uuid.UUID]*activationSchool{target.id: target},
		defaults: map[string][]activationCandidate{
			"openai_responses": {{route: PreferredRoute{UpstreamModel: firstConnectUpstream, ProviderID: provider, CredentialID: &key, Adapter: "openai_responses"}}},
		},
	}
	m.applyFallbackSchools()
	if candidate := m.defaults["openai_responses"][0]; candidate.fallbackSchoolID != target.id {
		t.Fatalf("unique target did not receive a nil-school candidate: %+v", candidate)
	}
	routes, blocker := m.defaultRoutes(target)
	if blocker.Code != "" || len(routes) != 1 || routes[0].SchoolModel != target.name {
		t.Fatalf("unique nil candidate did not match: routes=%+v blocker=%+v", routes, blocker)
	}
	m.updateCandidateDiagnostics()
	if m.diagnostics[diagnosticCandidateRows] != 1 || m.diagnostics[diagnosticMatchingSchool] != 0 ||
		m.diagnostics[diagnosticFallbackSchool] != 1 || m.diagnostics[diagnosticSchools] != 1 {
		t.Fatalf("unexpected nil candidate diagnostics: %+v", m.diagnostics)
	}
}

func TestActivationFallbackDiagnosticsExcludeDirectMatches(t *testing.T) {
	target := &activationSchool{id: uuid.New(), name: "school-target", status: "active"}
	oldTarget, provider, directKey, fallbackKey := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	m := &activationModel{
		provider: provider, diagnostics: newActivationDiagnostics(),
		schools: map[uuid.UUID]*activationSchool{target.id: target},
		defaults: map[string][]activationCandidate{
			"openai_responses": {
				{schoolID: target.id, route: PreferredRoute{UpstreamModel: firstConnectUpstream, ProviderID: provider, CredentialID: &directKey, Adapter: "openai_responses"}},
				{schoolID: oldTarget, route: PreferredRoute{UpstreamModel: firstConnectUpstream, ProviderID: provider, CredentialID: &fallbackKey, Adapter: "openai_responses"}},
			},
		},
	}
	m.applyFallbackSchools()
	direct, fallback := m.defaults["openai_responses"][0], m.defaults["openai_responses"][1]
	if direct.fallbackSchoolID != uuid.Nil || fallback.fallbackSchoolID != target.id {
		t.Fatalf("fallback assignment mixed direct and fallback candidates: direct=%+v fallback=%+v", direct, fallback)
	}
	m.updateCandidateDiagnostics()
	if m.diagnostics[diagnosticCandidateRows] != 2 || m.diagnostics[diagnosticMatchingSchool] != 1 ||
		m.diagnostics[diagnosticFallbackSchool] != 1 || m.diagnostics[diagnosticSchools] != 1 {
		t.Fatalf("direct match was counted as fallback: %+v", m.diagnostics)
	}
}

func TestEnableModelAliasAndUpstreamSameKeyUsesCanonicalNamePostgres(t *testing.T) {
	f := newFirstConnectFixture(t)
	school := f.addSchoolModel(t, firstConnectUpstream, "pending_configuration")
	const aliasUpstream = "gemini-4-argon-alias"
	saveCatalogFixture(t, f.ctx, f.db, f.provider, f.keys[0], []catalog.ProviderEntry{
		{ID: firstConnectUpstream, SupportedProtocols: firstConnectAdapters},
		{ID: aliasUpstream, SupportedProtocols: firstConnectAdapters},
	}, nil)
	schoolName := firstConnectUpstream
	if err := f.service.PutModelAlias(f.ctx, f.provider, f.actor, &f.keys[0], aliasUpstream, &schoolName); err != nil {
		t.Fatal(err)
	}
	f.configureProductionBothCatalog(t)

	result, err := f.service.EnableModelsBatch(f.ctx, f.provider, f.actor, []string{school.String()})
	if err != nil || len(result.Enabled) != 1 || len(result.Skipped) != 0 || result.Enabled[0].ModelID != school {
		t.Fatalf("same-key alias/upstream candidates should enable: %+v err=%v", result, err)
	}
	f.assertPublished(t, school, f.keys[0], firstConnectAdapters)
}

func TestSelectActivationRouteCanonicalAndAmbiguity(t *testing.T) {
	provider := uuid.New()
	keyA, keyB := uuid.New(), uuid.New()
	route := func(key uuid.UUID, upstream string, priority int) PreferredRoute {
		return PreferredRoute{UpstreamModel: upstream, ProviderID: provider, CredentialID: &key, Priority: priority, Adapter: "openai_responses"}
	}
	t.Run("target name wins and duplicates collapse", func(t *testing.T) {
		selected, blocker := selectActivationRoute([]PreferredRoute{
			route(keyA, "alias-upstream", 1), route(keyA, "school-target", 10), route(keyA, "school-target", 10),
		}, "school-target", "")
		if blocker.Code != "" || selected.UpstreamModel != "school-target" {
			t.Fatalf("canonical candidate was not selected: selected=%+v blocker=%+v", selected, blocker)
		}
	})
	t.Run("different key remains ambiguous", func(t *testing.T) {
		_, blocker := selectActivationRoute([]PreferredRoute{route(keyA, "alias-a", 10), route(keyB, "alias-b", 10)}, "school-target", "")
		if blocker.Code != blockerMultiCandidate {
			t.Fatalf("different keys should stay ambiguous: %+v", blocker)
		}
	})
	t.Run("same-key different upstream uses priority", func(t *testing.T) {
		selected, blocker := selectActivationRoute([]PreferredRoute{route(keyA, "alias-a", 20), route(keyA, "alias-b", 10)}, "school-target", "")
		if blocker.Code != "" || selected.UpstreamModel != "alias-b" {
			t.Fatalf("priority did not disambiguate: selected=%+v blocker=%+v", selected, blocker)
		}
	})
}

func assertActivationDiagnostics(t *testing.T, details map[string]any, want map[string]int) {
	t.Helper()
	for key, expected := range want {
		got, ok := details[key].(int)
		if !ok || got != expected {
			t.Fatalf("diagnostic %s = %#v, want %d; all=%+v", key, details[key], expected, details)
		}
	}
}

func assertActivationPlanDiagnostics(t *testing.T, details map[string]any, multiplierSet bool, writes, routes int) {
	t.Helper()
	want := map[string]any{
		"north_ready": false, "ambiguous": false, "catalog_blocked": false, "foreign_active": false,
		"multiplier_set": multiplierSet, "writes": writes, "routes": routes,
	}
	for key, value := range want {
		if details[key] != value {
			t.Fatalf("plan diagnostic %s = %#v, want %#v; all=%+v", key, details[key], value, details)
		}
	}
}

func mapsEqual(left map[string]any, right map[string]int) bool {
	for key, value := range right {
		got, ok := left[key].(int)
		if !ok || got != value {
			return false
		}
	}
	return true
}
