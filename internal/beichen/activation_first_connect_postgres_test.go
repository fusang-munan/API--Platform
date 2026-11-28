package beichen

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

const firstConnectUpstream = "gemini-4-argon"

var firstConnectAdapters = []string{"openai_responses", "google_gemini_v1beta"}

type firstConnectFixture struct {
	ctx             context.Context
	db              *sql.DB
	service         *Service
	provider, actor uuid.UUID
	keys            []uuid.UUID
}

func newFirstConnectFixture(t *testing.T) firstConnectFixture {
	t.Helper()
	ctx, db, provider, actor, keys := newActionsPostgres(t)
	execCatalogTestSQL(t, ctx, db, "UPDATE provider_credentials SET credential_ciphertext=uuid_send(id)")
	execCatalogTestSQL(t, ctx, db, "INSERT INTO beichen_website_key_sync(provider_id,status,last_seen) VALUES($1,'current',clock_timestamp())", provider)
	for i, key := range keys {
		upstreamKeyID := 12098 + i
		execCatalogTestSQL(t, ctx, db, `INSERT INTO beichen_website_keys
 (provider_id,upstream_key_id,group_id,name,status,enabled,credential_id,ciphertext_digest,match_status)
SELECT provider_id,$2,5,label::text,'active',true,id,sha256(credential_ciphertext),'matched'
FROM provider_credentials WHERE id=$1`, key, upstreamKeyID)
		execCatalogTestSQL(t, ctx, db, `INSERT INTO beichen_provision_channels(credential_id,channel,key_fingerprint,discover_ids,upstream_user_id,checked_at)
SELECT id,label::text,sha256(credential_ciphertext),true,100,clock_timestamp()
FROM provider_credentials WHERE id=$1`, key)
		execCatalogTestSQL(t, ctx, db, `INSERT INTO beichen_panel_access(credential_id,group_id,upstream_key_id,token_ciphertext,updated_by,updated_at)
SELECT id,5,$2,uuid_send(id),$3,updated_at FROM provider_credentials WHERE id=$1`, key, upstreamKeyID, actor)
	}

	f := firstConnectFixture{ctx: ctx, db: db, service: New(db, nil), provider: provider, actor: actor, keys: keys}
	f.readyKey(t, keys[0])
	return f
}

func (f firstConnectFixture) readyKey(t *testing.T, key uuid.UUID) {
	t.Helper()
	saveCatalogFixture(t, f.ctx, f.db, f.provider, key, []catalog.ProviderEntry{
		{ID: firstConnectUpstream, SupportedProtocols: firstConnectAdapters},
	}, nil)
	for _, adapter := range firstConnectAdapters {
		execCatalogTestSQL(t, f.ctx, f.db, `INSERT INTO upstream_probes(provider_id,credential_id,model,protocol,status_code,duration_ms,checked_at,last_passed_at)
VALUES($1,$2,$3,$4,200,1,clock_timestamp(),clock_timestamp())`, f.provider, key, firstConnectUpstream, adapter)
	}
	var revision time.Time
	if err := f.db.QueryRowContext(f.ctx, "SELECT updated_at FROM provider_credentials WHERE id=$1", key).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	rate, zero, peak := int64(2_000_000), int64(0), false
	now := time.Now()
	err = saveBillingObservation(f.ctx, tx, key, revision, billingInfo{
		Multiplier: "0.6", GroupRate: "0.6", PeakEnabled: &peak, ObservedAt: now,
	}, nil, 5, panelQuote{
		expiresAt: now.Add(time.Hour),
		rows: []priceRow{{Model: firstConnectUpstream, Rates: Rates{
			Input: &rate, Output: &rate, CacheRead: &zero, CacheWrite: &zero,
		}}},
	})
	if err == nil {
		err = recordSuccess(f.ctx, tx, key, "billing")
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		t.Fatal(err)
	}
}

func (f firstConnectFixture) pendingSchool(t *testing.T, schoolName string, historicalKey *uuid.UUID) uuid.UUID {
	t.Helper()
	schoolID := uuid.New()
	execCatalogTestSQL(t, f.ctx, f.db, `INSERT INTO models(id,model_id,display_name,status,created_at,updated_at)
VALUES($1,$2,$3,'pending_configuration',now(),now())`, schoolID, schoolName, firstConnectUpstream)
	if historicalKey != nil {
		saveCatalogFixture(t, f.ctx, f.db, f.provider, *historicalKey, []catalog.ProviderEntry{
			{ID: firstConnectUpstream, SupportedProtocols: firstConnectAdapters},
		}, nil)
		execCatalogTestSQL(t, f.ctx, f.db, `UPDATE provider_model_catalog SET last_seen_at=now()-interval '1 hour'
WHERE credential_id=$1 AND model_id=$2`, *historicalKey, firstConnectUpstream)
		if err := f.service.PutModelAlias(f.ctx, f.provider, f.actor, historicalKey, firstConnectUpstream, &schoolName); err != nil {
			t.Fatal(err)
		}
	}
	return schoolID
}

func (f firstConnectFixture) unmappedSchool(t *testing.T) uuid.UUID {
	t.Helper()
	return f.pendingSchool(t, "school-gemini-4-argon", &f.keys[1])
}

func (f firstConnectFixture) assertEvidence(t *testing.T, schoolID, candidateSchoolID uuid.UUID) {
	t.Helper()
	tx, err := f.db.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	keys, _, err := readSwitchCredentials(f.ctx, tx, f.provider)
	if err != nil || len(keys) != 4 {
		t.Fatalf("fixture channels: %d, %v", len(keys), err)
	}
	for _, key := range keys {
		if key.Status != "active" || key.Identity != "ready" {
			t.Fatalf("fixture channel %s: status=%s identity=%s", key.Label, key.Status, key.Identity)
		}
	}
	var matched, passed int
	err = tx.QueryRowContext(f.ctx, `SELECT
 (SELECT count(*) FROM beichen_website_keys WHERE provider_id=$1 AND match_status='matched'),
 (SELECT count(*) FROM upstream_probes WHERE provider_id=$1 AND credential_id=$2 AND model=$3 AND status_code=200)`,
		f.provider, f.keys[0], firstConnectUpstream).Scan(&matched, &passed)
	if err != nil || matched != 4 || passed != len(firstConnectAdapters) {
		t.Fatalf("fixture evidence: matched=%d passed=%d: %v", matched, passed, err)
	}
	var fixed bool
	var terms []byte
	err = tx.QueryRowContext(f.ctx, activationPriceSQL, f.provider, f.keys[0], firstConnectUpstream).Scan(&fixed, &terms)
	if err != nil || !fixed {
		t.Fatalf("fixture needs a confirmed flat quote: %t, %v", fixed, err)
	}
	models, err := loadActivationCatalog(f.ctx, tx, f.provider, firstConnectUpstream)
	if err != nil || len(models) != 1 || len(models[0].schools) != 1 {
		t.Fatalf("fixture needs one upstream model and one school target: %d, %v", len(models), err)
	}
	m := models[0]
	if school := m.schools[schoolID]; school == nil || school.status != "pending_configuration" || len(school.bindings) != 0 {
		t.Fatalf("fixture needs a pending school model without bindings: %+v", school)
	}
	for _, adapter := range firstConnectAdapters {
		candidates := m.defaults[adapter]
		if len(candidates) != 1 || candidates[0].schoolID != candidateSchoolID ||
			candidates[0].route.CredentialID == nil || *candidates[0].route.CredentialID != f.keys[0] {
			t.Fatalf("fixture %s candidates: %+v", adapter, candidates)
		}
	}
}

func (f firstConnectFixture) assertPublished(t *testing.T, school, key uuid.UUID, adapters []string) string {
	t.Helper()
	var status, bindingIDs string
	var multiplier int64
	var bindings, expected, audits int
	err := f.db.QueryRowContext(f.ctx, `SELECT m.status,m.credit_multiplier_milli,count(b.id),
 count(b.id) FILTER(WHERE b.provider_id=$2 AND b.credential_id=$3 AND b.upstream_model_name=$4
   AND b.adapter=ANY($5::text[]) AND b.status='active'),
 COALESCE(string_agg(b.id::text,',' ORDER BY b.id),''),
 (SELECT count(*) FROM model_multiplier_audits a WHERE a.model_id=m.id AND a.actor_user_id=$6)
FROM models m LEFT JOIN model_bindings b ON b.model_id=m.id
WHERE m.id=$1 GROUP BY m.id`, school, f.provider, key, firstConnectUpstream, adapters, f.actor).
		Scan(&status, &multiplier, &bindings, &expected, &bindingIDs, &audits)
	if err != nil || status != "active" || multiplier != 1000 || bindings != len(adapters) || expected != bindings || audits != 1 {
		t.Fatalf("published model: status=%s multiplier=%d bindings=%d matching=%d audits=%d: %v",
			status, multiplier, bindings, expected, audits, err)
	}
	return bindingIDs
}

func (f firstConnectFixture) assertPending(t *testing.T, school uuid.UUID) {
	t.Helper()
	var status string
	var bindings, audits int
	err := f.db.QueryRowContext(f.ctx, `SELECT m.status,
 (SELECT count(*) FROM model_bindings b WHERE b.model_id=m.id AND b.provider_id=$2),
 (SELECT count(*) FROM model_multiplier_audits a WHERE a.model_id=m.id)
FROM models m WHERE m.id=$1`, school, f.provider).Scan(&status, &bindings, &audits)
	if err != nil || status != "pending_configuration" || bindings != 0 || audits != 0 {
		t.Fatalf("blocked model changed: status=%s bindings=%d audits=%d: %v", status, bindings, audits, err)
	}
}

func (f firstConnectFixture) assertSkipped(t *testing.T, reason, code, message string, selectors ...string) {
	t.Helper()
	result, err := f.service.EnableModelsBatch(f.ctx, f.provider, f.actor, selectors)
	if err != nil || len(result.Enabled) != 0 || len(result.Skipped) != len(selectors) {
		t.Fatalf("expected all selectors to be blocked: %+v, %v", result, err)
	}
	for i, skipped := range result.Skipped {
		if skipped.Model != selectors[i] || skipped.Reason != reason || skipped.Code != code || !strings.Contains(skipped.Message, message) {
			t.Fatalf("expected %s/%s with %q for %q, got %+v", reason, code, message, selectors[i], skipped)
		}
	}
}

func (f firstConnectFixture) assertEnableRejected(t *testing.T, selector, code, message string) {
	t.Helper()
	id, err := f.service.EnableModel(f.ctx, f.provider, f.actor, firstConnectUpstream, selector)
	if id != uuid.Nil || domain.ErrorCode(err) != domain.CodeValidation || activationErrorCode(err) != code ||
		!strings.Contains(err.Error(), message) {
		t.Fatalf("selector %q: expected %s containing %q, got id=%s code=%s err=%v",
			selector, code, message, id, activationErrorCode(err), err)
	}
}

func TestEnableModelFirstConnectPostgres(t *testing.T) {
	t.Run("exact school model name", func(t *testing.T) {
		f := newFirstConnectFixture(t)
		school := f.pendingSchool(t, firstConnectUpstream, nil)
		f.assertEvidence(t, school, school)
		id, err := f.service.EnableModel(f.ctx, f.provider, f.actor, firstConnectUpstream, school.String())
		if err != nil || id != school {
			t.Fatalf("exact match: id=%s, err=%v", id, err)
		}
	})
	for _, name := range []string{"single by name", "single by school ID", "batch by name", "batch by school ID"} {
		t.Run(name, func(t *testing.T) {
			f := newFirstConnectFixture(t)
			school := f.unmappedSchool(t)
			f.assertEvidence(t, school, uuid.Nil)
			view, err := f.service.Overview(f.ctx, f.provider, "")
			if err != nil || len(view.ModelCatalog) != 1 {
				t.Fatalf("expected one model in overview, got %d: %v", len(view.ModelCatalog), err)
			}
			row := view.ModelCatalog[0]
			if row.SchoolModelID == nil || *row.SchoolModelID != school || !row.CanEnable || row.Activation != "ready" ||
				row.ProbeState != "passed" || row.PriceState != "confirmed" || len(row.PreferredRoutes) != len(firstConnectAdapters) {
				t.Fatalf("expected first connection ready to publish: %+v", row)
			}
			switch name {
			case "batch by name", "batch by school ID":
				selector := firstConnectUpstream
				if name == "batch by school ID" {
					selector = school.String()
				}
				result, err := f.service.EnableModelsBatch(f.ctx, f.provider, f.actor, []string{selector})
				if err != nil || len(result.Skipped) != 0 || len(result.Enabled) != 1 || result.Enabled[0].ModelID != school {
					t.Fatalf("first enable with %q: %+v, %v", selector, result, err)
				}
			default:
				selector := ""
				if name == "single by school ID" {
					selector = school.String()
				}
				id, err := f.service.EnableModel(f.ctx, f.provider, f.actor, firstConnectUpstream, selector)
				if err != nil || id != school {
					t.Fatalf("first enable with %q: id=%s, err=%v", selector, id, err)
				}
			}
			bindingIDs := f.assertPublished(t, school, f.keys[0], firstConnectAdapters)
			id, err := f.service.EnableModel(f.ctx, f.provider, f.actor, firstConnectUpstream, school.String())
			if err != nil || id != school {
				t.Fatalf("repeated enable: id=%s, err=%v", id, err)
			}
			f.assertSkipped(t, "already_active", "", "模型已启用", school.String())
			if after := f.assertPublished(t, school, f.keys[0], firstConnectAdapters); after != bindingIDs {
				t.Fatal("repeated enable replaced bindings")
			}
		})
	}
}

func TestEnableModelFirstConnectGuardsPostgres(t *testing.T) {
	t.Run("multiple school targets", func(t *testing.T) {
		f := newFirstConnectFixture(t)
		school := f.unmappedSchool(t)
		otherSchool := f.pendingSchool(t, "another-school-gemini", &f.keys[2])
		view, err := f.service.Overview(f.ctx, f.provider, "")
		if err != nil || len(view.ModelCatalog) != 2 {
			t.Fatalf("expected two school rows in overview, got %d: %v", len(view.ModelCatalog), err)
		}
		for _, row := range view.ModelCatalog {
			if row.CanEnable || row.Activation != "blocked" || len(row.PreferredRoutes) != 0 {
				t.Fatalf("scoping the view must not let this row take the shared candidate: %+v", row)
			}
		}
		f.assertEnableRejected(t, "", blockerMultiSchool, multiSchoolMessage)
		f.assertSkipped(t, "route_not_ready", blockerMultiSchool, multiSchoolMessage, firstConnectUpstream)
		for _, target := range []uuid.UUID{school, otherSchool} {
			f.assertEnableRejected(t, target.String(), blockerNoCandidate, "没有与唯一活跃校内目标匹配")
			f.assertSkipped(t, "route_not_ready", blockerNoCandidate, "没有与唯一活跃校内目标匹配", target.String())
			f.assertPending(t, target)
		}
	})
	t.Run("multiple candidate keys", func(t *testing.T) {
		f := newFirstConnectFixture(t)
		schoolName := "school-gemini-4-argon"
		school := f.pendingSchool(t, schoolName, &f.keys[1])
		f.readyKey(t, f.keys[2])
		f.assertEnableRejected(t, school.String(), blockerMultiCandidate, "同一协议有多个候选线路")
		f.assertSkipped(t, "route_not_ready", blockerMultiCandidate, "同一协议有多个候选线路", school.String())

		// Each protocol has one candidate, but the two protocols would pick
		// two different unmapped Keys for the same school model.
		execCatalogTestSQL(t, f.ctx, f.db, `UPDATE upstream_probes SET status_code=401,error_code='http_error'
WHERE model=$1 AND ((credential_id=$2 AND protocol=$4) OR (credential_id=$3 AND protocol=$5))`,
			firstConnectUpstream, f.keys[0], f.keys[2], firstConnectAdapters[0], firstConnectAdapters[1])
		f.assertEnableRejected(t, school.String(), blockerMultiCandidate, "首次启用有多个候选上游线路")
		f.assertSkipped(t, "route_not_ready", blockerMultiCandidate, "首次启用有多个候选上游线路", school.String())
		f.assertPending(t, school)

		for _, key := range []uuid.UUID{f.keys[0], f.keys[2]} {
			if err := f.service.PutModelAlias(f.ctx, f.provider, f.actor, &key, firstConnectUpstream, &schoolName); err != nil {
				t.Fatal(err)
			}
		}
		f.assertEnableRejected(t, school.String(), blockerMultiCandidate, "首次启用有多个候选上游线路")
		f.assertSkipped(t, "route_not_ready", blockerMultiCandidate, "首次启用有多个候选上游线路", school.String())
		f.assertPending(t, school)
	})
	t.Run("existing binding takes priority", func(t *testing.T) {
		f := newFirstConnectFixture(t)
		school := f.pendingSchool(t, "school-gemini-4-argon", nil)
		f.readyKey(t, f.keys[1])
		binding := uuid.New()
		execCatalogTestSQL(t, f.ctx, f.db, `INSERT INTO model_bindings
 (id,model_id,provider_id,credential_id,upstream_model_name,adapter,priority,created_at,updated_at)
VALUES($1,$2,$3,$4,$5,'openai_responses',13,now(),now())`, binding, school, f.provider, f.keys[1], firstConnectUpstream)
		id, err := f.service.EnableModel(f.ctx, f.provider, f.actor, firstConnectUpstream, school.String())
		if err != nil || id != school {
			t.Fatalf("enable with existing binding: id=%s, err=%v", id, err)
		}
		if got := f.assertPublished(t, school, f.keys[1], []string{"openai_responses"}); got != binding.String() {
			t.Fatal("enable replaced the teacher's existing binding")
		}
		var priority int
		if err := f.db.QueryRowContext(f.ctx, "SELECT priority FROM model_bindings WHERE id=$1", binding).Scan(&priority); err != nil || priority != 13 {
			t.Fatalf("enable changed existing priority to %d: %v", priority, err)
		}
	})
	t.Run("stale website entry supplies no mapping", func(t *testing.T) {
		f := newFirstConnectFixture(t)
		school := f.unmappedSchool(t)
		execCatalogTestSQL(t, f.ctx, f.db, "UPDATE provider_model_catalog SET catalog_source='website' WHERE credential_id=$1", f.keys[1])
		f.assertSkipped(t, "route_not_ready", blockerNoCandidate, "没有与唯一活跃校内目标匹配", school.String())
		f.assertPending(t, school)
	})
	t.Run("missing price", func(t *testing.T) {
		f := newFirstConnectFixture(t)
		school := f.unmappedSchool(t)
		execCatalogTestSQL(t, f.ctx, f.db, "DELETE FROM beichen_price_versions WHERE credential_id=$1 AND model=$2", f.keys[0], firstConnectUpstream)
		f.assertEnableRejected(t, school.String(), blockerPriceMissing, "缺少可用报价")
		f.assertSkipped(t, "no_price", blockerPriceMissing, "缺少可用报价", school.String())
		f.assertPending(t, school)
	})
	t.Run("401 and 403 probes", func(t *testing.T) {
		f := newFirstConnectFixture(t)
		school := f.unmappedSchool(t)
		execCatalogTestSQL(t, f.ctx, f.db, `UPDATE upstream_probes
SET status_code=CASE WHEN protocol='openai_responses' THEN 401 ELSE 403 END,error_code='http_error',checked_at=clock_timestamp()
WHERE provider_id=$1 AND credential_id=$2 AND model=$3`, f.provider, f.keys[0], firstConnectUpstream)
		f.assertEnableRejected(t, school.String(), blockerNoCandidate, "没有与唯一活跃校内目标匹配")
		f.assertSkipped(t, "route_not_ready", blockerNoCandidate, "没有与唯一活跃校内目标匹配", school.String())
		f.assertPending(t, school)
	})
	t.Run("active foreign route without school multiplier", func(t *testing.T) {
		f := newFirstConnectFixture(t)
		school := f.unmappedSchool(t)
		foreign, binding := uuid.New(), uuid.New()
		execCatalogTestSQL(t, f.ctx, f.db, `INSERT INTO providers(id,name,base_url,created_at,updated_at)
VALUES($1,'Other','https://other.invalid',now(),now())`, foreign)
		execCatalogTestSQL(t, f.ctx, f.db, `INSERT INTO model_bindings
 (id,model_id,provider_id,upstream_model_name,adapter,created_at,updated_at)
VALUES($1,$2,$3,$4,'openai_responses',now(),now())`, binding, school, foreign, firstConnectUpstream)
		f.assertEnableRejected(t, school.String(), blockerPriceMissing, "缺少可计算报价")
		f.assertSkipped(t, "route_not_ready", blockerPriceMissing, "缺少可计算报价", school.String())
		f.assertPending(t, school)
	})
}
