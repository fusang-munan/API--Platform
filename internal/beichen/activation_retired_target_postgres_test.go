package beichen

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

type retiredTargetShape struct {
	current, retired uuid.UUID
}

func (f firstConnectFixture) retiredTargetShape(t *testing.T, candidateSchool string) retiredTargetShape {
	t.Helper()
	shape := retiredTargetShape{
		current: uuid.New(),
		retired: uuid.New(),
	}
	currentModel := "school-current-" + strings.ReplaceAll(shape.current.String(), "-", "")[:10]
	retiredModel := "school-retired-" + strings.ReplaceAll(shape.retired.String(), "-", "")[:10]
	execCatalogTestSQL(t, f.ctx, f.db, `INSERT INTO models(id,model_id,display_name,status,created_at,updated_at)
VALUES($1,$2,$3,'pending_configuration',now(),now()),
      ($4,$5,$3,'inactive',now(),now())`,
		shape.current, currentModel, firstConnectUpstream, shape.retired, retiredModel)
	f.aliasCatalog(t, f.keys[1], retiredModel)
	f.aliasCatalog(t, f.keys[2], currentModel)
	if candidateSchool != "nil" {
		if err := f.service.PutModelAlias(f.ctx, f.provider, f.actor, &f.keys[0], firstConnectUpstream, &retiredModel); err != nil {
			t.Fatal(err)
		}
		if candidateSchool == "missing" {
			execCatalogTestSQL(t, f.ctx, f.db, `UPDATE upstream_model_aliases SET school_model='missing-school'
WHERE provider_id=$1 AND credential_id=$2 AND upstream_model=$3`, f.provider, f.keys[0], firstConnectUpstream)
		}
	}
	return shape
}

func (f firstConnectFixture) aliasCatalog(t *testing.T, key uuid.UUID, school string) {
	t.Helper()
	saveCatalogFixture(t, f.ctx, f.db, f.provider, key, []catalog.ProviderEntry{
		{ID: firstConnectUpstream, SupportedProtocols: firstConnectAdapters},
	}, nil)
	execCatalogTestSQL(t, f.ctx, f.db, `UPDATE provider_model_catalog SET last_seen_at=now()-interval '1 hour'
WHERE credential_id=$1 AND model_id=$2`, key, firstConnectUpstream)
	if err := f.service.PutModelAlias(f.ctx, f.provider, f.actor, &key, firstConnectUpstream, &school); err != nil {
		t.Fatal(err)
	}
}

func (f firstConnectFixture) assertRetiredTargetShape(t *testing.T, shape retiredTargetShape, wantCandidate uuid.UUID) {
	t.Helper()
	tx, err := f.db.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	models, err := loadActivationCatalog(f.ctx, tx, f.provider, firstConnectUpstream)
	if err != nil || len(models) != 1 {
		t.Fatalf("load production-shaped catalog: models=%d err=%v", len(models), err)
	}
	m := models[0]
	if len(m.schools) != 2 {
		t.Fatalf("expected active and retired schools, got %d: %+v", len(m.schools), m.schools)
	}
	if current := m.schools[shape.current]; current == nil || current.status != "pending_configuration" {
		t.Fatalf("missing pending target: %+v", current)
	}
	if retired := m.schools[shape.retired]; retired == nil || retired.status != "inactive" {
		t.Fatalf("missing inactive target: %+v", retired)
	}
	for _, adapter := range firstConnectAdapters {
		candidates := m.defaults[adapter]
		if len(candidates) != 1 || candidates[0].route.CredentialID == nil || *candidates[0].route.CredentialID != f.keys[0] {
			t.Fatalf("%s candidate: %+v", adapter, candidates)
		}
		if candidates[0].schoolID != wantCandidate {
			t.Fatalf("%s candidate school = %s, want %s", adapter, candidates[0].schoolID, wantCandidate)
		}
	}
}

func (f firstConnectFixture) assertRetiredTargetUntouched(t *testing.T, school uuid.UUID) {
	t.Helper()
	var status string
	var bindings, audits int
	err := f.db.QueryRowContext(f.ctx, `SELECT m.status,
 (SELECT count(*) FROM model_bindings b WHERE b.model_id=m.id),
 (SELECT count(*) FROM model_multiplier_audits a WHERE a.model_id=m.id)
FROM models m WHERE m.id=$1`, school).Scan(&status, &bindings, &audits)
	if err != nil || status != "inactive" || bindings != 0 || audits != 0 {
		t.Fatalf("retired target changed: status=%s bindings=%d audits=%d err=%v", status, bindings, audits, err)
	}
}

func (f firstConnectFixture) assertProductionOverview(t *testing.T, current uuid.UUID) {
	t.Helper()
	view, err := f.service.Overview(f.ctx, f.provider, "")
	if err != nil {
		t.Fatal(err)
	}
	var rows []CatalogModel
	for _, row := range view.ModelCatalog {
		if strings.EqualFold(row.Model, firstConnectUpstream) {
			rows = append(rows, row)
		}
	}
	if len(rows) != 1 || rows[0].SchoolModelID == nil || *rows[0].SchoolModelID != current ||
		!rows[0].CanEnable || rows[0].Activation != "ready" || len(rows[0].Blockers) != 0 {
		t.Fatalf("production-shaped overview should expose only the live target: %+v", rows)
	}
	row := rows[0]
	if row.KeyCount != 1 || len(row.CatalogKeys) != 1 || row.CatalogKeys[0].CredentialID != f.keys[0] ||
		row.PriceState != "confirmed" || row.ProbeState != "passed" || len(row.PreferredRoutes) != len(firstConnectAdapters) {
		t.Fatalf("overview is missing the current Gemini Key, confirmed price, or successful probes and candidate routes: %+v", row)
	}
	for _, adapter := range firstConnectAdapters {
		if !slices.ContainsFunc(row.PreferredRoutes, func(route PreferredRoute) bool {
			return route.CredentialID != nil && *route.CredentialID == f.keys[0] && route.Adapter == adapter &&
				route.SchoolModel == row.SchoolModel && route.State == "candidate"
		}) {
			t.Fatalf("expected a candidate with adapter %s for the live target: %+v", adapter, row.PreferredRoutes)
		}
	}
}

func activationErrorCode(err error) string {
	details := activationErrorDetails(err)
	code, _ := details["code"].(string)
	return code
}

func activationErrorDetails(err error) map[string]any {
	var appErr *domain.Error
	if !errors.As(err, &appErr) {
		return nil
	}
	switch details := appErr.Details.(type) {
	case map[string]any:
		return details
	case map[string]string:
		converted := make(map[string]any, len(details))
		for key, value := range details {
			converted[key] = value
		}
		return converted
	}
	return nil
}

func TestEnableModelRetiredSameNameShapePostgres(t *testing.T) {
	for _, candidateSchool := range []string{"nil", "retired", "missing"} {
		for _, selector := range []string{"name", "school"} {
			name := candidateSchool + "/" + selector
			t.Run(name, func(t *testing.T) {
				f := newFirstConnectFixture(t)
				shape := f.retiredTargetShape(t, candidateSchool)
				wantCandidate := uuid.Nil
				if candidateSchool == "retired" {
					wantCandidate = shape.retired
				}
				f.assertRetiredTargetShape(t, shape, wantCandidate)
				f.assertProductionOverview(t, shape.current)
				source := firstConnectUpstream
				school := ""
				if selector == "school" {
					school = shape.current.String()
					f.assertEnableRejected(t, shape.retired.String(), blockerNoCandidate, "所选校内模型已停用")
					f.assertSkipped(t, "route_not_ready", blockerNoCandidate, "所选校内模型已停用", shape.retired.String())
					f.assertPending(t, shape.current)
				}
				id, err := f.service.EnableModel(f.ctx, f.provider, f.actor, source, school)
				if err != nil || id != shape.current {
					t.Fatalf("%s candidate with %s selector: id=%s err=%v", candidateSchool, selector, id, err)
				}
				bindings := f.assertPublished(t, shape.current, f.keys[0], firstConnectAdapters)
				f.assertRetiredTargetUntouched(t, shape.retired)
				again, err := f.service.EnableModel(f.ctx, f.provider, f.actor, firstConnectUpstream, shape.current.String())
				if err != nil || again != shape.current {
					t.Fatalf("repeat enable: id=%s err=%v", again, err)
				}
				if after := f.assertPublished(t, shape.current, f.keys[0], firstConnectAdapters); after != bindings {
					t.Fatal("repeated enable replaced first-connect bindings")
				}
			})
		}
	}
}

func TestEnableModelMultipleLiveTargetsPostgres(t *testing.T) {
	f := newFirstConnectFixture(t)
	shape := f.retiredTargetShape(t, "nil")
	second := uuid.New()
	secondModel := "school-second-" + strings.ReplaceAll(second.String(), "-", "")[:10]
	execCatalogTestSQL(t, f.ctx, f.db, `INSERT INTO models(id,model_id,display_name,status,created_at,updated_at)
VALUES($1,$2,$3,'pending_configuration',now(),now())`, second, secondModel, firstConnectUpstream)
	f.aliasCatalog(t, f.keys[3], secondModel)

	id, err := f.service.EnableModel(f.ctx, f.provider, f.actor, firstConnectUpstream, "")
	if id != uuid.Nil || domain.ErrorCode(err) != domain.CodeValidation || activationErrorCode(err) != blockerMultiSchool ||
		err.Error() != multiSchoolMessage {
		t.Fatalf("multiple live targets: id=%s code=%s err=%v", id, activationErrorCode(err), err)
	}
	result, err := f.service.EnableModelsBatch(f.ctx, f.provider, f.actor, []string{firstConnectUpstream})
	if err != nil || len(result.Enabled) != 0 || len(result.Skipped) != 1 ||
		result.Skipped[0].Reason != "route_not_ready" || result.Skipped[0].Code != blockerMultiSchool ||
		result.Skipped[0].Message != multiSchoolMessage {
		t.Fatalf("multiple live targets batch: %+v err=%v", result, err)
	}

	view, err := f.service.Overview(f.ctx, f.provider, "")
	if err != nil {
		t.Fatal(err)
	}
	var rows []CatalogModel
	for _, row := range view.ModelCatalog {
		if strings.EqualFold(row.Model, firstConnectUpstream) {
			rows = append(rows, row)
		}
	}
	if len(rows) != 2 {
		t.Fatalf("expected two live catalog rows, got %+v", rows)
	}
	for _, row := range rows {
		if row.CanEnable {
			t.Fatalf("live target should remain blocked: %+v", row)
		}
		if !slices.Contains(row.Blockers, CatalogBlocker{blockerMultiSchool, multiSchoolMessage}) {
			t.Fatalf("live target missing the shared multi_school blocker: %+v", row)
		}
		if slices.ContainsFunc(row.Blockers, func(blocker CatalogBlocker) bool {
			return blocker.Code == blockerNoCandidate
		}) {
			t.Fatalf("live target replaced multi_school with no_candidate: %+v", row)
		}
	}
	f.assertPending(t, shape.current)
	f.assertPending(t, second)
	f.assertRetiredTargetUntouched(t, shape.retired)
}

func TestEnableModelRetiredSameNameBatchPostgres(t *testing.T) {
	for _, candidateSchool := range []string{"nil", "retired"} {
		t.Run(candidateSchool, func(t *testing.T) {
			f := newFirstConnectFixture(t)
			shape := f.retiredTargetShape(t, candidateSchool)
			wantCandidate := uuid.Nil
			if candidateSchool == "retired" {
				wantCandidate = shape.retired
			}
			f.assertRetiredTargetShape(t, shape, wantCandidate)
			result, err := f.service.EnableModelsBatch(f.ctx, f.provider, f.actor, []string{shape.current.String()})
			if err != nil || len(result.Skipped) != 0 || len(result.Enabled) != 1 || result.Enabled[0].ModelID != shape.current {
				t.Fatalf("batch enable: %+v err=%v", result, err)
			}
			f.assertPublished(t, shape.current, f.keys[0], firstConnectAdapters)
			f.assertRetiredTargetUntouched(t, shape.retired)
		})
	}
}
