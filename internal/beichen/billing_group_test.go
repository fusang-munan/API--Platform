package beichen

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestIndexKeyGroupsMarksStaleBillingGroup(t *testing.T) {
	credentialID := uuid.New()
	oldGroupID := int64(2)
	for _, tc := range []struct {
		name, groupStatus, billingState string
		wantStale, wantCurrent          bool
	}{
		{"assigned stale", "assigned", "stale", true, false},
		{"legacy stale", "", "stale", true, false},
		{"assigned current", "assigned", "current", false, true},
		{"assigned unverified", "assigned", "unverified", false, false},
		{"not allowed", "not_allowed", "stale", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keys := []discoveryKey{{CatalogKey: CatalogKey{CredentialRef: CredentialRef{CredentialID: credentialID}}}}
			indexKeyGroups([]Dataset{{
				CredentialID: credentialID, Kind: "billing", GroupID: &oldGroupID,
				GroupStatus: tc.groupStatus, BillingState: tc.billingState,
			}}, keys, nil)
			key := keys[0]
			if key.GroupID == nil || *key.GroupID != oldGroupID || key.GroupStale != tc.wantStale ||
				key.groupCurrent != tc.wantCurrent {
				t.Fatalf("unexpected group state: %+v", key)
			}
		})
	}
}

func TestBeichenGroupDenialError(t *testing.T) {
	cases := []struct {
		name, host, body string
		denied           bool
	}{
		{"nested code", "api.beichenapi.com", `{"error":{"code":"GROUP_NOT_ALLOWED","message":"private upstream text"}}`, true},
		{"top-level reason", "api.beichenapi.com", `{"reason":"GROUP_NOT_ALLOWED"}`, true},
		{"ordinary 403", "api.beichenapi.com", `{"error":{"code":"IP_NOT_ALLOWED","message":"GROUP_NOT_ALLOWED"}}`, false},
		{"oversize", "api.beichenapi.com", `{"code":"GROUP_NOT_ALLOWED"}` + strings.Repeat(" ", 4096), false},
		{"web session 403", "beichenapi.com", `{"code":"GROUP_NOT_ALLOWED"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodGet, "https://"+tc.host+billingPath, nil)
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Transport: panelTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			_, err = readBeichenResponse(client, request)
			if errors.Is(err, errKeyGroupNotAllowed) != tc.denied ||
				errors.Is(err, upstreamHTTPStatus(http.StatusForbidden)) == tc.denied {
				t.Fatalf("wrong 403 classification: %v", err)
			}
		})
	}
}

func TestBeichenBillingGroupDenialPostgres(t *testing.T) {
	ctx, db, provider, _, keys := newActionsPostgres(t)
	key := keys[0]
	const secret, model = "sk-fixture-gemini", "gemini-3.7-flash"
	manager := switchTestManager(t)
	ciphertext, err := manager.EncryptCredential(secret)
	if err != nil {
		t.Fatal(err)
	}
	execCatalogTestSQL(t, ctx, db, `UPDATE provider_credentials SET credential_ciphertext=$2 WHERE id=$1`, key, ciphertext)
	const legacyError = "北辰面板找不到当前 Key 分组"
	execCatalogTestSQL(t, ctx, db, `
INSERT INTO beichen_key_observations (credential_id,key_digest,website_key_id,models_error,usage_error)
VALUES ($1,sha256($2::bytea),41,$3,$3)`, key, ciphertext, legacyError)
	execCatalogTestSQL(t, ctx, db, `
INSERT INTO beichen_datasets (credential_id,kind,last_error) VALUES ($1,'prices',$2)`, key, legacyError)
	service := New(db, manager)
	service.ConfigurePanelFallback("fixture-session")
	const knownPrice = `{"name":"gemini-3.7-flash","pricing":{"billing_mode":"token","input_price":0.000004,"output_price":0.00002}}`
	website := websiteGroupFixture(5, "0.3", knownPrice)
	listedSecret, group := secret, `"group_id":5,"group":{"id":5,"name":"[Gemini]国外豆包","rate_multiplier":0.30}`
	allowed := false
	var billingReads int
	service.client.Transport = panelTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			return nil, errors.New("only fixture metadata GET is accepted")
		}
		status, body := http.StatusOK, ""
		switch r.URL.String() {
		case panelURL:
			body = website
		case "https://beichenapi.com/api/v1/groups/rates":
			body = `{"code":0,"data":{"5":0.30,"17":0.50}}`
		case accountKeysURL + "?page_size=100&page=1":
			body = fmt.Sprintf(`{"code":0,"data":{"items":[{"id":41,"user_id":13,"key":%q,"name":"哈吉米","status":"active",%s}],
				"total":1,"page":1,"page_size":100,"pages":1}}`, listedSecret, group)
		case "https://api.beichenapi.com" + billingPath:
			billingReads++
			status, body = http.StatusForbidden, `{"error":{"type":"GROUP_NOT_ALLOWED"}}`
			if allowed {
				status = http.StatusOK
				body = fmt.Sprintf(`{"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token",
					"group_id":17,"effective_rate_multiplier":0.5,"group_rate_multiplier":0.5,
					"peak_rate_enabled":false,"observed_at":%q}`, time.Now().UTC().Format(time.RFC3339Nano))
			}
		case "https://api.beichenapi.com" + modelsPath:
			body = `{"data":[{"id":"gemini-3.7-flash"}]}`
		default:
			if r.URL.Host != "beichenapi.com" || r.URL.Path != "/api/v1/usage" || r.URL.Query().Get("api_key_id") != "41" {
				return nil, errors.New("unexpected fixture endpoint")
			}
			body = `{"code":0,"data":{"items":[],"total":0,"page":1,"page_size":100,"pages":0}}`
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	overview := func() Overview {
		t.Helper()
		view, err := service.Overview(ctx, provider, "")
		if err != nil {
			t.Fatal(err)
		}
		return view
	}
	refresh := func(kind string) (Overview, error) {
		t.Helper()
		_, syncErr := service.runJob(ctx, syncJob{provider: provider, credential: key, kind: kind}, true)
		return overview(), syncErr
	}
	capability := func(view Overview) KeyCapability {
		t.Helper()
		for _, item := range view.KeyCapabilities {
			if item.CredentialID == key {
				return item
			}
		}
		t.Fatal("missing Gemini Key capability")
		return KeyCapability{}
	}
	view := overview()
	card := capability(view)
	if card.GroupStatus != "pending" || card.WebsiteGroup != nil || card.ModelsError == nil || *card.ModelsError != "分组待同步" {
		t.Fatalf("old errors must not be evidence that the Key is unassigned: %+v", card)
	}
	prices := datasetForCredential(t, view, key, "prices")
	if prices.LastError == nil || *prices.LastError != "分组待同步" {
		t.Fatalf("old price error was not normalized: %+v", prices)
	}
	view, err = refresh("billing")
	if err != nil || len(view.Models) != 1 || view.Models[0].Model != model || view.Models[0].PriceStatus != "confirmed" {
		t.Fatalf("expected the model to start with a confirmed price: %v", err)
	}
	website = websiteGroupFixture(6, "0.3", knownPrice)
	view, err = refresh("billing")
	if !errors.Is(err, errKeyGroupNotAllowed) || billingReads != 1 {
		t.Fatalf("missing group in the price directory must check the Key permission: %v", err)
	}
	billing := datasetForCredential(t, view, key, "billing")
	prices = datasetForCredential(t, view, key, "prices")
	if billing.GroupStatus != "not_allowed" || billing.GroupID == nil || *billing.GroupID != 5 ||
		billing.GroupName == nil || *billing.GroupName != "[Gemini]国外豆包" || billing.GroupRate == nil || *billing.GroupRate != "0.30" ||
		billing.BillingState != "stale" || prices.LastError == nil || *prices.LastError != "账号无 group 5 权限" || prices.OfficialPriceMissing ||
		len(view.Models) != 1 || view.Models[0].UpstreamRates != nil {
		t.Fatalf("denial did not preserve the Key identity and expire its quote: billing=%+v prices=%+v", billing, prices)
	}
	card = capability(view)
	if card.GroupStatus != "not_allowed" || card.WebsiteGroup == nil || card.WebsiteGroup.ID != 5 ||
		card.WebsiteGroup.Name != "[Gemini]国外豆包" || card.WebsiteGroup.Rate == nil || *card.WebsiteGroup.Rate != "0.30" ||
		card.ModelsError == nil || *card.ModelsError != "账号无 group 5 权限" {
		t.Fatalf("the card did not use billing evidence instead of its old Key usage error: %+v", card)
	}

	group = `"group_id":null`
	view, err = refresh("billing")
	billing = datasetForCredential(t, view, key, "billing")
	card = capability(view)
	if !errors.Is(err, errKeyGroupMissing) || billingReads != 1 || billing.GroupStatus != "not_found" ||
		billing.GroupID != nil || billing.GroupName != nil || billing.GroupRate != nil ||
		card.GroupStatus != "not_found" || card.WebsiteGroup != nil {
		t.Fatalf("an unassigned Key must be distinguished from an inaccessible group: billing=%+v card=%+v err=%v", billing, card, err)
	}

	group = `"description":"response omitted its group field"`
	view, err = refresh("billing")
	billing = datasetForCredential(t, view, key, "billing")
	if !errors.Is(err, errPanelGroupUnverified) || billingReads != 1 || billing.GroupStatus != "pending" || capability(view).GroupStatus != "pending" {
		t.Fatalf("an omitted group field must not indicate a missing group: %+v, %v", billing, err)
	}

	listedSecret = "sk-unrelated-fixture"
	view, err = refresh("billing")
	billing = datasetForCredential(t, view, key, "billing")
	if !errors.Is(err, errKeyMatch) || billingReads != 1 || billing.GroupStatus != "pending" {
		t.Fatalf("credential mismatch must be distinguished from an unassigned Key: %+v, %v", billing, err)
	}

	listedSecret, group = secret, `"group_id":5,"group":{"id":5,"name":"[Gemini]国外豆包","rate_multiplier":0.30}`
	view, err = refresh("key_usage")
	billing, card = datasetForCredential(t, view, key, "billing"), capability(view)
	if !errors.Is(err, errKeyGroupNotAllowed) || billingReads != 2 || billing.GroupStatus != "not_allowed" ||
		card.GroupStatus != "not_allowed" || card.WebsiteGroup == nil || card.WebsiteGroup.ID != 5 {
		t.Fatalf("a Key-only check must publish the same denial: billing=%+v card=%+v err=%v", billing, card, err)
	}

	// A slower billing job may finish after the Key check using an older list.
	var revision time.Time
	if err := db.QueryRowContext(ctx, "SELECT updated_at FROM provider_credentials WHERE id=$1", key).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	older := time.Now().UTC().Add(-time.Minute)
	staleRead := websiteKeyCatalog{keyGroup: &WebsiteGroup{ID: 17}, keyGroupAt: older}
	if _, err := saveBillingGroup(ctx, tx, key, revision, older, staleRead, nil, true); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if card := capability(overview()); card.GroupStatus != "not_allowed" || card.WebsiteGroup == nil || card.WebsiteGroup.ID != 5 {
		t.Fatalf("an older group observation replaced the denial: %+v", card)
	}

	listedSecret, group, allowed = secret, `"group_id":17,"group":{"id":17,"rate_multiplier":0.50}`, true
	website = `{"code":0,"data":[]}`
	view, err = refresh("billing")
	billing, prices = datasetForCredential(t, view, key, "billing"), datasetForCredential(t, view, key, "prices")
	card = capability(view)
	if err != nil || billingReads != 3 || billing.GroupStatus != "assigned" || billing.GroupID == nil ||
		*billing.GroupID != 17 || billing.BillingState != "current" || billing.LastError != nil || !prices.OfficialPriceMissing ||
		len(view.Models) != 1 || view.Models[0].UpstreamRates != nil ||
		card.GroupStatus != "assigned" || card.WebsiteGroup == nil || card.WebsiteGroup.ID != 17 || card.ModelsError != nil {
		t.Fatalf("missing official prices must not fail the verified multiplier: billing=%+v prices=%+v err=%v", billing, prices, err)
	}
}

func datasetForCredential(t *testing.T, view Overview, key uuid.UUID, kind string) Dataset {
	t.Helper()
	for _, dataset := range view.Datasets {
		if dataset.CredentialID == key && dataset.Kind == kind {
			return dataset
		}
	}
	t.Fatalf("missing %s dataset", kind)
	return Dataset{}
}
