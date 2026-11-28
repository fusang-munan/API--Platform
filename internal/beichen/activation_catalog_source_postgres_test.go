package beichen

import (
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/db/ent/modelbinding"
)

func (f firstConnectFixture) configureProductionBothCatalog(t *testing.T) {
	t.Helper()
	key := f.keys[0]
	var revision time.Time
	if err := f.db.QueryRowContext(f.ctx, "SELECT updated_at FROM provider_credentials WHERE id=$1", key).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := saveWebsiteCatalog(f.ctx, tx, key, revision, panelQuote{
		groupID: 31, websiteModels: []string{firstConnectUpstream},
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	execCatalogTestSQL(t, f.ctx, f.db, "UPDATE provider_model_catalog SET catalog_source='both' WHERE credential_id=$1 AND model_id=$2", key, firstConnectUpstream)
	execCatalogTestSQL(t, f.ctx, f.db, "UPDATE beichen_website_keys SET group_id=31,rate=0.5 WHERE provider_id=$1 AND credential_id=$2", f.provider, key)
	execCatalogTestSQL(t, f.ctx, f.db, "UPDATE beichen_panel_access SET group_id=31 WHERE credential_id=$1", key)
	execCatalogTestSQL(t, f.ctx, f.db, "UPDATE beichen_multiplier_versions SET group_id=31,multiplier=0.5 WHERE credential_id=$1", key)
	execCatalogTestSQL(t, f.ctx, f.db, "UPDATE beichen_panel_snapshots SET group_id=31 WHERE credential_id=$1", key)
	execCatalogTestSQL(t, f.ctx, f.db, "UPDATE beichen_price_versions SET group_id=31 WHERE credential_id=$1 AND model=$2", key, firstConnectUpstream)
}

func (f firstConnectFixture) configureProductionBothTarget(t *testing.T) uuid.UUID {
	t.Helper()
	school := f.unmappedSchool(t)
	f.configureProductionBothCatalog(t)
	return school
}

func (f firstConnectFixture) catalogRow(t *testing.T, school uuid.UUID) CatalogModel {
	t.Helper()
	view, err := f.service.Overview(f.ctx, f.provider, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range view.ModelCatalog {
		if row.SchoolModelID != nil && *row.SchoolModelID == school {
			return row
		}
	}
	t.Fatalf("missing catalog row for %s", school)
	return CatalogModel{}
}

func TestEnableModelCatalogSourceBothPostgres(t *testing.T) {
	for _, mode := range []string{"single-name", "single-uuid", "batch-uuid"} {
		t.Run(mode, func(t *testing.T) {
			f := newFirstConnectFixture(t)
			school := f.configureProductionBothTarget(t)
			row := f.catalogRow(t, school)
			if row.Source != "both" || row.KeyCount != 1 || len(row.CatalogKeys) != 1 ||
				row.CatalogKeys[0].CredentialID != f.keys[0] || row.CatalogKeys[0].State != "listed" ||
				row.PriceState != "confirmed" || row.ProbeState != "passed" || !row.CanEnable || row.Activation != "ready" {
				t.Fatalf("production-shaped both row is not ready: %+v", row)
			}
			if len(row.PreferredRoutes) != len(firstConnectAdapters) {
				t.Fatalf("expected two protocol candidates: %+v", row.PreferredRoutes)
			}
			for _, route := range row.PreferredRoutes {
				if route.CredentialID == nil || *route.CredentialID != f.keys[0] ||
					route.SchoolModel != row.SchoolModel || route.EffectiveMultiplier == nil ||
					route.EffectiveMultiplier.String() != "0.5" {
					t.Fatalf("unexpected both candidate: %+v", route)
				}
			}

			var id uuid.UUID
			var err error
			switch mode {
			case "single-name":
				id, err = f.service.EnableModel(f.ctx, f.provider, f.actor, firstConnectUpstream, "")
			case "single-uuid":
				id, err = f.service.EnableModel(f.ctx, f.provider, f.actor, firstConnectUpstream, school.String())
			case "batch-uuid":
				var result BatchEnableResult
				result, err = f.service.EnableModelsBatch(f.ctx, f.provider, f.actor, []string{school.String()})
				if len(result.Enabled) == 1 {
					id = result.Enabled[0].ModelID
				}
				if len(result.Skipped) != 0 {
					t.Fatalf("both batch was skipped: %+v", result)
				}
			}
			if err != nil || id != school {
				t.Fatalf("both first connect failed: id=%s err=%v", id, err)
			}
			f.assertPublished(t, school, f.keys[0], firstConnectAdapters)

			client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, f.db)))
			t.Cleanup(func() { _ = client.Close() })
			key := f.keys[0]
			for _, adapter := range firstConnectAdapters {
				ready, err := catalog.BindingRouteReady(f.ctx, client, &ent.ModelBinding{
					ProviderID: f.provider, CredentialID: &key, UpstreamModelName: firstConnectUpstream,
					Adapter: modelbinding.Adapter(adapter), Status: modelbinding.StatusActive,
				})
				if err != nil || !ready {
					t.Fatalf("%s binding does not treat both as Key-backed: ready=%t err=%v", adapter, ready, err)
				}
			}
		})
	}
}

func TestEnableModelRestoresInactiveProtocolSlotsPostgres(t *testing.T) {
	for _, mode := range []string{"single-name", "batch-uuid"} {
		t.Run(mode, func(t *testing.T) {
			f := newFirstConnectFixture(t)
			school := f.configureProductionBothTarget(t)
			oldKey := f.keys[1]
			f.bindKey(t, school, oldKey)
			execCatalogTestSQL(t, f.ctx, f.db, "UPDATE provider_credentials SET status='inactive',retired_at=now() WHERE id=$1", oldKey)
			execCatalogTestSQL(t, f.ctx, f.db, "UPDATE provider_model_catalog SET status='inactive' WHERE credential_id=$1", oldKey)
			execCatalogTestSQL(t, f.ctx, f.db, "UPDATE model_bindings SET status='inactive' WHERE model_id=$1 AND provider_id=$2", school, f.provider)
			before := f.bindingSnapshot(t, school)

			row := f.catalogRow(t, school)
			if row.Source != "both" || row.SchoolStatus != "pending_configuration" || row.Activation != "ready" || !row.CanEnable ||
				row.PriceState != "confirmed" || row.ProbeState != "passed" || len(row.PreferredRoutes) != len(firstConnectAdapters) {
				t.Fatalf("inactive slots should allow first connect: %+v", row)
			}
			for _, route := range row.PreferredRoutes {
				if route.CredentialID == nil || *route.CredentialID != f.keys[0] ||
					route.EffectiveMultiplier == nil || route.EffectiveMultiplier.String() != "0.5" {
					t.Fatalf("expected both protocols on the current Gemini Key: %+v", route)
				}
			}

			if mode == "single-name" {
				if id, err := f.service.EnableModel(f.ctx, f.provider, f.actor, firstConnectUpstream, ""); err != nil || id != school {
					t.Fatalf("first connect did not restore the slots: id=%s err=%v", id, err)
				}
			} else {
				result, err := f.service.EnableModelsBatch(f.ctx, f.provider, f.actor, []string{school.String()})
				if err != nil || len(result.Enabled) != 1 || result.Enabled[0].ModelID != school || len(result.Skipped) != 0 {
					t.Fatalf("batch first connect did not restore the slots: %+v, %v", result, err)
				}
			}
			if id, err := f.service.EnableModel(f.ctx, f.provider, f.actor, firstConnectUpstream, school.String()); err != nil || id != school {
				t.Fatalf("repeated enable: id=%s err=%v", id, err)
			}
			f.assertSkipped(t, "already_active", "", "模型已启用", school.String())
			f.assertPublished(t, school, f.keys[0], firstConnectAdapters)
			if after := f.bindingSnapshot(t, school); after.bindingIDs != before.bindingIDs {
				t.Fatalf("restore replaced binding IDs: before=%s after=%s", before.bindingIDs, after.bindingIDs)
			}
			for i, adapter := range firstConnectAdapters {
				var priority int
				err := f.db.QueryRowContext(f.ctx, `
SELECT priority FROM model_bindings WHERE model_id=$1 AND provider_id=$2 AND adapter=$3`, school, f.provider, adapter).Scan(&priority)
				if err != nil || priority != 10+i {
					t.Fatalf("restore changed %s priority: got %d, err=%v", adapter, priority, err)
				}
			}
		})
	}
}

func TestActivationCatalogSourceNeedsKeyEvidencePostgres(t *testing.T) {
	for _, evidence := range []string{"catalog not current", "group denied"} {
		t.Run(evidence, func(t *testing.T) {
			f := newFirstConnectFixture(t)
			school := f.configureProductionBothTarget(t)
			if evidence == "catalog not current" {
				execCatalogTestSQL(t, f.ctx, f.db, `UPDATE beichen_datasets SET last_success=clock_timestamp()-interval '31 minutes'
WHERE credential_id=$1 AND kind='catalog'`, f.keys[0])
			} else {
				execCatalogTestSQL(t, f.ctx, f.db, "UPDATE beichen_website_keys SET enabled=false WHERE provider_id=$1 AND credential_id=$2", f.provider, f.keys[0])
			}
			f.assertEnableRejected(t, school.String(), blockerNoCandidate, noCandidateMessage)
			f.assertSkipped(t, "route_not_ready", blockerNoCandidate, noCandidateMessage, school.String())
			f.assertPending(t, school)
		})
	}
}

// A website directory entry keeps its published bindings without any Key
// backed catalog row. Named and UUID lookups must both report it as active.
func TestEnableWebsiteOnlyPublishedRouteIsAlreadyActivePostgres(t *testing.T) {
	f := newFirstConnectFixture(t)
	oldKey := f.keys[1]
	school := f.pendingSchool(t, "school-website-published", &oldKey)
	f.readyKey(t, oldKey)
	f.bindKey(t, school, oldKey)
	if id, err := f.service.EnableModel(f.ctx, f.provider, f.actor, firstConnectUpstream, school.String()); err != nil || id != school {
		t.Fatalf("initial publication failed: id=%s err=%v", id, err)
	}
	bindingIDs := f.assertPublished(t, school, oldKey, firstConnectAdapters)

	// The directory entry is website-only and older than the Key revision, so
	// loading it yields no candidate while the gateway still serves the
	// published bindings.
	execCatalogTestSQL(t, f.ctx, f.db, `UPDATE provider_model_catalog
SET catalog_source='website', raw_entry='{"source":"beichen_website_group"}', last_seen_at=now()-interval '1 hour'
WHERE credential_id=$1 AND model_id=$2`, oldKey, firstConnectUpstream)
	// No other Key lists the upstream, so the name has no candidate rows.
	execCatalogTestSQL(t, f.ctx, f.db, `UPDATE provider_model_catalog SET status='inactive' WHERE credential_id=$1 AND model_id=$2`, f.keys[0], firstConnectUpstream)
	before := f.bindingSnapshot(t, school)

	if id, err := f.service.EnableModel(f.ctx, f.provider, f.actor, firstConnectUpstream, ""); err != nil || id != school {
		t.Fatalf("website-only published route should stay active: id=%s err=%v", id, err)
	}
	for _, selector := range []string{firstConnectUpstream, school.String()} {
		result, err := f.service.EnableModelsBatch(f.ctx, f.provider, f.actor, []string{selector})
		if err != nil || len(result.Enabled) != 0 || len(result.Skipped) != 1 ||
			result.Skipped[0].Model != selector || result.Skipped[0].Reason != "already_active" {
			t.Fatalf("batch selector %q: %+v, %v", selector, result, err)
		}
	}
	if after := f.bindingSnapshot(t, school); !before.equal(after) {
		t.Fatalf("idempotent enable changed the published model: before=%+v after=%+v", before, after)
	}
	if after := f.assertPublished(t, school, oldKey, firstConnectAdapters); after != bindingIDs {
		t.Fatalf("idempotent enable replaced bindings: before=%s after=%s", bindingIDs, after)
	}
}

// A website directory route that a current Key can already replace is not
// "already active": the name-scoped plan cannot write, so it must report the
// blocker without reserving the school, and the UUID request repairs it.
func TestEnableModelsBatchRepairsRepairableWebsiteRoutePostgres(t *testing.T) {
	f := newFirstConnectFixture(t)
	oldKey := f.keys[1]
	schoolName := "school-website-repairable"
	school := f.pendingSchool(t, schoolName, &oldKey)
	f.readyKey(t, oldKey)
	f.bindKey(t, school, oldKey)
	if id, err := f.service.EnableModel(f.ctx, f.provider, f.actor, firstConnectUpstream, school.String()); err != nil || id != school {
		t.Fatalf("initial publication failed: id=%s err=%v", id, err)
	}
	bindingIDs := f.assertPublished(t, school, oldKey, firstConnectAdapters)

	// The published entry becomes a legacy website row while the other Key
	// still lists the upstream, so a UUID request can repair both bindings.
	execCatalogTestSQL(t, f.ctx, f.db, `UPDATE provider_model_catalog
SET catalog_source='website', raw_entry='{"source":"beichen_website_group"}', last_seen_at=now()-interval '1 hour'
WHERE credential_id=$1 AND model_id=$2`, oldKey, firstConnectUpstream)
	execCatalogTestSQL(t, f.ctx, f.db, `UPDATE provider_credentials SET updated_at=clock_timestamp() WHERE id=$1`, oldKey)

	const staleAlias = "gemini-legacy-alias"
	execCatalogTestSQL(t, f.ctx, f.db, `INSERT INTO provider_model_catalog
(id,provider_id,credential_id,model_id,last_seen_at,synced_at,created_at,updated_at)
VALUES(gen_random_uuid(),$1,$2,$3,now()-interval '1 hour',now(),now(),now())`, f.provider, oldKey, staleAlias)
	if err := f.service.PutModelAlias(f.ctx, f.provider, f.actor, &oldKey, staleAlias, &schoolName); err != nil {
		t.Fatal(err)
	}
	before := f.bindingSnapshot(t, school)

	id, err := f.service.EnableModel(f.ctx, f.provider, f.actor, staleAlias, school.String())
	if id != uuid.Nil || domain.ErrorCode(err) != domain.CodeValidation || activationErrorCode(err) != blockerNoCandidate {
		t.Fatalf("repairable website route must not claim already active: id=%s err=%v", id, err)
	}
	if after := f.bindingSnapshot(t, school); !before.equal(after) {
		t.Fatalf("blocked single enable wrote bindings: before=%+v after=%+v", before, after)
	}

	result, err := f.service.EnableModelsBatch(f.ctx, f.provider, f.actor, []string{staleAlias, school.String()})
	if err != nil || len(result.Enabled) != 1 || len(result.Skipped) != 1 ||
		result.Enabled[0].Model != school.String() || result.Enabled[0].ModelID != school ||
		result.Skipped[0].Model != staleAlias || result.Skipped[0].Reason != "route_not_ready" ||
		result.Skipped[0].Code != blockerNoCandidate {
		t.Fatalf("repairable website route must not occupy the school slot: %+v, %v", result, err)
	}
	if after := f.assertPublished(t, school, f.keys[0], firstConnectAdapters); after != bindingIDs {
		t.Fatalf("repair replaced binding IDs: before=%s after=%s", bindingIDs, after)
	}
	for i, adapter := range firstConnectAdapters {
		var priority int
		err := f.db.QueryRowContext(f.ctx, `SELECT priority FROM model_bindings WHERE model_id=$1 AND provider_id=$2 AND adapter=$3`, school, f.provider, adapter).Scan(&priority)
		if err != nil || priority != 10+i {
			t.Fatalf("repair changed %s priority: got %d, err=%v", adapter, priority, err)
		}
	}
	f.assertSkipped(t, "already_active", "", "模型已启用", school.String())
}

func TestActivationCatalogSourceWebsiteOnlyNeedsBindingPostgres(t *testing.T) {
	f := newFirstConnectFixture(t)
	school := f.unmappedSchool(t)
	key := f.keys[0]
	countCandidate := func(source string) int {
		t.Helper()
		execCatalogTestSQL(t, f.ctx, f.db, "UPDATE provider_model_catalog SET catalog_source=$2 WHERE credential_id=$1 AND model_id=$3",
			key, source, firstConnectUpstream)
		tx, err := f.db.BeginTx(f.ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		models, err := loadActivationCatalog(f.ctx, tx, f.provider, firstConnectUpstream)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, model := range models {
			for _, adapter := range firstConnectAdapters {
				for _, candidate := range model.defaults[adapter] {
					if candidate.route.CredentialID != nil && *candidate.route.CredentialID == key {
						count++
					}
				}
			}
		}
		return count
	}
	if got := countCandidate("key"); got != len(firstConnectAdapters) {
		t.Fatalf("key-only candidate count = %d", got)
	}
	if got := countCandidate("both"); got != len(firstConnectAdapters) {
		t.Fatalf("both candidate count = %d", got)
	}
	if got := countCandidate("website"); got != 0 {
		t.Fatalf("website-only entry without a binding supplied %d candidates", got)
	}
	f.assertPending(t, school)
}
