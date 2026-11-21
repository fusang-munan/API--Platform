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
			return nil, errors.New("unexpected fixture endpoint")
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	refresh := func() (Overview, error) {
		t.Helper()
		_, syncErr := service.runJob(ctx, syncJob{provider: provider, credential: key, kind: "billing"}, true)
		view, err := service.Overview(ctx, provider, "")
		if err != nil {
			t.Fatal(err)
		}
		return view, syncErr
	}
	view, err := refresh()
	if err != nil || len(view.Models) != 1 || view.Models[0].Model != model || view.Models[0].PriceStatus != "confirmed" {
		t.Fatalf("expected the model to start with a confirmed price: %v", err)
	}
	website = websiteGroupFixture(6, "0.3", knownPrice)
	view, err = refresh()
	if !errors.Is(err, errKeyGroupNotAllowed) || billingReads != 1 {
		t.Fatalf("missing group in the price directory must check the Key permission: %v", err)
	}
	billing := datasetForCredential(t, view, key, "billing")
	prices := datasetForCredential(t, view, key, "prices")
	if billing.GroupStatus != "not_allowed" || billing.GroupID == nil || *billing.GroupID != 5 ||
		billing.GroupName == nil || *billing.GroupName != "[Gemini]国外豆包" || billing.GroupRate == nil || *billing.GroupRate != "0.30" ||
		billing.BillingState != "stale" || prices.LastError == nil || *prices.LastError != "账号无 group 5 权限" || prices.OfficialPriceMissing ||
		len(view.Models) != 1 || view.Models[0].UpstreamRates != nil {
		t.Fatalf("denial did not preserve the Key identity and expire its quote: billing=%+v prices=%+v", billing, prices)
	}

	group = `"group_id":null`
	view, err = refresh()
	billing = datasetForCredential(t, view, key, "billing")
	if !errors.Is(err, errKeyGroupMissing) || billingReads != 1 || billing.GroupStatus != "not_found" ||
		billing.GroupID != nil || billing.GroupName != nil || billing.GroupRate != nil {
		t.Fatalf("an unassigned Key must be distinguished from an inaccessible group: %+v, %v", billing, err)
	}

	group = `"description":"response omitted its group field"`
	view, err = refresh()
	billing = datasetForCredential(t, view, key, "billing")
	if !errors.Is(err, errPanelGroupUnverified) || billingReads != 1 || billing.GroupStatus != "" {
		t.Fatalf("an omitted group field must not indicate a missing group: %+v, %v", billing, err)
	}

	listedSecret = "sk-unrelated-fixture"
	view, err = refresh()
	billing = datasetForCredential(t, view, key, "billing")
	if !errors.Is(err, errKeyMatch) || billingReads != 1 || billing.GroupStatus != "" {
		t.Fatalf("credential mismatch must be distinguished from an unassigned Key: %+v, %v", billing, err)
	}

	listedSecret, group, allowed = secret, `"group_id":17,"group":{"id":17,"rate_multiplier":0.50}`, true
	website = `{"code":0,"data":[]}`
	view, err = refresh()
	billing, prices = datasetForCredential(t, view, key, "billing"), datasetForCredential(t, view, key, "prices")
	if err != nil || billingReads != 2 || billing.GroupStatus != "assigned" || billing.GroupID == nil ||
		*billing.GroupID != 17 || billing.BillingState != "current" || billing.LastError != nil || !prices.OfficialPriceMissing ||
		len(view.Models) != 1 || view.Models[0].UpstreamRates != nil {
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
