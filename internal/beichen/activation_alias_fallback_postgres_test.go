package beichen

import (
	"testing"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

func (f firstConnectFixture) addSchoolModel(t *testing.T, modelID, status string) uuid.UUID {
	t.Helper()
	id := uuid.New()
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

func TestEnableModelAliasAndUpstreamDifferentLiveTargetsPostgres(t *testing.T) {
	f := newFirstConnectFixture(t)
	upstreamSchool := f.addSchoolModel(t, firstConnectUpstream, "active")
	aliasSchool := f.addSchoolModel(t, "alias-live-school", "active")
	f.configureProductionBothCatalog(t)
	f.putCatalogAlias(t, f.keys[0], "alias-live-school")

	f.assertEnableRejected(t, "", blockerMultiSchool, multiSchoolMessage)
	f.assertSkipped(t, "route_not_ready", blockerMultiSchool, multiSchoolMessage, firstConnectUpstream)
	var bindings, audits int
	if err := f.db.QueryRowContext(f.ctx, `SELECT
 (SELECT count(*) FROM model_bindings WHERE model_id IN ($1,$2) AND provider_id=$3),
 (SELECT count(*) FROM model_multiplier_audits WHERE model_id IN ($1,$2))`, upstreamSchool, aliasSchool, f.provider).
		Scan(&bindings, &audits); err != nil || bindings != 0 || audits != 0 {
		t.Fatalf("ambiguous alias changed a target: bindings=%d audits=%d err=%v", bindings, audits, err)
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
		}
		assertActivationDiagnostics(t, activationErrorDetails(err), want)

		result, err := f.service.EnableModelsBatch(f.ctx, f.provider, f.actor, []string{school.String()})
		if err != nil || len(result.Skipped) != 1 || result.Skipped[0].Code != blockerNoCandidate {
			t.Fatalf("expected one diagnostic skip: %+v err=%v", result, err)
		}
		if !mapsEqual(result.Skipped[0].Details, want) {
			t.Fatalf("batch diagnostics = %+v, want %+v", result.Skipped[0].Details, want)
		}
		f.assertPending(t, school)
	})

	t.Run("missing alias and upstream target", func(t *testing.T) {
		f := newFirstConnectFixture(t)
		f.configureProductionBothCatalog(t)
		f.putCatalogAlias(t, f.keys[0], "missing-old-school")

		_, err := f.service.EnableModel(f.ctx, f.provider, f.actor, firstConnectUpstream, "")
		if activationErrorCode(err) != blockerNoCandidate {
			t.Fatalf("expected no_candidate without a target, got %v", err)
		}
		want := map[string]int{
			diagnosticRows: 1, diagnosticAvailable: 1, diagnosticCatalogCurrent: 1,
			diagnosticAliasMissing: 1, diagnosticTargetMissing: 1, diagnosticKeyListed: 1, diagnosticGroupAllowed: 1,
		}
		assertActivationDiagnostics(t, activationErrorDetails(err), want)
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

func mapsEqual(left, right map[string]int) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range right {
		if left[key] != value {
			return false
		}
	}
	return true
}
