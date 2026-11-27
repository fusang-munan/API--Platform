package beichen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const crawlKeyChannelsFixture = `{"code":0,"data":[{"platforms":[
 {"platform":"anthropic","groups":[{"id":2,"name":"示例 CC1","rate_multiplier":0.15,"models":["fixture-claude","fixture-claude"]},
 {"id":99,"name":"空分组","models":[]}]},
 {"platform":"openai","models":[{"id":"fixture-openai"}],"groups":[{"id":10}]}
]}]}`

func crawlKeyListFixture(cc2, cc1, empty string) string {
	return fmt.Sprintf(`{"code":0,"data":{"total":4,"page":1,"page_size":100,"pages":1,"items":[
 {"id":1001,"user_id":42,"name":"示例 CC2 Key","key":%q,"status":"active","group_id":16,
  "group":{"id":16,"name":"示例 CC2","rate_multiplier":0.60,"peak_rate_enabled":false}},
 {"id":1002,"user_id":42,"name":"示例 CC1 Key","key":%q,"status":"active","group_id":2,
  "group":{"id":2,"rate_multiplier":0.15,"peak_rate_enabled":true,"peak_rate_multiplier":1.5,"peak_start":"18:00:00","peak_end":"21:00"}},
 {"id":1003,"user_id":42,"name":"示例空分组 Key","key":%q,"status":"inactive","group_id":99,"group":{"id":99,"name":"空分组","rate_multiplier":0.1}},
 {"id":1004,"user_id":42,"name":"示例无分组 Key","key":"sk-example…mask","status":"disabled","group_id":null}
]}}`, cc2, cc1, empty)
}

type crawlModelFixture struct {
	status int
	body   string
}

func mockCrawlKeyEndpoints(t *testing.T, service *Service, bearer string, channels, keys *string, models map[string]crawlModelFixture) *int {
	t.Helper()
	requests := 0
	service.client.Transport = panelTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "api.beichenapi.com" {
			fixture, found := models[r.Header.Get("Authorization")]
			if !found || r.Method != http.MethodGet || r.URL.Path != modelsPath || r.URL.RawQuery != "" || r.URL.Scheme != "https" {
				t.Error("model catalog request used an unexpected endpoint or Key")
				return nil, errors.New("unexpected model catalog request")
			}
			return &http.Response{StatusCode: fixture.status, Body: io.NopCloser(strings.NewReader(fixture.body))}, nil
		}
		if r.Method != http.MethodGet || r.URL.Host != "beichenapi.com" || r.Header.Get("Authorization") != "Bearer "+bearer {
			t.Error("website request used an unexpected origin, method or credential")
			return nil, errors.New("unexpected website request")
		}
		requests++
		var body string
		switch r.URL.Path {
		case "/api/v1/channels/available":
			body = *channels
		case "/api/v1/keys":
			if r.URL.Query().Get("page") != "1" || r.URL.Query().Get("page_size") != "100" {
				t.Fatal("Key list request omitted pagination")
			}
			body = *keys
		default:
			t.Fatal("crawl requested an unrelated endpoint")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	return &requests
}

func TestBeichenCrawlWebsiteKeys(t *testing.T) {
	service := New(nil, nil)
	bearer, cc2Secret := uuid.NewString(), "sk-"+uuid.NewString()
	cc1Secret, deniedSecret := "sk-"+uuid.NewString(), "sk-"+uuid.NewString()
	channels := strings.Replace(crawlKeyChannelsFixture, `"id":"fixture-openai"`, `"id":"fixture-openai","pricing":{"input_price":{}}`, 1)
	channels = strings.Replace(channels, `"fixture-claude","fixture-claude"`, `"fixture-claude","fixture-claude","fixture-only-group"`, 1)
	keyBody := crawlKeyListFixture(cc2Secret, cc1Secret, deniedSecret)
	models := map[string]crawlModelFixture{
		"Bearer " + cc2Secret:    {http.StatusOK, `{"data":[]}`},
		"Bearer " + cc1Secret:    {http.StatusOK, `{"data":[{"id":"Fixture-Claude"},{"id":"fixture-only-key"}]}`},
		"Bearer " + deniedSecret: {http.StatusForbidden, deniedSecret},
	}
	requests := mockCrawlKeyEndpoints(t, service, bearer, &channels, &keyBody, models)
	options := CrawlOptions{PanelToken: bearer, IncludeKeys: true}
	report, err := service.CrawlDirectory(context.Background(), options)
	if !errors.Is(err, errWebsiteDirectoryPartial) || !errors.Is(err, errCrawlKeyModelsPartial) || report.Complete ||
		*requests != 2 || report.Keys == nil || len(*report.Keys) != 4 {
		t.Fatal("expected both website reads and a price diagnostic with the complete Key list")
	}
	cc2, cc1, empty, unknown := (*report.Keys)[0], (*report.Keys)[1], (*report.Keys)[2], (*report.Keys)[3]
	if cc2.GroupID == nil || *cc2.GroupID != 16 || cc2.Rate == nil || *cc2.Rate != "0.60" || cc2.GroupCheck != "missing_group" ||
		cc2.Models == nil || len(cc2.Models) != 0 || cc2.Message != "该分组未分配模型" || cc2.KeyPrefix == nil || *cc2.KeyPrefix != cc2Secret[:10] ||
		cc2.CrawlKeyModels == nil || cc2.Check != "empty" || cc2.ActualModelCount == nil || *cc2.ActualModelCount != 0 ||
		cc2.ActualModels == nil || cc2.Matched == nil || cc2.OnlyKey == nil || cc2.OnlyGroup == nil {
		t.Fatal("pricing-only error obscured the missing group or changed its Key metadata")
	}
	if cc1.GroupCheck != "listed" || cc1.Group == nil || *cc1.Group != "示例 CC1" ||
		!slices.Equal(cc1.Models, []string{"fixture-claude", "fixture-only-group"}) ||
		cc1.Peak.Enabled == nil || !*cc1.Peak.Enabled || cc1.Peak.Start == nil || *cc1.Peak.Start != "18:00" {
		t.Fatal("same-group models or Key peak rules were lost when supplementing the group name")
	}
	if cc1.CrawlKeyModels == nil || cc1.ActualModelCount == nil || *cc1.ActualModelCount != 2 ||
		!slices.Equal(cc1.ActualModels, []string{"Fixture-Claude", "fixture-only-key"}) ||
		!slices.Equal(cc1.Matched, []CrawlKeyModelPair{{KeyModel: "Fixture-Claude", ChannelModel: "fixture-claude", Match: "normalized"}}) ||
		!slices.Equal(cc1.OnlyKey, []string{"fixture-only-key"}) || !slices.Equal(cc1.OnlyGroup, []string{"fixture-only-group"}) {
		t.Fatal("model catalogs were not compared against the matching Key's own group")
	}
	if empty.GroupCheck != "no_models" || empty.CrawlKeyModels == nil || empty.Check != "unavailable" || empty.ActualModelCount != nil ||
		empty.OnlyGroup != nil || !strings.Contains(empty.CrawlKeyModels.Message, "HTTP 403") ||
		unknown.GroupCheck != "unknown_group" || unknown.Status != "disabled" || unknown.CrawlKeyModels == nil ||
		unknown.Check != "unverified" || unknown.ActualModelCount != nil {
		t.Fatal("rejected or masked Keys were reported as confirmed empty inventories")
	}
	encoded, err := json.Marshal(report)
	if err != nil || strings.Contains(string(encoded), cc2Secret) || strings.Contains(string(encoded), cc1Secret) ||
		strings.Contains(string(encoded), deniedSecret) || strings.Contains(string(encoded), bearer) {
		t.Fatal("serialized report contained a full credential")
	}
	channels = strings.Replace(channels, `"models":[{"id":"fixture-openai","pricing":{"input_price":{}}}]`, `"models":{}`, 1)
	report, err = service.CrawlDirectory(context.Background(), options)
	if !errors.Is(err, errWebsiteDirectoryPartial) || report.Keys == nil || len(*report.Keys) != 4 ||
		(*report.Keys)[0].GroupCheck != "unverified" || (*report.Keys)[1].ActualModelCount == nil || (*report.Keys)[1].OnlyKey != nil {
		t.Fatal("unreadable membership was reported as a confirmed absence")
	}
}

func TestBeichenCrawlWebsiteKeyRatesPostgres(t *testing.T) {
	ctx, db, provider, _, credentials := newActionsPostgres(t)
	manager := switchTestManager(t)
	service := New(db, manager)
	now := time.Now().UTC().Truncate(time.Microsecond)
	service.now = func() time.Time { return now }
	bearer, secret := uuid.NewString(), "sk-"+uuid.NewString()
	service.ConfigurePanelFallback(bearer)
	channels, keyBody := crawlKeyChannelsFixture, crawlKeyListFixture(secret, "", "")
	requests := mockCrawlKeyEndpoints(t, service, bearer, &channels, &keyBody, map[string]crawlModelFixture{
		"Bearer " + secret: {http.StatusOK, `{"data":[]}`},
	})
	encrypted, err := manager.EncryptCredential(secret)
	if err != nil {
		t.Fatal(err)
	}
	credential := credentials[3]
	execTestSQL(t, ctx, db, "UPDATE provider_credentials SET credential_ciphertext=$2 WHERE id=$1", credential, encrypted)

	readOnly, err := service.CrawlDirectory(ctx, CrawlOptions{ProviderID: provider, IncludeKeys: true})
	before, viewErr := service.WebsiteKeys(ctx, provider)
	if !errors.Is(err, errCrawlKeyModelsPartial) || viewErr != nil || readOnly.Keys == nil || before.Status != "unsynced" || *requests != 2 {
		t.Fatal("read-only crawl did not preserve the unsynced database")
	}
	options := CrawlOptions{ProviderID: provider, SaveKeys: true}
	report, err := service.CrawlDirectory(ctx, options)
	baseline, viewErr := service.WebsiteKeys(ctx, provider)
	if err != nil || viewErr != nil || report.KeyStorage != "saved" || *requests != 4 || len(baseline.Keys) != 4 ||
		baseline.Keys[0].ObservedAt == nil || !baseline.Keys[0].ObservedAt.Equal(now) || baseline.Keys[0].ChangedAt != nil ||
		baseline.Keys[0].Rate == nil || *baseline.Keys[0].Rate != "0.6" || baseline.Keys[0].MatchStatus != "matched" ||
		report.Keys == nil || len(*report.Keys) != 4 || (*report.Keys)[0].ObservedAt == nil || !(*report.Keys)[0].ObservedAt.Equal(now) {
		t.Fatal("CLI did not persist the same Key observation with an initial rate version")
	}
	if err := service.syncWebsiteKeyInventories(ctx); err != nil || *requests != 4 {
		t.Fatal("scheduled Key read ran before the next due time")
	}
	due := func() {
		t.Helper()
		execTestSQL(t, ctx, db, "UPDATE beichen_website_key_sync SET next_attempt=clock_timestamp() WHERE provider_id=$1", provider)
	}
	now = now.Add(time.Minute)
	changedAt := now
	keyBody = strings.Replace(keyBody, `"rate_multiplier":0.60`, `"rate_multiplier":0.75`, 1)
	keyBody = strings.Replace(keyBody, `"peak_rate_multiplier":1.5`, `"peak_rate_multiplier":1.7`, 1)
	due()
	if err := service.syncWebsiteKeyInventories(ctx); err != nil || *requests != 6 {
		t.Fatal("scheduled Key read did not refresh the due inventory")
	}
	changed, err := service.WebsiteKeys(ctx, provider)
	if err != nil || len(changed.Keys) != 4 || changed.Keys[0].ChangedAt == nil || !changed.Keys[0].ChangedAt.Equal(changedAt) ||
		changed.Keys[0].Rate == nil || *changed.Keys[0].Rate != "0.75" || changed.Keys[1].ChangedAt == nil ||
		!changed.Keys[1].ChangedAt.Equal(changedAt) || changed.Keys[1].Peak.Rate == nil || *changed.Keys[1].Peak.Rate != "1.7" ||
		changed.Keys[1].MatchStatus != "unmatched" || changed.Keys[2].ChangedAt != nil {
		t.Fatal("scheduled sync failed to track base and peak-only changes separately by Key")
	}

	keyBody = strings.Replace(keyBody, `"rate_multiplier":0.75`, `"rate_multiplier":0.750`, 1)
	now = now.Add(time.Minute)
	report, err = service.CrawlDirectory(ctx, options)
	if err != nil || *requests != 8 || report.Keys == nil || len(*report.Keys) != 4 ||
		(*report.Keys)[0].ChangedAt == nil || !(*report.Keys)[0].ChangedAt.Equal(changedAt) {
		t.Fatal("CLI save did not return the last change from the stored version")
	}
	keyBody = strings.Replace(keyBody, `"total":4`, `"total":5`, 1)
	due()
	if err := service.syncWebsiteKeyInventories(ctx); err != nil || *requests != 10 {
		t.Fatal("invalid page did not complete its scheduled failure handling")
	}
	after, err := service.WebsiteKeys(ctx, provider)
	if err != nil || after.Status != "unavailable" || len(after.Keys) != 4 || after.Keys[0].ChangedAt == nil ||
		!after.Keys[0].ChangedAt.Equal(changedAt) || after.LastSeen == nil || !after.LastSeen.Equal(now) ||
		after.FailedPage != 1 || !strings.Contains(after.LastError, "条目数与公布总数不一致") {
		t.Fatal("equivalent precision or partial pagination changed the last confirmed rate version")
	}
	var historyCount, multiplierCount int
	var group int64
	var rate string
	err = db.QueryRowContext(ctx, `
SELECT (SELECT count(*) FROM beichen_website_key_rate_versions WHERE provider_id=$1),
       (SELECT count(*) FROM beichen_multiplier_versions WHERE credential_id=$2),group_id,trim_scale(group_rate)::text
FROM beichen_multiplier_versions WHERE credential_id=$2 ORDER BY effective_at DESC,id DESC LIMIT 1`, provider, credential).
		Scan(&historyCount, &multiplierCount, &group, &rate)
	if err != nil || historyCount != 6 || multiplierCount != 2 || group != 16 || rate != "0.75" {
		t.Fatal("website history or matched billing multiplier lost a version or duplicated an observation")
	}
}
