package beichen

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
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
	if err != nil || cfg.Host != "127.0.0.1" || cfg.Database != "nebula_test" || cfg.TLSConfig != nil || len(cfg.Fallbacks) != 0 {
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
	for _, name := range []string{"already", "conditional", "mixed-price", "flat", "manual-only", "fail-enable", "fresh"} {
		exec(`INSERT INTO provider_model_catalog(id,provider_id,credential_id,model_id,last_seen_at,synced_at,created_at,updated_at)
VALUES(gen_random_uuid(),$1,$2,$3,now(),now(),now(),now())`, provider, keys[0], name)
	}
	exec(`INSERT INTO provider_model_catalog(id,provider_id,credential_id,model_id,last_seen_at,synced_at,created_at,updated_at)
VALUES(gen_random_uuid(),$1,$2,'blocked',now(),now(),now(),now()),
(gen_random_uuid(),$1,$3,'mixed-price',now(),now(),now(),now())`, provider, keys[3], keys[1])
	exec("UPDATE provider_credentials SET status='inactive' WHERE id=$1", keys[3])
	for _, name := range []string{"flat", "mixed-price", "fail-enable", "fresh"} {
		exec(`INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,input_rate,output_rate)
VALUES($1,$2,'upstream','effective','billing',0,2000000)`, keys[0], name)
	}
	exec(`INSERT INTO beichen_price_versions(credential_id,model,track,basis,source,actor_id,input_rate,output_rate)
VALUES($1,'manual-only','school','manual','teacher',$2,1000000,1000000)`, keys[0], actor)
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
		[]string{"absent", "already", "blocked", "conditional", "mixed-price", "flat", "manual-only", "fail-enable", "fresh", "FLAT"})
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
	if result.Skipped[3].Message != "官网未报价或按条件计价，请先手动定价" {
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
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM models WHERE model_id='conditional'`).Scan(&unpriced); err != nil || unpriced != 0 {
		t.Fatalf("无价格项不应创建模型: %d, %v", unpriced, err)
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
