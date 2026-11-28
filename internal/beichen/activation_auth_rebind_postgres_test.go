package beichen

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

type activationBindingSnapshot struct {
	status     string
	bindings   string
	bindingIDs string
	audits     int
	multiplier *int64
}

func (f firstConnectFixture) bindKey(t *testing.T, school, key uuid.UUID) {
	t.Helper()
	for i, adapter := range firstConnectAdapters {
		execCatalogTestSQL(t, f.ctx, f.db, `INSERT INTO model_bindings
 (id,model_id,provider_id,credential_id,upstream_model_name,adapter,priority,created_at,updated_at)
VALUES(gen_random_uuid(),$1,$2,$3,$4,$5,$6,now(),now())`,
			school, f.provider, key, firstConnectUpstream, adapter, 10+i)
	}
}

func (f firstConnectFixture) authFailedBindings(t *testing.T, schoolName string, statuses ...int) (uuid.UUID, activationBindingSnapshot) {
	t.Helper()
	school := f.pendingSchool(t, schoolName, &f.keys[1])
	f.bindKey(t, school, f.keys[1])
	for i, adapter := range firstConnectAdapters {
		execCatalogTestSQL(t, f.ctx, f.db, `INSERT INTO upstream_probes
 (provider_id,credential_id,model,protocol,status_code,error_code,duration_ms,checked_at)
VALUES($1,$2,$3,$4,$5,'http_error',1,clock_timestamp())`,
			f.provider, f.keys[1], firstConnectUpstream, adapter, statuses[i%len(statuses)])
	}
	return school, f.bindingSnapshot(t, school)
}

func (f firstConnectFixture) catalogAuthFailure(t *testing.T, key uuid.UUID) {
	t.Helper()
	execCatalogTestSQL(t, f.ctx, f.db, `INSERT INTO maintenance_state(id,name,last_error,created_at,updated_at)
SELECT gen_random_uuid(),'beichen_catalog_auth/'||id::text,
  jsonb_build_object('credential_revision',updated_at)::text,now(),now()
FROM provider_credentials WHERE id=$1`, key)
}

func (f firstConnectFixture) bindingSnapshot(t *testing.T, school uuid.UUID) activationBindingSnapshot {
	t.Helper()
	var snapshot activationBindingSnapshot
	var multiplier *int64
	err := f.db.QueryRowContext(f.ctx, `SELECT m.status,
 COALESCE((SELECT string_agg(b.id::text||':'||COALESCE(b.credential_id::text,''),',' ORDER BY b.adapter)
   FROM model_bindings b WHERE b.model_id=m.id AND b.provider_id=$2),''),
 COALESCE((SELECT string_agg(b.id::text,',' ORDER BY b.adapter)
   FROM model_bindings b WHERE b.model_id=m.id AND b.provider_id=$2),''),
 (SELECT count(*) FROM model_multiplier_audits a WHERE a.model_id=m.id),
 m.credit_multiplier_milli
FROM models m WHERE m.id=$1`, school, f.provider).Scan(&snapshot.status, &snapshot.bindings, &snapshot.bindingIDs, &snapshot.audits, &multiplier)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.multiplier = multiplier
	return snapshot
}

func (s activationBindingSnapshot) equal(other activationBindingSnapshot) bool {
	if s.multiplier == nil || other.multiplier == nil {
		return s.multiplier == nil && other.multiplier == nil &&
			s.status == other.status && s.bindings == other.bindings && s.bindingIDs == other.bindingIDs && s.audits == other.audits
	}
	return *s.multiplier == *other.multiplier && s.status == other.status &&
		s.bindings == other.bindings && s.bindingIDs == other.bindingIDs && s.audits == other.audits
}

func TestEnableModelDoesNotAutoRepairAuthFailedActiveRoutePostgres(t *testing.T) {
	for _, statusCode := range []int{401, 403} {
		t.Run(strconv.Itoa(statusCode), func(t *testing.T) {
			f := newFirstConnectFixture(t)
			school, before := f.authFailedBindings(t, "school-auth-"+uuid.NewString()[:8], statusCode)
			id, err := f.service.EnableModel(f.ctx, f.provider, f.actor, firstConnectUpstream, school.String())
			if id != uuid.Nil || domain.ErrorCode(err) != domain.CodeValidation ||
				activationErrorCode(err) != blockerAuthFailed ||
				!strings.Contains(err.Error(), "拒绝认证") || !strings.Contains(err.Error(), "显式重绑") {
				t.Fatalf("auth-failed active route was auto-repaired: id=%s err=%v", id, err)
			}
			if after := f.bindingSnapshot(t, school); !before.equal(after) {
				t.Fatalf("failed enable changed the model or binding: before=%+v after=%+v", before, after)
			}
			result, err := f.service.EnableModelsBatch(f.ctx, f.provider, f.actor, []string{school.String()})
			if err != nil || len(result.Enabled) != 0 || len(result.Skipped) != 1 ||
				result.Skipped[0].Reason != "route_not_ready" || result.Skipped[0].Code != blockerAuthFailed ||
				!strings.Contains(result.Skipped[0].Message, "显式重绑") {
				t.Fatalf("batch did not keep auth failure blocked: %+v %v", result, err)
			}
			if after := f.bindingSnapshot(t, school); !before.equal(after) {
				t.Fatalf("failed batch changed the model or binding: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestEnableModelDoesNotPublishCredentialCatalogAuthFailurePostgres(t *testing.T) {
	f := newFirstConnectFixture(t)
	school := f.pendingSchool(t, "school-catalog-auth", &f.keys[1])
	f.readyKey(t, f.keys[1])
	f.bindKey(t, school, f.keys[1])
	f.catalogAuthFailure(t, f.keys[1])
	before := f.bindingSnapshot(t, school)
	id, err := f.service.EnableModel(f.ctx, f.provider, f.actor, firstConnectUpstream, school.String())
	if id != uuid.Nil || domain.ErrorCode(err) != domain.CodeValidation ||
		activationErrorCode(err) != blockerAuthFailed ||
		!strings.Contains(err.Error(), "拒绝认证") || !strings.Contains(err.Error(), "显式重绑") {
		t.Fatalf("catalog auth failure published the model: id=%s err=%v", id, err)
	}
	if after := f.bindingSnapshot(t, school); !before.equal(after) {
		t.Fatalf("catalog auth failure changed the model or binding: before=%+v after=%+v", before, after)
	}
}

func TestEnableModelPrioritizesAuthFailurePostgres(t *testing.T) {
	for _, name := range []string{"no replacement", "ambiguous replacements"} {
		t.Run(name, func(t *testing.T) {
			f := newFirstConnectFixture(t)
			school, before := f.authFailedBindings(t, "school-mixed-failures", 404, 403)
			if name == "no replacement" {
				execCatalogTestSQL(t, f.ctx, f.db, `UPDATE upstream_probes SET status_code=401,error_code='http_error'
WHERE provider_id=$1 AND credential_id=$2 AND model=$3 AND protocol=$4`,
					f.provider, f.keys[0], firstConnectUpstream, firstConnectAdapters[0])
			} else {
				f.readyKey(t, f.keys[2])
			}

			view, err := f.service.Overview(f.ctx, f.provider, "")
			if err != nil {
				t.Fatal(err)
			}
			if !slices.ContainsFunc(view.ModelCatalog, func(row CatalogModel) bool {
				return row.Model == firstConnectUpstream && row.SchoolModelID != nil && *row.SchoolModelID == school &&
					!row.CanEnable && slices.Contains(row.Blockers, CatalogBlocker{blockerAuthFailed, authFailedRebindMessage})
			}) {
				t.Fatal("overview did not show the auth failure as the activation blocker")
			}
			f.assertEnableRejected(t, school.String(), blockerAuthFailed, authFailedRebindMessage)
			f.assertSkipped(t, "route_not_ready", blockerAuthFailed, authFailedRebindMessage, school.String())
			if after := f.bindingSnapshot(t, school); !before.equal(after) {
				t.Fatalf("blocked enable changed the model or binding: before=%+v after=%+v", before, after)
			}

			execCatalogTestSQL(t, f.ctx, f.db, `UPDATE models SET status='active',credit_multiplier_milli=1000 WHERE id=$1`, school)
			published := f.bindingSnapshot(t, school)
			f.assertEnableRejected(t, school.String(), blockerAuthFailed, authFailedRebindMessage)
			f.assertSkipped(t, "route_not_ready", blockerAuthFailed, authFailedRebindMessage, school.String())
			if after := f.bindingSnapshot(t, school); !published.equal(after) {
				t.Fatalf("blocked enable changed the published model or binding: before=%+v after=%+v", published, after)
			}
		})
	}
}

func TestEnableModelRepairsInactiveOrRetiredAuthFailedRoutePostgres(t *testing.T) {
	for _, retire := range []bool{false, true} {
		name := "inactive"
		if retire {
			name = "retired"
		}
		t.Run(name, func(t *testing.T) {
			f := newFirstConnectFixture(t)
			school, before := f.authFailedBindings(t, "school-repair-"+name, 403)
			if retire {
				execCatalogTestSQL(t, f.ctx, f.db, "UPDATE provider_credentials SET status='inactive',retired_at=clock_timestamp() WHERE id=$1", f.keys[1])
			} else {
				execCatalogTestSQL(t, f.ctx, f.db, "UPDATE provider_credentials SET status='inactive' WHERE id=$1", f.keys[1])
			}
			id, err := f.service.EnableModel(f.ctx, f.provider, f.actor, firstConnectUpstream, school.String())
			if err != nil || id != school {
				t.Fatalf("retired Key was not repaired: id=%s err=%v", id, err)
			}
			after := f.bindingSnapshot(t, school)
			if after.status != "active" || after.audits != 1 || after.bindingIDs != before.bindingIDs ||
				strings.Contains(after.bindings, f.keys[1].String()) || !strings.Contains(after.bindings, f.keys[0].String()) {
				t.Fatalf("retired Key repair did not retain its routes: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestEnableModelKeepsHealthyExistingRoutePostgres(t *testing.T) {
	f := newFirstConnectFixture(t)
	school := f.pendingSchool(t, "school-healthy-existing", &f.keys[1])
	f.readyKey(t, f.keys[1])
	f.bindKey(t, school, f.keys[1])
	before := f.bindingSnapshot(t, school)
	id, err := f.service.EnableModel(f.ctx, f.provider, f.actor, firstConnectUpstream, school.String())
	if err != nil || id != school {
		t.Fatalf("healthy existing route did not enable: id=%s err=%v", id, err)
	}
	after := f.bindingSnapshot(t, school)
	if after.status != "active" || after.audits != 1 || after.bindingIDs != before.bindingIDs ||
		!strings.Contains(after.bindings, f.keys[1].String()) || strings.Contains(after.bindings, f.keys[0].String()) {
		t.Fatalf("healthy existing route was replaced: before=%+v after=%+v", before, after)
	}
}
