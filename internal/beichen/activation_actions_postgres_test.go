package beichen

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

func newActionsPostgres(t *testing.T) (context.Context, *sql.DB, uuid.UUID, uuid.UUID, []uuid.UUID) {
	t.Helper()
	dsn := os.Getenv("NEBULA_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("需要隔离的本机 nebula_test PostgreSQL")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil || cfg.Host != "127.0.0.1" || !isIsolatedTestDatabase(cfg.Database) || cfg.TLSConfig != nil || len(cfg.Fallbacks) != 0 {
		t.Fatal("仅允许连接 sslmode=disable 的本机 nebula_test 测试数据库")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	t.Cleanup(cancel)
	db := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { db.Close() })
	schema := pgx.Identifier{"beichen_actions_" + strings.ReplaceAll(uuid.NewString(), "-", "")}.Sanitize()
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	cfg.RuntimeParams["search_path"] = schema + ",public"
	isolated := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { isolated.Close() })
	paths, err := filepath.Glob("../infrastructure/db/migrations/*.sql")
	if err != nil || len(paths) == 0 {
		t.Fatal("未找到迁移文件")
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := isolated.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range paths {
		migration, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		exec(string(migration))
	}
	var provider uuid.UUID
	if err := isolated.QueryRowContext(ctx, "SELECT id FROM providers WHERE base_url=$1", origin).Scan(&provider); err != nil {
		t.Fatal(err)
	}
	actor := uuid.New()
	exec(`INSERT INTO users(id,name,email,password_hash,role,created_at,updated_at)
VALUES($1,'测试教师','actions@example.test','fixture','teacher',now(),now())`, actor)
	keys := []uuid.UUID{
		uuid.MustParse("00000000-0000-4000-8000-000000000004"),
		uuid.MustParse("00000000-0000-4000-8000-000000000003"),
		uuid.MustParse("00000000-0000-4000-8000-000000000002"),
		uuid.MustParse("00000000-0000-4000-8000-000000000001"),
	}
	for i, key := range keys {
		exec(`INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at)
VALUES($1,$2,$3,'fixture',now(),now())`, key, provider, []string{"Gemini", "GM1", "G3", "CC1"}[i])
	}
	return ctx, isolated, provider, actor, keys
}

func TestEnableModelsBatchPostgres(t *testing.T) {
	ctx, db, provider, actor, keys := newActionsPostgres(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"already", "unpriced", "mixed-price", "flat", "manual-only", "fail-enable", "fresh"} {
		exec(`INSERT INTO provider_model_catalog(id,provider_id,credential_id,model_id,last_seen_at,synced_at,created_at,updated_at)
VALUES(gen_random_uuid(),$1,$2,$3,now(),now(),now(),now())`, provider, keys[0], name)
	}
	exec(`INSERT INTO provider_model_catalog(id,provider_id,credential_id,model_id,last_seen_at,synced_at,created_at,updated_at)
VALUES(gen_random_uuid(),$1,$2,'blocked',now(),now(),now(),now()),
(gen_random_uuid(),$1,$3,'mixed-price',now(),now(),now(),now())`, provider, keys[3], keys[1])
	exec("UPDATE provider_credentials SET status='inactive' WHERE id=$1", keys[3])
	exec(`INSERT INTO beichen_datasets(credential_id,kind,last_success)
VALUES($1,'catalog',clock_timestamp()),($2,'catalog',clock_timestamp())`, keys[0], keys[1])
	for _, name := range []string{"flat", "mixed-price", "fail-enable", "fresh"} {
		exec(`INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,input_rate,output_rate,cache_read_rate,cache_write_rate)
VALUES($1,$2,'upstream','effective','billing',0,2000000,0,0)`, keys[0], name)
	}
	exec(`INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,actor_id,input_rate,output_rate,cache_read_rate,cache_write_rate)
VALUES($1,'manual-only','school','manual','teacher',$2,1000000,1000000,0,0)`, keys[0], actor)
	exec(`INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
VALUES(gen_random_uuid(),'already','已启用','active',1000,now(),now()),
(gen_random_uuid(),'mixed-price','多线路','inactive',1000,now(),now())`)
	exec(`INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,priority,created_at,updated_at)
SELECT gen_random_uuid(),id,$1,$2,model_id::text,'openai_compatible',0,now(),now() FROM models WHERE model_id IN ('already','mixed-price')`, provider, keys[0])
	exec(`INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,priority,created_at,updated_at)
SELECT gen_random_uuid(),id,$1,$2,'mixed-price','openai_responses',0,now(),now() FROM models WHERE model_id='mixed-price'`, provider, keys[1])
	exec(`CREATE FUNCTION reject_action_fixture() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF NEW.model_id='fail-enable' THEN RAISE EXCEPTION '测试单项失败'; END IF; RETURN NEW; END $$;
CREATE TRIGGER reject_action_fixture BEFORE INSERT ON models FOR EACH ROW EXECUTE FUNCTION reject_action_fixture()`)
	service := New(db, nil)
	result, err := service.EnableModelsBatch(ctx, provider, actor,
		[]string{"absent", "already", "blocked", "unpriced", "mixed-price", "flat", "manual-only", "fail-enable", "fresh", "FLAT"})
	if err != nil || len(result.Enabled) != 3 || len(result.Skipped) != 6 {
		t.Fatalf("批量结果错误: %+v, %v", result, err)
	}
	for i, name := range []string{"flat", "manual-only", "fresh"} {
		if result.Enabled[i].Model != name || result.Enabled[i].ModelID == uuid.Nil {
			t.Fatalf("启用顺序或模型 ID 错误: %+v", result.Enabled)
		}
	}
	for i, reason := range []string{"not_found", "already_active", "route_not_ready", "no_price", "no_price", "enable_failed"} {
		if result.Skipped[i].Reason != reason || result.Skipped[i].Message == "" {
			t.Fatalf("跳过原因错误: %+v", result.Skipped)
		}
	}
	if result.Skipped[3].Message != "北辰线路缺少可用报价，请同步官网价或设置手动价" {
		t.Fatal("缺价提示错误")
	}
	replay, err := service.EnableModelsBatch(ctx, provider, actor, []string{"flat", "manual-only", "fresh"})
	if err != nil || len(replay.Enabled) != 0 || len(replay.Skipped) != 3 {
		t.Fatalf("重复启用不幂等: %+v, %v", replay, err)
	}
	for _, skipped := range replay.Skipped {
		if skipped.Reason != "already_active" {
			t.Fatalf("重复启用不应写入: %+v", skipped)
		}
	}
	var audits, failed, unpriced int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM model_multiplier_audits WHERE actor_user_id=$1`, actor).Scan(&audits); err != nil || audits != 3 {
		t.Fatalf("审计重复或缺失: %d, %v", audits, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM models WHERE model_id='fail-enable'`).Scan(&failed); err != nil || failed != 0 {
		t.Fatalf("失败项未回滚: %d, %v", failed, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM models WHERE model_id='unpriced'`).Scan(&unpriced); err != nil || unpriced != 0 {
		t.Fatalf("无价格项不应创建模型: %d, %v", unpriced, err)
	}
}

func TestSingleAndBatchEnableUseSamePricesPostgres(t *testing.T) {
	ctx, db, provider, actor, keys := newActionsPostgres(t)
	service := New(db, nil)
	execCatalogTestSQL(t, ctx, db, "INSERT INTO beichen_datasets(credential_id,kind,last_success) VALUES($1,'catalog',clock_timestamp())", keys[0])
	const missingOutput = `{"input_price":0,"output_price":1,"intervals":[{"min_tokens":0,"input_price":1}]}`
	for _, tc := range []struct {
		name, conditions, code, messagePart string
		cacheRate                           *int64
		enable, noRoute                     bool
	}{
		{name: "fixed", cacheRate: new(int64), enable: true},
		{name: "fixed-without-cache-prices", code: "missing_price", messagePart: "缺少可用报价"},
		{name: "tiered", enable: true, conditions: `{"billing_mode":"token","intervals":[
			{"min_tokens":0,"max_tokens":100,"input_price":0,"output_price":0.000002},
			{"min_tokens":"100","max_tokens":null,"input_price":"0.000003","output_price":"0.000004"}]}`},
		{name: "default-mode", enable: true, conditions: `{"intervals":[{"min_tokens":0,"input_price":0,"output_price":1}]}`},
		{name: "per-request", conditions: `{"billing_mode":"per_request","per_request_price":1}`,
			code: "billing_mode_non_token", messagePart: "非 Token 计价"},
		{name: "missing-output", conditions: missingOutput, code: "tier_missing_output", messagePart: "第 1 档缺少输出"},
		{name: "no-route", conditions: missingOutput, noRoute: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec := func(q string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(ctx, q, args...); err != nil {
					t.Fatal(err)
				}
			}
			single, batch := tc.name+"-single", tc.name+"-batch"
			key := keys[0]
			if tc.noRoute {
				key = keys[3]
				exec("UPDATE provider_credentials SET status='inactive' WHERE id=$1", key)
			}
			exec(`INSERT INTO provider_model_catalog(id,provider_id,credential_id,model_id,last_seen_at,synced_at,created_at,updated_at)
VALUES(gen_random_uuid(),$1,$2,$3,now(),now(),now(),now()),
      (gen_random_uuid(),$1,$2,$4,now(),now(),now(),now())`, provider, key, single, batch)
			if tc.conditions == "" {
				exec(`INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,input_rate,output_rate,cache_read_rate,cache_write_rate)
VALUES($1,$2,'upstream','effective','billing',0,2000000,$4,$4),
      ($1,$3,'upstream','effective','billing',0,2000000,$4,$4)`, key, single, batch, tc.cacheRate)
			} else {
				exec(`WITH rate AS (
  INSERT INTO beichen_multiplier_versions(credential_id,effective_at,multiplier)
  VALUES($1,statement_timestamp(),1) RETURNING id,effective_at
)
INSERT INTO beichen_panel_snapshots(credential_id,effective_at,credential_revision,group_id,expires_at,
  multiplier_version,model_pricing,credential_ciphertext_sha256)
SELECT c.id,rate.effective_at,c.updated_at,1,rate.effective_at+interval '1 hour',rate.id,
  jsonb_build_object($2::text,$4::jsonb,$3::text,$4::jsonb),sha256(c.credential_ciphertext)
FROM provider_credentials c CROSS JOIN rate WHERE c.id=$1`, key, single, batch, tc.conditions)
			}

			view, err := service.Overview(ctx, provider, "")
			if err != nil {
				t.Fatal(err)
			}
			previewed := 0
			var priceMessage string
			for _, model := range view.ModelCatalog {
				if model.Model != single && model.Model != batch {
					continue
				}
				previewed++
				if model.PriceCovered != tc.enable || (model.Activation == "ready") != tc.enable || model.ProbeState != "unprobed" {
					t.Fatalf("coverage and activation preview disagree: %+v", model)
				}
				if tc.code == "" {
					if model.PriceBlocker != nil {
						t.Fatalf("unexpected price blocker: %+v", model.PriceBlocker)
					}
				} else {
					if model.PriceBlocker == nil || model.PriceBlocker.Code != tc.code || !strings.Contains(model.PriceBlocker.Message, tc.messagePart) {
						t.Fatalf("expected price blocker %q with %q, got %+v", tc.code, tc.messagePart, model.PriceBlocker)
					}
					priceMessage = model.PriceBlocker.Message
				}
				if tc.noRoute && (!model.Retired || model.CanEnable || len(model.Blockers) != 0) {
					t.Fatalf("disabled-only history should leave the current activation preview: %+v", model)
				}
			}
			if previewed != 2 {
				t.Fatal("missing activation preflight")
			}
			id, singleErr := service.EnableModel(ctx, provider, actor, single, "")
			result, err := service.EnableModelsBatch(ctx, provider, actor, []string{batch})
			if err != nil {
				t.Fatal(err)
			}
			if tc.enable {
				wantMessage := ""
				if tc.conditions != "" {
					wantMessage = "按条件价自动结算"
				}
				if singleErr != nil || id == uuid.Nil || len(result.Skipped) != 0 || len(result.Enabled) != 1 ||
					result.Enabled[0].Model != batch || result.Enabled[0].ModelID == uuid.Nil || result.Enabled[0].Message != wantMessage {
					t.Fatalf("两种入口都应启用: single=%s %v batch=%+v", id, singleErr, result)
				}
				var active int
				if err := db.QueryRowContext(ctx, `SELECT count(*) FROM models WHERE status='active' AND id IN ($1,$2)`,
					id, result.Enabled[0].ModelID).Scan(&active); err != nil || active != 2 {
					t.Fatalf("两种入口都应发布模型: active=%d err=%v", active, err)
				}
				exec("UPDATE provider_model_catalog SET status='inactive' WHERE model_id=$1", single)
				view, err := service.Overview(ctx, provider, "")
				if err != nil {
					t.Fatal(err)
				}
				for _, model := range view.ModelCatalog {
					if model.Model == single && model.CatalogState == "published_unavailable" && model.SchoolStatus == "active" &&
						model.KeyCount == 0 && model.ReadyCount == 0 && !model.CanEnable &&
						model.BindingIssue != nil && model.BindingIssue.Code == "catalog_missing" {
						return
					}
				}
				t.Fatal("a published model must stay visible after it leaves all catalogues")
			}
			if id != uuid.Nil || domain.ErrorCode(singleErr) != domain.CodeValidation || len(result.Enabled) != 0 || len(result.Skipped) != 1 {
				t.Fatalf("两种入口都应拒绝: single=%s %v batch=%+v", id, singleErr, result)
			}
			skipped := result.Skipped[0]
			if tc.noRoute {
				if skipped.Reason != "route_not_ready" || !strings.Contains(singleErr.Error(), "路线") {
					t.Fatalf("两种入口都应提示线路原因: single=%v batch=%+v", singleErr, skipped)
				}
			} else if skipped.Reason != "no_price" || skipped.Message != priceMessage || singleErr.Error() != priceMessage {
				t.Fatalf("两种入口都应显示和概览一致的价格原因: overview=%q single=%v batch=%+v", priceMessage, singleErr, skipped)
			}
			var created int
			if err := db.QueryRowContext(ctx, `SELECT count(*) FROM models WHERE model_id IN ($1,$2)`, single, batch).Scan(&created); err != nil || created != 0 {
				t.Fatalf("缺少可用报价时不应创建模型: created=%d err=%v", created, err)
			}
		})
	}
}

func TestEnableRequiresCurrentKeyCatalogPostgres(t *testing.T) {
	ctx, db, provider, actor, keys := newActionsPostgres(t)
	key := keys[0]
	exec := func(query string, args ...any) { execCatalogTestSQL(t, ctx, db, query, args...) }
	exec("UPDATE provider_credentials SET updated_at=now()-interval '1 hour' WHERE id=$1", key)
	newModels := []string{"new-single", "new-batch", "configured"}
	models := append(slices.Clone(newModels), "published")
	exec(`INSERT INTO provider_model_catalog(id,provider_id,credential_id,model_id,supported_protocols,last_seen_at,synced_at,created_at,updated_at)
SELECT gen_random_uuid(),$1,$2,model,ARRAY['openai_compatible'],now(),now(),now(),now() FROM unnest($3::text[]) model`, provider, key, models)
	exec(`INSERT INTO beichen_datasets(credential_id,kind,last_success) VALUES($1,'catalog',now())`, key)
	exec(`INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,actor_id,input_rate,output_rate)
SELECT $1,model,'school','manual','teacher',$2,1000000,2000000 FROM unnest($3::text[]) model`, key, actor, models)
	configured, published := uuid.New(), uuid.New()
	exec(`INSERT INTO models(id,model_id,display_name,status,credit_multiplier_milli,created_at,updated_at)
VALUES($1,'configured','Configured','inactive',1000,now(),now()),($2,'published','Published','active',1000,now(),now())`, configured, published)
	exec(`INSERT INTO model_bindings(id,model_id,provider_id,credential_id,upstream_model_name,adapter,priority,created_at,updated_at)
VALUES(gen_random_uuid(),$1,$3,NULL,'configured','openai_compatible',0,now(),now()),
      (gen_random_uuid(),$2,$3,$4,'published','openai_compatible',0,now(),now())`, configured, published, provider, key)
	service := New(db, nil)
	scanError := "fixture scan failed"
	for _, tc := range []struct {
		name       string
		minutesAgo int
		lastError  *string
	}{
		{name: "expired", minutesAgo: 31},
		{name: "scan failed", lastError: &scanError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec(`UPDATE beichen_datasets SET last_success=now()-make_interval(mins=>$2),last_error=$3
WHERE credential_id=$1 AND kind='catalog'`, key, tc.minutesAgo, tc.lastError)
			view, err := service.Overview(ctx, provider, "")
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range newModels {
				i := slices.IndexFunc(view.ModelCatalog, func(model CatalogModel) bool { return model.Model == name })
				if i < 0 || view.ModelCatalog[i].CanEnable || view.ModelCatalog[i].Activation != "blocked" {
					t.Fatalf("%s should be blocked in the preview: %+v", name, view.ModelCatalog)
				}
				if id, err := service.EnableModel(ctx, provider, actor, name, ""); id != uuid.Nil || domain.ErrorCode(err) != domain.CodeValidation {
					t.Fatalf("%s should be rejected despite its manual price: %s (%v)", name, id, err)
				}
			}
			i := slices.IndexFunc(view.ModelCatalog, func(model CatalogModel) bool { return model.Model == "published" })
			if i < 0 || view.ModelCatalog[i].ReadyCount != 0 || view.ModelCatalog[i].SchoolStatus != "active" ||
				!view.ModelCatalog[i].BoundCatalogPending || view.ModelCatalog[i].ModelAvailability.Availability != "unverified" {
				t.Fatalf("the published binding should wait for current catalog evidence: %+v", view.ModelCatalog)
			}
			if id, err := service.EnableModel(ctx, provider, actor, "published", ""); err != nil || id != published {
				t.Fatalf("the published model should still resolve to %s: %s (%v)", published, id, err)
			}
			result, err := service.EnableModelsBatch(ctx, provider, actor, models)
			if err != nil || len(result.Enabled) != 0 || len(result.Skipped) != 4 {
				t.Fatalf("unexpected batch result: %+v (%v)", result, err)
			}
			for i, reason := range []string{"route_not_ready", "route_not_ready", "route_not_ready", "already_active"} {
				if result.Skipped[i].Model != models[i] || result.Skipped[i].Reason != reason {
					t.Fatalf("unexpected batch skip: %+v", result.Skipped[i])
				}
			}
		})
	}
	var inactive bool
	var created int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT status='inactive' FROM models WHERE id=$1),
  (SELECT count(*) FROM models WHERE model_id IN ('new-single','new-batch'))`, configured).Scan(&inactive, &created); err != nil || !inactive || created != 0 {
		t.Fatalf("rejected actions published models: inactive=%v created=%d (%v)", inactive, created, err)
	}

	exec("UPDATE beichen_datasets SET last_success=now(),last_error=NULL WHERE credential_id=$1 AND kind='catalog'", key)
	// The unpinned binding will try the lower Key ID first, even if its listing predates that Key.
	other := keys[1]
	exec(`INSERT INTO beichen_datasets(credential_id,kind,last_success) VALUES($1,'catalog',now())`, other)
	exec(`INSERT INTO provider_model_catalog(id,provider_id,credential_id,model_id,last_seen_at,synced_at,created_at,updated_at)
VALUES(gen_random_uuid(),$1,$2,'configured',now()-interval '1 hour',now(),now(),now())`, provider, other)
	if _, err := service.EnableModel(ctx, provider, actor, "configured", ""); domain.ErrorCode(err) != domain.CodeValidation {
		t.Fatalf("another Key's current listing must not authorize the unpinned binding: %v", err)
	}
	exec("UPDATE provider_model_catalog SET status='inactive' WHERE credential_id=$1", other)
	if id, err := service.EnableModel(ctx, provider, actor, "new-single", ""); err != nil || id == uuid.Nil {
		t.Fatalf("current catalog should allow single enable: %s (%v)", id, err)
	}
	result, err := service.EnableModelsBatch(ctx, provider, actor, []string{"new-batch", "configured"})
	if err != nil || len(result.Skipped) != 0 || len(result.Enabled) != 2 || result.Enabled[1].ModelID != configured {
		t.Fatalf("current catalog should allow batch enable: %+v (%v)", result, err)
	}
}

func TestActivationRejectsDerivedPricesPostgres(t *testing.T) {
	ctx, db, provider, actor, keys := newActionsPostgres(t)
	key := keys[0]
	exec := func(query string, args ...any) { execCatalogTestSQL(t, ctx, db, query, args...) }
	models := []string{"derived-single", "derived-batch"}
	exec(`INSERT INTO provider_model_catalog(id,provider_id,credential_id,model_id,last_seen_at,synced_at,created_at,updated_at)
SELECT gen_random_uuid(),$1,$2,model,now(),now(),now(),now() FROM unnest($3::text[]) model`, provider, key, models)
	exec(`INSERT INTO beichen_datasets(credential_id,kind,last_success) VALUES($1,'catalog',now())`, key)
	exec(`WITH rate AS (
  INSERT INTO beichen_multiplier_versions(credential_id,effective_at,multiplier) VALUES($1,now(),1)
  RETURNING id,effective_at
), snapshot AS (
  INSERT INTO beichen_panel_snapshots(credential_id,effective_at,credential_revision,group_id,expires_at,multiplier_version,model_pricing)
  SELECT c.id,rate.effective_at,c.updated_at,16,rate.effective_at+interval '1 hour',rate.id,'{}'::jsonb
  FROM provider_credentials c CROSS JOIN rate WHERE c.id=$1
  RETURNING effective_at
)
INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,input_rate,output_rate,cache_read_rate,cache_write_rate,group_id,effective_at,observed_at)
SELECT $1,model,'upstream','base','derived_from_usage',1000000,2000000,0,0,16,effective_at,effective_at-interval '1 minute'
FROM snapshot CROSS JOIN unnest($2::text[]) model`, key, models)
	service := New(db, nil)
	checkPreview := func(wantActivation, wantPriceState string) {
		t.Helper()
		view, err := service.Overview(ctx, provider, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(view.Models) != len(models) || len(view.ModelCatalog) != len(models) {
			t.Fatalf("missing fixture models: %+v", view.ModelCatalog)
		}
		for _, row := range view.Models {
			if row.PriceSource != derivedPriceSource || row.UpstreamRates == nil {
				t.Fatalf("expected the stored derived price to remain visible: %+v", row)
			}
		}
		for _, row := range view.ModelCatalog {
			if row.Activation != wantActivation || row.PriceState != wantPriceState ||
				(wantActivation == "blocked" && (row.PriceBlocker == nil || row.PriceBlocker.Code != "missing_price")) {
				t.Fatalf("unexpected activation preview: %+v", row)
			}
		}
	}
	checkPreview("blocked", "derived")
	if id, err := service.EnableModel(ctx, provider, actor, models[0], ""); id != uuid.Nil || domain.ErrorCode(err) != domain.CodeValidation ||
		!strings.Contains(err.Error(), "缺少可用报价") {
		t.Fatalf("single activation accepted the derived price: %s (%v)", id, err)
	}
	result, err := service.EnableModelsBatch(ctx, provider, actor, models[1:])
	if err != nil || len(result.Enabled) != 0 || len(result.Skipped) != 1 || result.Skipped[0].Reason != "no_price" {
		t.Fatalf("batch activation accepted the derived price: %+v (%v)", result, err)
	}

	exec(`INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,actor_id,input_rate,output_rate)
SELECT $1,model,'school','manual','teacher',$2,1000000,2000000 FROM unnest($3::text[]) model`, key, actor, models)
	checkPreview("ready", "manual")
	if id, err := service.EnableModel(ctx, provider, actor, models[0], ""); err != nil || id == uuid.Nil {
		t.Fatalf("single activation rejected the manual price: %s (%v)", id, err)
	}
	result, err = service.EnableModelsBatch(ctx, provider, actor, models[1:])
	if err != nil || len(result.Enabled) != 1 || len(result.Skipped) != 0 || result.Enabled[0].Message != "" {
		t.Fatalf("batch activation rejected the manual price: %+v (%v)", result, err)
	}
}

func TestSetModelPriceFourCredentialsPostgres(t *testing.T) {
	ctx, db, provider, actor, keys := newActionsPostgres(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range keys {
		for _, model := range []string{"Shared", "rollback"} {
			exec(`INSERT INTO provider_model_catalog(id,provider_id,credential_id,model_id,last_seen_at,synced_at,created_at,updated_at)
VALUES(gen_random_uuid(),$1,$2,$3,now(),now(),now(),now())`, provider, key, model)
		}
	}
	exec("UPDATE provider_credentials SET status='inactive' WHERE id=$1", keys[1])
	service := New(db, nil)
	input, output, zero := int64(1234567), int64(6000000), int64(0)
	manual := PriceInput{Mode: "manual", Rates: &Rates{Input: &input, Output: &output, CacheRead: &zero, CacheWrite: &zero}}
	for range 2 {
		refs, err := service.SetModelPrice(ctx, provider, actor, "shared", manual)
		if err != nil || len(refs) != 4 {
			t.Fatalf("未保存四条渠道: %+v, %v", refs, err)
		}
		for i, ref := range refs {
			if ref.CredentialID != keys[3-i] || ref.Label == "" {
				t.Fatalf("未按 UUID 顺序返回凭据: %+v", refs)
			}
		}
	}
	var rows, times int
	var correct bool
	err := db.QueryRowContext(ctx, `SELECT count(*),count(DISTINCT effective_at),bool_and(model='Shared' AND basis='manual'
AND input_rate=1234567 AND output_rate=6000000 AND cache_read_rate=0 AND cache_write_rate=0 AND actor_id=$1)
FROM beichen_price_versions WHERE track='school'`, actor).Scan(&rows, &times, &correct)
	if err != nil || rows != 4 || times != 1 || !correct {
		t.Fatalf("价格、统一时间或幂等错误: rows=%d times=%d correct=%t, %v", rows, times, correct, err)
	}
	if _, err := service.SetModelPrice(ctx, provider, actor, "Shared", PriceInput{Mode: "follow"}); err != nil {
		t.Fatal(err)
	}
	err = db.QueryRowContext(ctx, `SELECT count(*),count(DISTINCT effective_at),bool_and(input_rate IS NULL AND output_rate IS NULL
AND cache_read_rate IS NULL AND cache_write_rate IS NULL) FROM beichen_price_versions WHERE basis='follow'`).Scan(&rows, &times, &correct)
	if err != nil || rows != 4 || times != 1 || !correct {
		t.Fatalf("跟随模式未清空费率或时间不统一: %d, %d, %t, %v", rows, times, correct, err)
	}
	exec(`CREATE FUNCTION reject_price_fixture() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF NEW.model='rollback' AND NEW.credential_id='00000000-0000-4000-8000-000000000004'::uuid
THEN RAISE EXCEPTION '测试事务回滚'; END IF; RETURN NEW; END $$;
CREATE TRIGGER reject_price_fixture BEFORE INSERT ON beichen_price_versions FOR EACH ROW EXECUTE FUNCTION reject_price_fixture()`)
	if _, err := service.SetModelPrice(ctx, provider, actor, "rollback", manual); err == nil {
		t.Fatal("最后一条渠道失败应使整批失败")
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM beichen_price_versions WHERE model='rollback'`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("部分价格未回滚: %d, %v", rows, err)
	}
	exec("UPDATE provider_model_catalog SET status='inactive' WHERE model_id='rollback'")
	for _, id := range []uuid.UUID{provider, uuid.New()} {
		if _, err := service.SetModelPrice(ctx, id, actor, "rollback", manual); domain.ErrorCode(err) != domain.CodeNotFound {
			t.Fatalf("没有有效目录凭据时应返回 404: %v", err)
		}
	}
}

func TestSetModelPriceCredentialsPostgres(t *testing.T) {
	ctx, db, provider, actor, keys := newActionsPostgres(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	otherProvider, otherKey := uuid.New(), uuid.New()
	exec(`INSERT INTO providers(id,name,base_url,created_at,updated_at) VALUES($1,'Other','https://other.invalid',now(),now())`, otherProvider)
	exec(`INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at)
VALUES($1,$2,'other','fixture',now(),now())`, otherKey, otherProvider)
	exec(`INSERT INTO provider_model_catalog(id,provider_id,credential_id,model_id,last_seen_at,synced_at,created_at,updated_at)
VALUES(gen_random_uuid(),$1,$2,'Shared',now(),now(),now(),now()),
 (gen_random_uuid(),$1,$3,'shared',now(),now(),now(),now()),
 (gen_random_uuid(),$1,$4,'Shared',now(),now(),now(),now()),
 (gen_random_uuid(),$5,$6,'Shared',now(),now(),now(),now())`, provider, keys[3], keys[0], keys[1], otherProvider, otherKey)
	exec("UPDATE provider_credentials SET status='inactive' WHERE id=$1", keys[1])

	service := New(db, nil)
	value := int64(1250000)
	price := PriceInput{Mode: "manual", Rates: &Rates{Input: &value, Output: &value}}
	for _, invalidKey := range []uuid.UUID{keys[1], keys[2], otherKey, uuid.New()} {
		scope := ModelPriceScope{Scope: "credentials", CredentialIDs: []uuid.UUID{keys[3], invalidKey}}
		if refs, err := service.SetModelPrice(ctx, provider, actor, "SHARED", price, scope); domain.ErrorCode(err) != domain.CodeNotFound || len(refs) != 0 {
			t.Fatalf("非可用线路应使整批失败: refs=%v, err=%v", refs, err)
		}
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM beichen_price_versions").Scan(&count); err != nil || count != 0 {
		t.Fatalf("拒绝的范围产生了价格版本: count=%d, err=%v", count, err)
	}

	scope := ModelPriceScope{Scope: "credentials", CredentialIDs: []uuid.UUID{keys[0], keys[3], keys[0]}}
	for range 2 {
		refs, err := service.SetModelPrice(ctx, provider, actor, "SHARED", price, scope)
		if err != nil || len(refs) != 2 || refs[0].CredentialID != keys[3] || refs[1].CredentialID != keys[0] ||
			refs[0].Label != "CC1" || refs[1].Label != "Gemini" {
			t.Fatalf("定价渠道范围错误: refs=%v, err=%v", refs, err)
		}
	}
	var times int
	var correct bool
	err := db.QueryRowContext(ctx, `SELECT count(*),count(DISTINCT effective_at),bool_and(
 ((credential_id=$1 AND model='Shared') OR (credential_id=$2 AND model='shared')) AND
 track='school' AND basis='manual' AND input_rate=1250000 AND output_rate=1250000)
FROM beichen_price_versions`, keys[3], keys[0]).Scan(&count, &times, &correct)
	if err != nil || count != 2 || times != 1 || !correct {
		t.Fatalf("范围、同一生效时间或幂等错误: count=%d, times=%d, correct=%t, err=%v", count, times, correct, err)
	}
}
