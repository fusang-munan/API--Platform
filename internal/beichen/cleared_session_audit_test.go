package beichen

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/infrastructure/schoolseal"
)

const (
	reviewCapture   = "fixture-captured-authorization"
	reviewOld       = "fixture-old-authorization"
	reviewInference = "sk-fixture-cleared-review"
)

func configureReviewCapture(t *testing.T, s *Service) {
	t.Helper()
	identity, err := schoolseal.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := identity.SessionStore(schoolseal.Session{
		WebEmail: "capture@review.example", WebAccess: reviewCapture,
		WebRefresh: "fixture-capture-refresh", WebExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ConfigureSavedWebSession(store); err != nil {
		t.Fatal(err)
	}
}

func reviewPanelFixture(t *testing.T, s *Service, cleared *bool, owner *int) *atomic.Int32 {
	t.Helper()
	var wrongSource atomic.Int32
	s.client.Transport = panelTransport(func(r *http.Request) (*http.Response, error) {
		var body string
		if r.URL.Host == "api.beichenapi.com" && r.URL.Path == modelsPath {
			body = `{"data":[{"id":"shared"}]}`
		} else if r.URL.Host == "beichenapi.com" {
			want := reviewOld
			if *cleared {
				want = reviewCapture
			}
			if r.Header.Get("Authorization") != "Bearer "+want {
				wrongSource.Add(1)
				return nil, errors.New("unexpected website authorization source")
			}
			switch r.URL.Path {
			case "/api/v1/auth/logout":
				body = `{"code":0}`
			case "/api/v1/keys":
				body = fmt.Sprintf(`{"code":0,"data":{"items":[{"id":41,"user_id":%d,"group_id":15,"group":{"id":15,"name":"review","rate_multiplier":0.15},"name":"Gemini","key":%q}],"total":1,"page":1,"page_size":100,"pages":1}}`, *owner, reviewInference)
			case "/api/v1/channels/available":
				body = `{"code":0,"data":[{"id":100,"name":"review","status":"active","platforms":[{"platform":"openai","groups":[{"id":15,"platform":"openai","subscription_type":"standard","rate_multiplier":0.15,"peak_rate_enabled":false}],"supported_models":[{"name":"shared","platform":"openai","pricing":{"billing_mode":"token","input_price":0.000001,"output_price":0.000002,"cache_read_price":0,"cache_write_price":0}}]}]}]}`
			case "/api/v1/groups/rates":
				body = `{"code":0,"data":{"15":0.15}}`
			}
		}
		if body == "" {
			return nil, fmt.Errorf("unexpected fixture endpoint %s %s", r.Method, r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	return &wrongSource
}

func TestReviewClearedScheduledJobsPostgres(t *testing.T) {
	ctx, db, provider, actor, keys := newActionsPostgres(t)
	manager := switchTestManager(t)
	s := New(db, manager)
	configureReviewCapture(t, s)
	s.ConfigurePanelFallback(reviewOld)
	cleared, owner := false, 17
	reviewPanelFixture(t, s, &cleared, &owner)
	cred := keys[0]
	cipher, err := manager.EncryptCredential(reviewInference)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO users(id,name,email,password_hash,role,status,created_at,updated_at)
VALUES($1,'fixture','review@admin.example','!','student','disabled',now(),now())`, ProvisionActorID)
	exec("UPDATE provider_credentials SET credential_ciphertext=$2 WHERE id=$1", cred, cipher)
	exec("UPDATE provider_credentials SET status='inactive' WHERE provider_id=$1 AND id<>$2", provider, cred)
	exec("INSERT INTO beichen_provision_channels(credential_id,channel,key_fingerprint,discover_ids) VALUES($1,'Gemini',$2,true)", cred, make([]byte, 32))
	exec("INSERT INTO beichen_panel_access(credential_id,updated_by,updated_at) VALUES($1,$2,now())", cred, ProvisionActorID)

	if _, err := s.LoginPanelAccount(ctx, provider, actor, PanelAccountLogin{Mode: panelModeToken, Token: reviewOld}); err != nil {
		t.Fatal(err)
	}
	grant, err := s.ExportPanelConfiguration(ctx, provider, actor, "https://school.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.discoverChannelKeys(ctx, &cred); err != nil {
		t.Fatal(err)
	}
	view, err := s.ClearPanelAccount(ctx, provider, actor)
	if err != nil || view.Status != panelStateNone || view.Configured || !view.WebSessionConfigured || view.LastError != nil {
		t.Fatalf("清除后的账号状态不符，err=%v", err)
	}
	cleared = true
	if _, err := s.ReadSharedPanelSession(ctx, provider, grant.grant, ""); !errors.Is(err, errLocalGrantRejected) {
		t.Fatal("清除后旧版授权仍可兑换，或返回错误不符")
	}
	if _, err := s.ExportPanelConfiguration(ctx, provider, actor, "https://school.example.test"); err == nil {
		t.Fatal("清除后导出了新的旧版授权")
	}
	if _, err := s.VerifyPanelAccount(ctx, provider); err == nil || err.Error() != errPanelAccountMissing.Error() {
		t.Fatal("验证已清除的教师账号时返回结果不符")
	}

	t.Run("定时逐Key报价", func(t *testing.T) {
		if _, err := s.runJob(ctx, syncJob{provider: provider, credential: cred, kind: "billing"}, true); err != nil {
			var detail string
			_ = db.QueryRowContext(ctx, "SELECT COALESCE(last_error,'') FROM beichen_datasets WHERE credential_id=$1 AND kind='billing'", cred).Scan(&detail)
			t.Fatalf("旧绑定的报价同步失败: %v （%s）", err, detail)
		}
		overview, err := s.Overview(ctx, provider, "")
		if err != nil || overview.WebsiteSync.PricedChannels != 1 {
			t.Fatalf("报价未入库，已采价渠道数 %d，err=%v", overview.WebsiteSync.PricedChannels, err)
		}
	})
	t.Run("旧发现绑定逐笔授权", func(t *testing.T) {
		got, keyID, _, err := s.PanelUsageAccess(ctx, cred)
		if err != nil || keyID != 41 || got != reviewCapture {
			t.Fatalf("未用交付会话读取旧发现绑定，err=%v", err)
		}
		owner = 18
		defer func() { owner = 17 }()
		if _, _, _, err := s.PanelUsageAccess(ctx, cred); !errors.Is(err, errKeyMatch) {
			t.Fatal("交付账号的 Key 主人改变后应拒绝使用旧账单绑定")
		}
	})
	t.Run("定时官网目录", func(t *testing.T) {
		if err := s.syncWebsiteDirectories(ctx, nil); err != nil {
			t.Fatal(err)
		}
		var saved bool
		err := db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM beichen_website_directories WHERE provider_id=$1 AND last_success IS NOT NULL)", provider).Scan(&saved)
		if err != nil || !saved {
			t.Fatalf("目录任务未成功写入，err=%v", err)
		}
	})
	t.Run("定时官网Key", func(t *testing.T) {
		if err := s.syncWebsiteKeyInventories(ctx); err != nil {
			t.Fatal(err)
		}
		var saved bool
		err := db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM beichen_website_key_sync WHERE provider_id=$1 AND status='current')", provider).Scan(&saved)
		if err != nil || !saved {
			t.Fatalf("官网 Key 任务未成功写入，err=%v", err)
		}
	})
	t.Run("渠道发现", func(t *testing.T) {
		exec("UPDATE beichen_provision_channels SET checked_at=NULL WHERE credential_id=$1", cred)
		if err := s.discoverChannelKeys(ctx, &cred); err != nil {
			t.Fatal(err)
		}
		var discovered bool
		err := db.QueryRowContext(ctx, "SELECT checked_at IS NOT NULL AND discovery_error IS NULL FROM beichen_provision_channels WHERE credential_id=$1", cred).Scan(&discovered)
		if err != nil || !discovered {
			t.Fatalf("渠道发现任务未成功写入，err=%v", err)
		}
	})
	t.Run("手动目录与Key清单", func(t *testing.T) {
		if got, err := s.CrawlPanelDirectory(ctx, provider); err != nil || got.Models == nil || *got.Models == 0 {
			t.Fatalf("手动目录失败: %v", err)
		}
		if got, err := s.RefreshWebsiteKeys(ctx, provider); err != nil || got.Status != "current" {
			t.Fatalf("手动官网 Key 失败: %v", err)
		}
	})
	t.Run("验证抓取", func(t *testing.T) {
		if got, err := s.CheckPanelAccount(ctx, provider); err != nil || got.Models == nil || *got.Models == 0 {
			t.Fatalf("验证抓取失败: %v", err)
		}
	})
	t.Run("本机旧授权验证", func(t *testing.T) {
		s.sharedPanel = &LocalPanelConfig{provider: provider, origin: "https://school.example.test", grant: grant.grant}
		defer func() { s.sharedPanel = nil }()
		if err := s.CheckSharedPanelConnection(ctx, provider); err == nil {
			t.Fatal("旧本机授权验证误报成功")
		}
	})
}

func TestReviewClearedLegacySourcesPostgres(t *testing.T) {
	ctx, db, provider, actor, keys := newActionsPostgres(t)
	manager := switchTestManager(t)
	s := New(db, manager)
	configureReviewCapture(t, s)
	cleared, owner := true, 17
	wrongSource := reviewPanelFixture(t, s, &cleared, &owner)
	cred := keys[0]
	cipher, err := manager.EncryptCredential(reviewInference)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE provider_credentials SET credential_ciphertext=$2 WHERE id=$1", cred, cipher); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClearPanelAccount(ctx, provider, actor); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfigurePanel(reviewOld, fmt.Sprintf(`{%q:15}`, cred.String())); err != nil {
		t.Fatal(err)
	}
	s.ConfigurePanelFallback(reviewOld)
	t.Run("环境分组令牌", func(t *testing.T) {
		access, err := s.loadPriceAccess(ctx, cred)
		if err != nil {
			t.Fatal(err)
		}
		used, err := readWebsiteKey(ctx, s, access, reviewInference, func(bearer string, key listedKey) (bool, error) {
			return bearer == reviewCapture && key.ID == 41, nil
		})
		if err != nil || !used {
			t.Errorf("逐 Key 读取没有选择交付授权: %v", err)
		}
		group, peak := int64(15), false
		info := billingInfo{GroupID: &group, PeakEnabled: &peak, ObservedAt: s.now(), GroupRate: "0.15", ResolvedRate: "0.15", Multiplier: "0.15"}
		wrongBefore := wrongSource.Load()
		quote := s.fetchChannelQuote(ctx, access, reviewInference, info, []catalog.ProviderEntry{{ID: "shared"}})
		if quote.err != nil || quote.groupID != group || len(quote.terms) == 0 || wrongSource.Load() != wrongBefore {
			t.Errorf("旧报价入口尝试了旧令牌，或没有得到官网报价: %v", quote.err)
		}
	})
	t.Run("旧逐笔绑定令牌", func(t *testing.T) {
		keyID := int64(41)
		if err := s.SetPanelAccess(ctx, provider, cred, actor, PanelAccessInput{Enabled: true, GroupID: 15, UpstreamKeyID: &keyID, Token: reviewOld}); err != nil {
			t.Fatal(err)
		}
		got, actualKeyID, _, err := s.PanelUsageAccess(ctx, cred)
		if err != nil || got != reviewCapture || actualKeyID != keyID {
			t.Fatalf("旧逐笔绑定恢复了旧令牌或拒绝了经过核对的交付会话，err=%v", err)
		}
	})
	t.Run("邮箱固定绑定", func(t *testing.T) {
		keyID := int64(41)
		if err := s.write(ctx, provider, cred, func(tx *sql.Tx) error {
			return s.WritePanelAccessTx(ctx, tx, cred, actor, PanelAccessInput{
				Enabled: true, GroupID: 15, UpstreamKeyID: &keyID, webAccount: accountDigest("another@review.example"),
			})
		}); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := s.PanelUsageAccess(ctx, cred); err == nil {
			t.Fatal("交付会话绕过了逐笔绑定邮箱检查")
		}
		if _, err := s.readBillingCatalog(ctx, syncJob{provider: provider, credential: cred}, reviewInference); err == nil {
			t.Fatal("交付会话绕过了报价绑定邮箱检查")
		}
	})
	t.Run("本机登录不能抢先", func(t *testing.T) {
		store := &LocalLoginStore{dir: t.TempDir()}
		if err := store.save(localPanelLogin{Account: "local@review.example", Access: reviewOld}); err != nil {
			t.Fatal(err)
		}
		s.UseLocalLogin(store)
		defer s.UseLocalLogin(nil)
		got, err := readWithWebsiteKeySession(ctx, s, provider, "", func(auth panelAuthorization) (string, error) {
			return auth.bearer, nil
		})
		if err != nil || got != reviewCapture {
			t.Fatalf("清除后本机登录抢先，err=%v", err)
		}
	})
	t.Run("普通启动密码不能恢复", func(t *testing.T) {
		withoutCapture := New(db, manager)
		if err := withoutCapture.ConfigureWebSession("other@review.example", "fixture-password"); err != nil {
			t.Fatal(err)
		}
		_, _, err := withoutCapture.managedPanelAuthorization(ctx, provider, reviewOld)
		if !errors.Is(err, errPanelAccountMissing) {
			t.Fatalf("清除后把启动密码配置当成交付会话，err=%v", err)
		}
	})
	t.Run("状态边界", func(t *testing.T) {
		if _, err := db.ExecContext(ctx, "UPDATE beichen_panel_accounts SET token_ciphertext=$2 WHERE provider_id=$1", provider, cipher); err == nil {
			t.Fatal("数据库允许已清除状态保留非空凭证")
		}
		const problem = "模拟密码登录失败"
		if _, err := db.ExecContext(ctx, "UPDATE beichen_panel_accounts SET status='error',last_error=$2 WHERE provider_id=$1", provider, problem); err != nil {
			t.Fatal(err)
		}
		_, _, err := s.managedPanelAuthorization(ctx, provider, "")
		if err == nil || err.Error() != problem {
			t.Fatalf("登录失败的记录被当成已清除，err=%v", err)
		}
	})
}
