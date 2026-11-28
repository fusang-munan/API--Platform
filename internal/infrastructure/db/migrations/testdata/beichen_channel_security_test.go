package beichen

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/starprince1234/Nebula-api/internal/domain"
	store "github.com/starprince1234/Nebula-api/internal/infrastructure/db"
)

type channelSecurityFixture struct {
	ctx               context.Context
	db                *sql.DB
	service           *Service
	provider, teacher uuid.UUID
}

func newChannelSecurityFixture(t *testing.T) channelSecurityFixture {
	t.Helper()
	dsn := os.Getenv("NEBULA_CHANNEL_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("需要单独的本机 nebula_test_* PostgreSQL 测试库")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil || cfg.Host != "127.0.0.1" || cfg.TLSConfig != nil || len(cfg.Fallbacks) != 0 ||
		!strings.HasPrefix(cfg.Database, "nebula_test_") || cfg.Password != "" {
		t.Fatal("只允许无密码、回环地址、sslmode=disable 的独立 nebula_test_* 库")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { admin.Close() })
	schema := pgx.Identifier{"channel_safety_" + uuid.NewString()}.Sanitize()
	execCatalogTestSQL(t, ctx, admin, "CREATE SCHEMA "+schema)
	t.Cleanup(func() { execCatalogTestSQL(t, context.Background(), admin, "DROP SCHEMA "+schema+" CASCADE") })
	cfg.RuntimeParams["search_path"] = schema + ",public"
	db := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(6)
	f := channelSecurityFixture{ctx: ctx, db: db, service: New(db, switchTestManager(t)), teacher: uuid.New()}
	if err := db.QueryRowContext(ctx, "SELECT id FROM providers WHERE base_url=$1", origin).Scan(&f.provider); err != nil {
		t.Fatal(err)
	}
	execCatalogTestSQL(t, ctx, db, `
INSERT INTO users(id,name,email,password_hash,role,status,created_at,updated_at)
VALUES ($1,'测试教师','channel-safety@example.test','!','teacher','active',now(),now())`, f.teacher)
	return f
}

func (f channelSecurityFixture) key(t *testing.T, label, secret string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	ciphertext, err := f.service.crypto.EncryptCredential(secret)
	if err != nil {
		t.Fatal(err)
	}
	execCatalogTestSQL(t, f.ctx, f.db, `
INSERT INTO provider_credentials(id,provider_id,label,credential_ciphertext,created_at,updated_at)
VALUES ($1,$2,$3,$4,now(),now())`, id, f.provider, label, ciphertext)
	return id
}

func listChannelSafetyKeys(items ...map[string]any) []byte {
	body, _ := json.Marshal(map[string]any{"code": 0, "data": map[string]any{
		"items": items, "total": len(items), "page": 1, "page_size": 100, "pages": 1,
	}})
	return body
}

func channelSafetyKey(id int64, name, key, status string, group int64) map[string]any {
	item := map[string]any{"id": id, "user_id": 81, "name": name, "key": key, "status": status, "group_id": group}
	if group != 0 {
		item["group"] = map[string]any{"id": group, "name": "测试分组", "rate_multiplier": 1}
	}
	return item
}

func (f channelSecurityFixture) website(t *testing.T, responses map[string][]byte) {
	t.Helper()
	f.service.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Scheme != "https" || req.URL.Host != "beichenapi.com" || req.URL.Path != "/api/v1/keys" {
			t.Error("请求发往了非预期官网接口")
			return nil, errors.New("unexpected website endpoint")
		}
		body, found := responses[strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")]
		if !found {
			return &http.Response{StatusCode: http.StatusUnauthorized, Body: http.NoBody, Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
}

func TestBeichenChannelManualPanelOwnership(t *testing.T) {
	f := newChannelSecurityFixture(t)
	const leftKey, rightKey = "sk-local-left-panel-key", "sk-local-right-panel-key"
	const leftToken, rightToken = "fixture-left-panel-bearer", "fixture-right-panel-bearer"
	left := f.key(t, "左渠道", leftKey)
	right := f.key(t, "右渠道", rightKey)
	const group = 23
	keyID, otherID := int64(701), int64(702)
	f.website(t, map[string][]byte{
		leftToken: listChannelSafetyKeys(
			channelSafetyKey(keyID, "左渠道", leftKey, "active", group),
			channelSafetyKey(otherID, "无关", "sk-local-other-panel-key", "active", group)),
		rightToken: listChannelSafetyKeys(channelSafetyKey(keyID, "右渠道", rightKey, "active", group)),
	})
	wrongID := PanelAccessInput{Enabled: true, GroupID: group, Token: leftToken, UpstreamKeyID: &otherID}
	wrongGroup := PanelAccessInput{Enabled: true, GroupID: group + 1, Token: leftToken, UpstreamKeyID: &keyID}
	for _, input := range []PanelAccessInput{wrongID, wrongGroup, {Enabled: true, GroupID: group, Token: "fixture-unknown-bearer", UpstreamKeyID: &keyID}} {
		err := f.service.SetPanelAccess(f.ctx, f.provider, left, f.teacher, input)
		if err == nil || !strings.Contains(err.Error(), "请使用自动发现") {
			t.Fatal("无法核对归属的手工 Key ID 未被拒绝")
		}
	}
	if got, err := f.service.PanelStatus(f.ctx, f.provider, left); err != nil || got.Configured {
		t.Fatal("核对失败后留下了面板授权")
	}

	type result struct {
		id  uuid.UUID
		err error
	}
	claims := make(chan result, 2)
	for id, token := range map[uuid.UUID]string{left: leftToken, right: rightToken} {
		go func() {
			err := f.service.SetPanelAccess(f.ctx, f.provider, id, f.teacher,
				PanelAccessInput{Enabled: true, GroupID: group, Token: token, UpstreamKeyID: &keyID})
			claims <- result{id: id, err: err}
		}()
	}
	var accepted, rejected uuid.UUID
	for range 2 {
		claim := <-claims
		if claim.err == nil {
			accepted = claim.id
			continue
		}
		var conflict *domain.Error
		if !errors.As(claim.err, &conflict) || conflict.Code != domain.CodeNameConflict {
			t.Fatalf("重复认领未返回明确的冲突：%v", claim.err)
		}
		rejected = claim.id
	}
	if accepted == uuid.Nil || rejected == uuid.Nil {
		t.Fatal("同时认领同一官网 Key 必须恰好成功一次")
	}
	var owner uuid.UUID
	if err := f.db.QueryRowContext(f.ctx, `
SELECT credential_id FROM beichen_receipt_active_bindings WHERE upstream_key_id=$1`, keyID).
		Scan(&owner); err != nil || owner != accepted {
		t.Fatalf("逐笔回执绑定未保留已核实的唯一认领：%v", err)
	}
	_, err := f.db.ExecContext(f.ctx, `
INSERT INTO beichen_panel_access(credential_id,updated_by,updated_at,group_id,upstream_key_id,token_ciphertext)
SELECT id,$2,updated_at,$3,$4,'fixture' FROM provider_credentials WHERE id=$1`,
		rejected, f.teacher, group, keyID)
	var constraint *pgconn.PgError
	if !errors.As(err, &constraint) || constraint.ConstraintName != "beichen_panel_access_upstream_key_owner" {
		t.Fatalf("直接写数据库绕过了 Key ID 冲突检查：%v", err)
	}
}

func TestBeichenChannelWebsiteEvidence(t *testing.T) {
	f := newChannelSecurityFixture(t)
	const exact, named, other = "sk-alpha-full-1234", "sk-comp-first-6789", "sk-comp-second-6789"
	const disabled, noGroup = "sk-sleep-full-0000", "sk-empty-full-1111"
	exactID := f.key(t, "完整", exact)
	namedID := f.key(t, "名称一致", named)
	f.key(t, "另一路", other)
	disabledID := f.key(t, "已停用", disabled)
	f.key(t, "分组不存在", noGroup)
	f.key(t, "同强度甲", "sk-tied-first-7654")
	f.key(t, "同强度乙", "sk-tied-second-7654")
	const token = "fixture-website-evidence-panel"
	f.service.ConfigurePanelFallback(token)
	f.website(t, map[string][]byte{token: listChannelSafetyKeys(
		channelSafetyKey(1, "完整", exact, "active", 11),
		channelSafetyKey(2, "完整", "sk-alpha****1234", "active", 12),
		channelSafetyKey(3, "名称一致", "sk-comp****6789", "active", 13),
		channelSafetyKey(4, "已停用", disabled, "inactive", 14),
		channelSafetyKey(5, "已停用", "sk-sleep****0000", "active", 15),
		channelSafetyKey(6, "分组不存在", noGroup, "active", 0),
		channelSafetyKey(7, "无名称证据", "sk-tied****7654", "active", 17),
	)})
	report, err := f.service.RefreshWebsiteKeys(f.ctx, f.provider)
	if err != nil || report.Status != "current" || len(report.Keys) != 7 {
		t.Fatalf("未生成完整的官网清单：%s, %v", report.Status, err)
	}
	want := map[int64]struct {
		status string
		id     uuid.UUID
	}{
		1: {"matched", exactID},
		2: {"unmatched", uuid.Nil},
		3: {"matched", namedID},
		4: {"unmatched", uuid.Nil},
		5: {"unmatched", uuid.Nil},
		6: {"unmatched", uuid.Nil},
		7: {"ambiguous", uuid.Nil},
	}
	for _, key := range report.Keys {
		expected := want[key.ID]
		id := uuid.Nil
		if key.CredentialID != nil {
			id = *key.CredentialID
		}
		if key.MatchStatus != expected.status || id != expected.id {
			t.Errorf("官网编号 %d：匹配状态 %q，预期 %q；归属一致=%v", key.ID, key.MatchStatus, expected.status, id == expected.id)
		}
	}
	execCatalogTestSQL(t, f.ctx, f.db, `
UPDATE beichen_website_keys k SET match_status='matched',credential_id=c.id,ciphertext_digest=sha256(c.credential_ciphertext)
FROM provider_credentials c WHERE k.provider_id=$1 AND k.upstream_key_id=4 AND c.id=$2`, f.provider, disabledID)
	cached, err := f.service.WebsiteKeys(f.ctx, f.provider)
	if err != nil || len(cached.Keys) != 7 || cached.Keys[3].ID != 4 ||
		cached.Keys[3].MatchStatus != "unmatched" || cached.Keys[3].CredentialID != nil {
		t.Fatal("读取旧清单时恢复了已停用 Key 的错误归属")
	}
}
