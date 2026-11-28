package beichen

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/catalog"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

type Dataset struct {
	CredentialID            uuid.UUID    `json:"credential_id"`
	Kind                    string       `json:"kind"`
	LastSuccess             *time.Time   `json:"last_success"`
	LastError               *string      `json:"last_error"`
	RefreshError            *string      `json:"refresh_error,omitempty"`
	EffectiveMultiplier     *json.Number `json:"effective_multiplier"`
	EffectiveMultiplierText string       `json:"effective_multiplier_text,omitempty"`
	AccountRate             *json.Number `json:"account_rate_multiplier,omitempty"`
	AccountRateSource       string       `json:"account_rate_source,omitempty"`
	BillingState            string       `json:"billing_state,omitempty"`
	GroupID                 *int64       `json:"group_id,omitempty"`
	GroupName               *string      `json:"group_name,omitempty"`
	GroupRate               *string      `json:"group_rate,omitempty"`
	GroupStatus             string       `json:"group_status,omitempty"`
	OfficialPriceMissing    bool         `json:"official_price_missing,omitempty"`
	PeakEnabled             *bool        `json:"peak_enabled,omitempty"`
	PeakRate                *string      `json:"peak_rate,omitempty"`
	ObservedAt              *time.Time   `json:"observed_at,omitempty"`
	FailureCount            int          `json:"failure_count,omitempty"`
	Alert                   bool         `json:"alert,omitempty"`
	accountRateKeyID        int64
	websiteAccountRate      *json.Number
	personalMultiplier      bool
	confirmedKeyBinding     *websiteKeyBindingObservation
}

type Model struct {
	CredentialID         uuid.UUID       `json:"credential_id"`
	Model                string          `json:"model"`
	CatalogSource        string          `json:"catalog_source,omitempty"`
	Mode                 string          `json:"mode"`
	SchoolStatus         string          `json:"school_status"`
	RouteReady           bool            `json:"route_ready"`
	PriceStatus          string          `json:"price_status"`
	PriceSource          string          `json:"price_source,omitempty"`
	PriceObservedAt      *time.Time      `json:"price_observed_at,omitempty"`
	PriceConditions      json.RawMessage `json:"price_conditions"`
	PriceConditionsExact map[string]any  `json:"price_conditions_exact,omitempty"`
	UpstreamRates        *DisplayRates   `json:"upstream_rates"`
	SchoolRates          *DisplayRates   `json:"school_rates"`
	// SchoolRates are base credits before the school surcharge. Follow
	// rates already include the effective upstream multiplier.
	SchoolRatesIncludeEffectiveMultiplier bool         `json:"school_rates_include_effective_multiplier,omitempty"`
	QuoteEffectiveMultiplier              *string      `json:"quote_effective_multiplier"`
	StudentPrice                          StudentPrice `json:"student_price"`
	PriceCovered                          bool         `json:"price_covered"`
	// QuoteUsable describes an official quote with its own verified rate.
	QuoteUsable bool `json:"quote_usable"`

	available          bool
	currentKey         bool // the saved row belongs to this revision of the credential
	catalogCurrent     bool // the Key catalog can support current probe status
	protocols          []string
	probeState         string
	lastProbeAt        *time.Time
	credentialRejected bool
	priceReadiness     activationPrice
	schoolMultiplier   string
}

// The effective multiplier can yield fractions of a micro-unit. Decimal
// strings also preserve integral amounts above JavaScript's safe range.
type DisplayRates struct {
	Input      *string `json:"input"`
	Output     *string `json:"output"`
	CacheRead  *string `json:"cache_read"`
	CacheWrite *string `json:"cache_write"`
}

type StudentPrice struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

const (
	studentPriceNoRoute     = "no_route"
	studentPriceRatePending = "multiplier_pending"
	studentPriceNoBase      = "missing_base_price"
	studentPriceHigher      = "only_higher"
	studentPriceManual      = "manual"
	studentPriceNormal      = "normal"
)

type Comparison struct {
	CredentialID             uuid.UUID `json:"credential_id"`
	Model                    string    `json:"model"`
	LocalRequests            *int64    `json:"local_requests"`
	UpstreamRequests         *int64    `json:"upstream_requests"`
	LocalInputTokens         *int64    `json:"local_input_tokens"`
	UpstreamInputTokens      *int64    `json:"upstream_input_tokens"`
	LocalOutputTokens        *int64    `json:"local_output_tokens"`
	UpstreamOutputTokens     *int64    `json:"upstream_output_tokens"`
	LocalCacheReadTokens     *int64    `json:"local_cache_read_tokens"`
	UpstreamCacheReadTokens  *int64    `json:"upstream_cache_read_tokens"`
	LocalCacheWriteTokens    *int64    `json:"local_cache_write_tokens"`
	UpstreamCacheWriteTokens *int64    `json:"upstream_cache_write_tokens"`
	EstimatedUpstreamMicros  *int64    `json:"estimated_upstream_micros"`
	BilledUpstreamMicros     *int64    `json:"billed_upstream_micros"`
	UpstreamUsageSource      *string   `json:"upstream_usage_source"`
	TrialCreditMilli         *int64    `json:"trial_credit_milli"`
	TrialCredits             *string   `json:"trial_credits"`
	TrialUnpricedCalls       *int64    `json:"trial_unpriced_calls"`
	ChargedCredits           *string   `json:"charged_credits"`
	PendingReviewCalls       *int64    `json:"pending_review_calls"`
}

type Overview struct {
	WebLoginRequired   bool                   `json:"web_login_required,omitempty"`
	WebsiteSync        WebsiteSyncStatus      `json:"website_sync"`
	WebsiteDirectory   WebsiteDirectoryStatus `json:"website_directory"`
	ArchivedUsage      []ArchivedUsage        `json:"archived_usage"`
	Credentials        []CredentialSummary    `json:"credentials"`
	KeyCapabilities    []KeyCapability        `json:"key_capabilities"`
	Datasets           []Dataset              `json:"datasets"`
	SchoolMultipliers  map[uuid.UUID]string   `json:"school_multipliers"`
	Models             []Model                `json:"models"`
	ModelCatalog       []CatalogModel         `json:"model_catalog"`
	Comparison         []Comparison           `json:"comparison"`
	CatalogDifferences []CatalogDifference    `json:"catalog_differences"`
	WebsiteOnlyModels  []WebsiteOnlyModel     `json:"website_only_models"`
	WebsiteOnlyStatus  string                 `json:"website_only_status"`
	ProbeRun           ProbeRun               `json:"probe_run"`
}

type CredentialSummary struct {
	CredentialID       uuid.UUID        `json:"credential_id"`
	Label              string           `json:"label"`
	Status             string           `json:"status"`
	CatalogLastSuccess *time.Time       `json:"catalog_last_success"`
	PricesLastSuccess  *time.Time       `json:"prices_last_success"`
	BillingLastSuccess *time.Time       `json:"billing_last_success"`
	UsageLastSuccess   *time.Time       `json:"usage_last_success"`
	LastError          *string          `json:"last_error"`
	ModelCount         int              `json:"model_count"`
	PricedModelCount   int              `json:"priced_model_count"`
	Health             CredentialHealth `json:"health"`
}

type CatalogModel struct {
	CanEnable            bool                      `json:"can_enable"`
	Published            bool                      `json:"published"`
	Active               bool                      `json:"active"` // At least one published binding has current Key evidence.
	Retired              bool                      `json:"retired,omitempty"`
	SchoolStatus         string                    `json:"school_status"`
	SchoolModel          string                    `json:"school_model,omitempty"`
	SchoolModelID        *uuid.UUID                `json:"school_model_id,omitempty"`
	Model                string                    `json:"model"`
	CredentialIDs        []uuid.UUID               `json:"credential_ids"`
	PreferredRoutes      []PreferredRoute          `json:"preferred_routes"`
	KeyCount             int                       `json:"key_count"`
	CurrentKeyCount      int                       `json:"current_key_count,omitempty"`
	StaleKeyCount        int                       `json:"stale_key_count,omitempty"`
	Source               string                    `json:"source,omitempty"`
	CatalogState         string                    `json:"catalog_state"`
	CatalogKeys          []CatalogKey              `json:"catalog_keys,omitempty"`
	WebsiteLocations     []WebsiteLocation         `json:"website_locations,omitempty"`
	RouteCount           int                       `json:"route_count"`
	ReadyCount           int                       `json:"ready_count"`
	PriceState           string                    `json:"price_state"`
	PriceCovered         bool                      `json:"price_covered"`
	StudentPrice         StudentPrice              `json:"student_price"`
	PriceBlocker         *CatalogBlocker           `json:"price_blocker,omitempty"`
	Blockers             []CatalogBlocker          `json:"blockers"`
	Activation           string                    `json:"activation"`
	Protocols            []string                  `json:"protocols"`
	ProbeState           string                    `json:"probe_state"`
	LastProbeAt          *time.Time                `json:"last_probe_at"`
	BindingIssue         *CatalogBlocker           `json:"binding_issue,omitempty"`
	BoundRoutes          []CatalogBoundRoute       `json:"bound_routes"`
	BoundKeyCount        int                       `json:"bound_key_count"`
	CoveredBoundKeyCount int                       `json:"covered_bound_key_count"`
	BoundCatalogPending  bool                      `json:"bound_catalog_pending"`
	ModelAvailability    catalog.ModelAvailability `json:"route_availability"`
	ActionHint           string                    `json:"action_hint,omitempty"`
	HistoricalChannels   []string                  `json:"historical_channels,omitempty"`
}

type CatalogBoundRoute struct {
	catalog.BeichenBinding
	UpstreamModel   string       `json:"upstream_model"`
	CredentialID    *uuid.UUID   `json:"credential_id"`
	CredentialLabel string       `json:"credential_label"`
	KeyEnabled      bool         `json:"key_enabled"`
	GroupID         *int64       `json:"group_id"`
	GroupStatus     string       `json:"group_status,omitempty"`
	GroupCheckedAt  *time.Time   `json:"group_checked_at,omitempty"`
	CatalogState    string       `json:"catalog_state"`
	PriceState      string       `json:"price_state"`
	PriceCovered    bool         `json:"price_covered"`
	StudentPrice    StudentPrice `json:"student_price"`
}

type CatalogBlocker struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type PreferredRoute struct {
	SchoolModel             string       `json:"school_model"`
	UpstreamModel           string       `json:"upstream_model"`
	ProviderName            string       `json:"provider_name"`
	CredentialLabel         *string      `json:"credential_label"`
	Adapter                 string       `json:"adapter"`
	ProviderID              uuid.UUID    `json:"provider_id"`
	CredentialID            *uuid.UUID   `json:"credential_id"`
	Priority                int          `json:"priority"`
	State                   string       `json:"state"`
	EffectiveMultiplier     *json.Number `json:"effective_multiplier"`
	EffectiveMultiplierText string       `json:"effective_multiplier_text,omitempty"`
}

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func checkProvider(ctx context.Context, db rowQueryer, providerID uuid.UUID) error {
	var found uuid.UUID
	err := db.QueryRowContext(ctx, "SELECT p.id FROM providers p WHERE p.id = $1 AND "+beichenProvider, providerID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NewError(domain.CodeNotFound, "北辰供应商不存在")
	}
	if err != nil {
		return unavailable("读取北辰供应商失败", err)
	}
	return nil
}

func (s *Service) Overview(ctx context.Context, providerID uuid.UUID, day string) (Overview, error) {
	if providerID == uuid.Nil {
		return Overview{}, invalid("需要供应商 ID")
	}
	if day == "" {
		day = s.now().In(beijing).Format(dayLayout)
	}
	start, err := dayStart(day)
	if err != nil {
		return Overview{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return Overview{}, unavailable("开启北辰概览查询失败", err)
	}
	defer tx.Rollback()
	if err := checkProvider(ctx, tx, providerID); err != nil {
		return Overview{}, err
	}
	view := Overview{Datasets: []Dataset{}, Models: []Model{}, Comparison: []Comparison{}}
	if s.webAccount != nil {
		view.WebLoginRequired = s.webAccount.humanVerification.Load()
	}
	if err = readDatasets(ctx, tx, providerID, &view); err == nil {
		err = readModels(ctx, tx, providerID, &view)
	}
	if err == nil {
		err = readCredentials(ctx, tx, providerID, &view)
	}
	if err == nil {
		err = readKeyCapabilities(ctx, tx, providerID, day, s.now(), &view)
	}
	if err == nil {
		err = readPreferredRoutes(ctx, tx, providerID, &view)
	}
	if err == nil {
		err = readCatalogDifferences(ctx, tx, providerID, &view)
		for i := range view.ModelCatalog {
			item := &view.ModelCatalog[i]
			switch {
			case item.Retired:
				item.StudentPrice = noStudentPriceRoute("")
			case item.StudentPrice.Status == "":
				item.StudentPrice = unpublishedStudentPrice()
			}
		}
		view.projectPriceRefreshErrors()
	}
	if err == nil {
		view.ProbeRun, err = readProbeRun(ctx, tx, providerID)
	}
	if err == nil {
		err = readComparison(ctx, tx, providerID, start, s.now(), &view)
	}
	if err == nil {
		view.ArchivedUsage, err = readArchivedUsage(ctx, tx, providerID, start)
	}
	if err == nil {
		view.WebsiteSync, err = readWebsiteSyncStatus(ctx, tx, providerID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return Overview{}, unavailable("读取北辰概览失败", err)
	}
	return view, nil
}

func readDatasets(ctx context.Context, tx *sql.Tx, providerID uuid.UUID, view *Overview) error {
	rows, err := tx.QueryContext(ctx, `
SELECT c.id, kinds.kind, d.last_success, d.last_error, trim_scale(m.multiplier)::text,
  m.group_id, trim_scale(m.group_rate)::text, m.peak_enabled, trim_scale(m.peak_rate)::text, m.observed_at,
  COALESCE(m.account_rate_source, ''),
  CASE WHEN kinds.kind <> 'billing' THEN ''
       WHEN m.id IS NOT NULL AND (m.stale_at IS NOT NULL OR d.last_error IS NOT NULL OR d.last_success < c.updated_at
         OR d.last_success <= statement_timestamp()-interval '16 minutes') THEN 'stale'
       WHEN m.id IS NULL OR d.last_success IS NULL THEN 'unverified'
       ELSE 'current' END,
  CASE WHEN kinds.kind = 'billing' THEN COALESCE(d.failure_count, 0) ELSE 0 END,
  d.website_group_observation, c.updated_at, COALESCE(k.upstream_key_id,0), k.group_id, k.group_name, trim_scale(k.rate)::text, inventory.last_seen
FROM provider_credentials c
CROSS JOIN (VALUES ('catalog'), ('billing'), ('prices'), ('usage')) AS kinds(kind)
LEFT JOIN beichen_datasets d ON d.credential_id = c.id AND d.kind = kinds.kind
LEFT JOIN beichen_website_keys k ON kinds.kind='billing' AND k.provider_id=c.provider_id AND k.credential_id=c.id
  AND k.ciphertext_digest=sha256(c.credential_ciphertext)
LEFT JOIN beichen_website_key_sync inventory ON inventory.provider_id=k.provider_id
LEFT JOIN LATERAL (
  SELECT id, multiplier, group_id, group_rate, peak_enabled, peak_rate, observed_at, stale_at, account_rate_source
  FROM beichen_multiplier_versions
  WHERE kinds.kind = 'billing' AND credential_id = c.id AND effective_at <= statement_timestamp()
  ORDER BY effective_at DESC, id DESC LIMIT 1
) m ON true
WHERE c.provider_id = $1
ORDER BY c.label, c.id, kinds.kind`, providerID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item Dataset
		var multiplier *string
		var rateSource string
		var observedGroup []byte
		var revision time.Time
		var inventory WebsiteKey
		if err := rows.Scan(&item.CredentialID, &item.Kind, &item.LastSuccess, &item.LastError, &multiplier,
			&item.GroupID, &item.GroupRate, &item.PeakEnabled, &item.PeakRate, &item.ObservedAt, &rateSource,
			&item.BillingState, &item.FailureCount, &observedGroup, &revision, &inventory.ID, &inventory.GroupID, &inventory.GroupName,
			&inventory.Rate, &inventory.LastSeen); err != nil {
			return err
		}
		versionGroup, versionRate := item.GroupID, item.GroupRate
		var evidence *billingAccountRateObservation
		if item.Kind == "billing" {
			evidence, err = item.applyBillingGroup(observedGroup, revision, inventory)
			if err != nil {
				return err
			}
		}
		item.OfficialPriceMissing = item.Kind == "prices" && item.LastError != nil && *item.LastError == errPanelEmpty.Error()
		item.Alert = item.FailureCount >= billingAlertFailures
		if multiplier != nil {
			value := json.Number(*multiplier)
			item.EffectiveMultiplier = &value
			item.EffectiveMultiplierText = *multiplier
			item.personalMultiplier = rateSource == keyBillingRateSource
			if item.BillingState == "current" && item.GroupStatus == "assigned" &&
				versionGroup != nil && item.GroupID != nil && *versionGroup == *item.GroupID &&
				versionRate != nil && item.GroupRate != nil && samePanelRate(json.Number(*versionRate), json.Number(*item.GroupRate)) {
				if rateSource == groupRateSource {
					rate := json.Number(*versionRate)
					item.AccountRate, item.AccountRateSource = &rate, rateSource
				} else if evidence != nil && evidence.GroupID == *versionGroup && evidence.Source == rateSource {
					switch {
					case rateSource == "" && websiteGroupRate(evidence.Rate) != nil:
						item.websiteAccountRate = &evidence.Rate
					case rateSource == keyBillingRateSource && item.confirmedKeyBinding != nil &&
						item.confirmedKeyBinding.KeyID == evidence.KeyID && item.confirmedKeyBinding.GroupID != nil &&
						*item.confirmedKeyBinding.GroupID == evidence.GroupID && samePanelRate(evidence.Rate, value):
						item.AccountRate, item.AccountRateSource, item.accountRateKeyID = &value, rateSource, evidence.KeyID
					}
				}
			}
		}
		if item.LastSuccess != nil {
			utc := item.LastSuccess.UTC()
			item.LastSuccess = &utc
		}
		if item.ObservedAt != nil {
			utc := item.ObservedAt.UTC()
			item.ObservedAt = &utc
		}
		view.Datasets = append(view.Datasets, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	billing := view.billingByCredential()
	for i := range view.Datasets {
		item := &view.Datasets[i]
		item.LastError = billing[item.CredentialID].groupReadError(item.LastError)
	}
	return nil
}

func (view *Overview) billingByCredential() map[uuid.UUID]Dataset {
	billing := make(map[uuid.UUID]Dataset)
	for _, dataset := range view.Datasets {
		if dataset.Kind == "billing" {
			billing[dataset.CredentialID] = dataset
		}
	}
	return billing
}

func readModels(ctx context.Context, tx *sql.Tx, providerID uuid.UUID, view *Overview) error {
	rows, err := tx.QueryContext(ctx, `
WITH `+catalog.BeichenKeyEvidenceCTE+`
SELECT k.credential_id, k.model_id::text, k.catalog_source, price.mode,
  COALESCE(route.school_status, named.status, 'not_configured'),
  owner.status = 'active' AND c.status = 'active' AND k.last_seen_at >= c.updated_at AND key_catalog.current AND `+catalog.BeichenGroupAllowsRouteSQL+` AND COALESCE(route.binding_ready, false),
  CASE WHEN version.source='derived_from_usage' AND price.upstream_input IS NOT NULL
        AND `+catalog.BeichenFollowPriceCurrentSQL+` THEN
       jsonb_strip_nulls(jsonb_build_object('billing_mode','token',
        'input_price',trim_scale(version.input_rate * 0.000000000001::numeric),
        'output_price',trim_scale(version.output_rate * 0.000000000001::numeric),
        'cache_read_price',trim_scale(version.cache_read_rate * 0.000000000001::numeric),
        'cache_write_price',trim_scale(version.cache_write_rate * 0.000000000001::numeric)))
       WHEN `+catalog.BeichenPanelQuoteCurrentSQL+`
       THEN snap.model_pricing -> (k.model_id::text) END,
  COALESCE(version.source,''),COALESCE(version.observed_at, CASE WHEN `+catalog.BeichenPanelQuoteCurrentSQL+` THEN snap.effective_at END),
  CASE WHEN `+catalog.BeichenPanelQuoteCurrentSQL+` THEN trim_scale(quote_rate.multiplier)::text END,
  CASE WHEN follow.current THEN trim_scale(price.upstream_input)::text END,
  CASE WHEN follow.current THEN trim_scale(price.upstream_output)::text END,
  CASE WHEN follow.current THEN trim_scale(price.upstream_cache_read)::text END,
  CASE WHEN follow.current THEN trim_scale(price.upstream_cache_write)::text END,
  CASE WHEN follow.current OR price.mode='manual' THEN trim_scale(price.school_input * f.factor)::text END,
  CASE WHEN follow.current OR price.mode='manual' THEN trim_scale(price.school_output * f.factor)::text END,
  CASE WHEN follow.current OR price.mode='manual' THEN trim_scale(price.school_cache_read * f.factor)::text END,
  CASE WHEN follow.current OR price.mode='manual' THEN trim_scale(price.school_cache_write * f.factor)::text END,
  owner.status = 'active' AND c.status = 'active', k.last_seen_at >= c.updated_at,
  key_catalog.current, k.raw_entry, to_json(k.supported_protocols),
  probe.checked_at, COALESCE(probe.passed, false), COALESCE(probe.credential_rejected, false),
  COALESCE(`+catalog.BeichenFixedPriceSQL+`,false)
FROM provider_model_catalog k
JOIN provider_credentials c ON c.id = k.credential_id AND c.provider_id = k.provider_id
JOIN providers owner ON owner.id = c.provider_id
JOIN beichen_key_evidence e ON e.credential_id=c.id
CROSS JOIN LATERAL (SELECT `+catalog.BeichenKeyListsModelSQL+` AS current) key_catalog
LEFT JOIN models named ON named.model_id = k.model_id
LEFT JOIN LATERAL (
  SELECT effective_at,expires_at,credential_revision,credential_ciphertext_sha256,group_id,model_pricing,multiplier_version FROM beichen_panel_snapshots
  WHERE credential_id = c.id AND effective_at <= statement_timestamp() ORDER BY effective_at DESC LIMIT 1
) snap ON true
LEFT JOIN beichen_multiplier_versions quote_rate ON quote_rate.id=snap.multiplier_version
  AND quote_rate.credential_id=c.id AND quote_rate.effective_at<=snap.effective_at
LEFT JOIN LATERAL (
  SELECT school.status AS school_status, b.status = 'active' AS binding_ready
  FROM model_bindings b JOIN models school ON school.id = b.model_id
  WHERE b.provider_id = k.provider_id AND b.credential_id = k.credential_id
    AND lower(btrim(b.upstream_model_name)) = lower(k.model_id::text)
  ORDER BY (b.status = 'active') DESC, (school.status = 'active') DESC,
    (school.status = 'pending_configuration') DESC, b.priority, b.id
  LIMIT 1
) route ON true
LEFT JOIN LATERAL (
  SELECT max(r.checked_at) AS checked_at, bool_or(r.error_code IS NULL) AS passed,
    bool_and(r.status_code = 401 OR r.error_code IS NOT DISTINCT FROM 'credential_unreadable') AS credential_rejected
  FROM upstream_probes r
  WHERE owner.status = 'active' AND c.status = 'active'
    AND r.provider_id = k.provider_id AND r.credential_id = k.credential_id AND r.model = k.model_id::text
    AND `+catalog.BeichenCurrentProbeSQL+` AND key_catalog.current AND `+catalog.BeichenGroupAllowsRouteSQL+`
    AND (cardinality(k.supported_protocols) = 0 OR r.protocol = ANY(k.supported_protocols))
) probe ON true
CROSS JOIN LATERAL beichen_price_at(k.credential_id, k.model_id::text, statement_timestamp()) price
LEFT JOIN beichen_price_versions version ON version.id=price.upstream_version
CROSS JOIN LATERAL (SELECT `+catalog.BeichenFollowPriceCurrentSQL+` AS current) follow
CROSS JOIN LATERAL (SELECT CASE WHEN price.mode = 'follow' THEN 100 ELSE 1 END AS factor) f
WHERE k.provider_id = $1 AND k.status = 'active'
ORDER BY k.credential_id, k.model_id`, providerID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var model Model
		var upstream, school DisplayRates
		var terms, rawEntry, protocols []byte
		var passed, fixedPrice bool
		err := rows.Scan(&model.CredentialID, &model.Model, &model.CatalogSource, &model.Mode,
			&model.SchoolStatus, &model.RouteReady, &terms, &model.PriceSource, &model.PriceObservedAt, &model.QuoteEffectiveMultiplier,
			&upstream.Input, &upstream.Output, &upstream.CacheRead, &upstream.CacheWrite,
			&school.Input, &school.Output, &school.CacheRead, &school.CacheWrite, &model.available, &model.currentKey, &model.catalogCurrent, &rawEntry, &protocols,
			&model.lastProbeAt, &passed, &model.credentialRejected, &fixedPrice)
		if err != nil {
			return err
		}
		entry, err := storedCatalogEntry(model.Model, rawEntry, protocols)
		if err != nil {
			return err
		}
		model.protocols = catalog.RegisteredProtocols(entry)
		model.probeState = "unprobed"
		if !model.catalogCurrent {
			model.lastProbeAt = nil
		}
		if model.lastProbeAt != nil {
			at := model.lastProbeAt.UTC()
			model.lastProbeAt = &at
			model.probeState = "failed"
			if passed {
				model.probeState = "passed"
			}
		}
		model.PriceStatus = "pending_confirmation"
		if bytes.Equal(terms, []byte("null")) {
			model.PriceStatus = "not_listed"
			terms = nil
		}
		model.PriceConditions = terms
		if len(terms) > 0 {
			decoder := json.NewDecoder(bytes.NewReader(usage.BeichenPanelDisplayTerms(terms)))
			decoder.UseNumber()
			if err := decoder.Decode(&model.PriceConditionsExact); err != nil {
				return err
			}
			stringifyPriceNumbers(model.PriceConditionsExact)
		}
		model.priceReadiness = routePriceReadiness(fixedPrice, terms)
		usableConditions := model.priceReadiness.ready() && model.priceReadiness.conditional && model.PriceSource != derivedPriceSource
		if upstream.Input != nil && upstream.Output != nil {
			model.PriceStatus = "confirmed"
			if model.PriceSource == derivedPriceSource {
				model.PriceStatus = "derived"
			}
			model.UpstreamRates = &upstream
		} else if model.priceReadiness.conditional || fixedPrice && conditionalPanelPricing(terms) {
			model.PriceStatus = "conditional"
		}
		if !usableConditions && (school.Input != nil || school.Output != nil) {
			model.SchoolRates = &school
			model.SchoolRatesIncludeEffectiveMultiplier = true
		}
		model.PriceCovered = model.priceReadiness.ready()
		view.Models = append(view.Models, model)
	}
	return rows.Err()
}

func readCredentials(ctx context.Context, tx *sql.Tx, providerID uuid.UUID, view *Overview) error {
	keyModels := make([]Model, 0, len(view.Models))
	for _, row := range view.Models {
		if beichenKeyCatalogSource(row.CatalogSource) {
			keyModels = append(keyModels, row)
		}
	}
	counts := credentialModelCounts(keyModels)
	billing := view.billingByCredential()
	datasetErrors := make(map[uuid.UUID]map[string]*string)
	for _, data := range view.Datasets {
		if datasetErrors[data.CredentialID] == nil {
			datasetErrors[data.CredentialID] = make(map[string]*string)
		}
		datasetErrors[data.CredentialID][data.Kind] = data.LastError
	}

	// The datasets store retry deadlines, not failure times. Undo the usual
	// retry interval when comparing errors from usage and the billing job.
	// Catalog and price errors can be written as part of that billing job.
	rows, err := tx.QueryContext(ctx, `
SELECT c.id, c.label::text, c.status, s.catalog_at, s.prices_at, s.billing_at, s.usage_at, s.last_error,
  p.status = 'active' AND c.status = 'active', octet_length(c.credential_ciphertext) > 0, c.updated_at,
  (CASE WHEN EXISTS (SELECT 1 FROM beichen_provision_channels school WHERE school.credential_id=c.id)
    THEN 1.10 ELSE 1 END)::text
FROM provider_credentials c JOIN providers p ON p.id = c.provider_id
LEFT JOIN beichen_datasets b ON b.credential_id = c.id AND b.kind = 'billing'
CROSS JOIN LATERAL (
  SELECT max(d.last_success) FILTER (WHERE d.kind = 'catalog') AS catalog_at,
    max(d.last_success) FILTER (WHERE d.kind = 'prices') AS prices_at,
    max(d.last_success) FILTER (WHERE d.kind = 'billing') AS billing_at,
    max(d.last_success) FILTER (WHERE d.kind = 'key_usage') AS usage_at,
    (array_agg(d.last_error ORDER BY GREATEST(d.last_success,
      CASE WHEN d.kind = 'key_usage' AND d.last_error IS NOT NULL THEN
             d.next_attempt - make_interval(mins => 1 << greatest(d.failure_count - 1, 0))
           WHEN b.last_error IS NOT NULL THEN
             b.next_attempt - make_interval(mins => 1 << greatest(b.failure_count - 1, 0))
           ELSE b.last_success END) DESC NULLS LAST, d.kind)
      FILTER (WHERE d.last_error IS NOT NULL))[1] AS last_error
  FROM beichen_datasets d WHERE d.credential_id = c.id AND d.kind <> 'usage'
) s
WHERE c.provider_id = $1
ORDER BY c.label, c.id`, providerID)
	if err != nil {
		return err
	}
	defer rows.Close()
	view.Credentials = []CredentialSummary{}
	view.SchoolMultipliers = make(map[uuid.UUID]string)
	for rows.Next() {
		var item CredentialSummary
		var enabled, configured bool
		var revision time.Time
		var schoolMultiplier string
		times := []**time.Time{&item.CatalogLastSuccess, &item.PricesLastSuccess, &item.BillingLastSuccess, &item.UsageLastSuccess}
		if err := rows.Scan(&item.CredentialID, &item.Label, &item.Status, times[0], times[1], times[2], times[3], &item.LastError,
			&enabled, &configured, &revision, &schoolMultiplier); err != nil {
			return err
		}
		view.SchoolMultipliers[item.CredentialID] = schoolMultiplier
		item.LastError = billing[item.CredentialID].groupReadError(item.LastError)
		for _, timestamp := range times {
			if *timestamp != nil {
				utc := (*timestamp).UTC()
				*timestamp = &utc
			}
		}
		count := counts[item.CredentialID]
		item.ModelCount, item.PricedModelCount = count.total, count.priced
		item.Health = buildCredentialHealth(item, count, enabled, configured, revision, datasetErrors[item.CredentialID])
		view.Credentials = append(view.Credentials, item)
	}
	return rows.Err()
}

// Rates and tier boundaries can exceed JavaScript's numeric precision.
func stringifyPriceNumbers(value any) any {
	switch v := value.(type) {
	case json.Number:
		return v.String()
	case map[string]any:
		for k, child := range v {
			v[k] = stringifyPriceNumbers(child)
		}
	case []any:
		for i, child := range v {
			v[i] = stringifyPriceNumbers(child)
		}
	}
	return value
}

func readPreferredRoutes(ctx context.Context, tx *sql.Tx, providerID uuid.UUID, view *Overview) error {
	models, err := loadActivationCatalog(ctx, tx, providerID, "")
	if err != nil {
		return err
	}
	snapshot, err := catalog.ReadBeichenCapabilitySnapshot(ctx, tx, providerID)
	if err != nil {
		return err
	}
	capabilities := make(map[string][]catalog.ModelCapability)
	for _, row := range snapshot.Capabilities {
		name := strings.ToLower(row.Model)
		capabilities[name] = append(capabilities[name], row)
	}
	records := make(map[string]map[uuid.UUID]*Model)
	for i := range view.Models {
		row := &view.Models[i]
		name := strings.ToLower(row.Model)
		row.RouteReady, row.PriceCovered = false, false
		row.StudentPrice = unpublishedStudentPrice()
		row.schoolMultiplier = view.SchoolMultipliers[row.CredentialID]
		for _, route := range capabilities[name] {
			if route.CredentialID != nil && *route.CredentialID == row.CredentialID {
				row.RouteReady, row.PriceCovered = route.BindingState == "ready", route.PriceCovered
				row.QuoteUsable = route.PriceCovered && row.QuoteEffectiveMultiplier != nil &&
					(route.PriceState == "conditional" && row.priceReadiness.conditional && row.priceReadiness.ready() ||
						route.PriceState == "confirmed" && row.PriceSource == "panel")
				row.StudentPrice = studentPriceForRoute(row, route, route.Published, row.RouteReady)
				break
			}
		}
		if records[name] == nil {
			records[name] = make(map[uuid.UUID]*Model)
		}
		records[name][row.CredentialID] = row
	}
	syncFailed := make(map[uuid.UUID]bool)
	for _, d := range view.Datasets {
		if d.Kind != "usage" && d.LastError != nil && !d.OfficialPriceMissing {
			syncFailed[d.CredentialID] = true
		}
	}
	view.ModelCatalog = make([]CatalogModel, 0, len(models))
	for _, model := range models {
		modelCapabilities := capabilities[strings.ToLower(model.name)]
		for _, catalogView := range model.catalogViews() {
			item, err := catalogView.overviewWithStatus(ctx, tx, records, syncFailed,
				bindingsForCatalogView(modelCapabilities, catalogView))
			if err != nil {
				return err
			}
			view.ModelCatalog = append(view.ModelCatalog, item)
		}
	}
	markRetiredCatalogModels(view, snapshot.RetiredModels)
	return nil
}

// The console uses the prices dataset error to gate price calculations.
// Report renewal failures separately while a published route has a usable quote.
func (view *Overview) projectPriceRefreshErrors() {
	usableQuotes := make(map[uuid.UUID]bool)
	for _, model := range view.Models {
		if model.RouteReady && model.QuoteUsable {
			usableQuotes[model.CredentialID] = true
		}
	}
	for i := range view.Datasets {
		dataset := &view.Datasets[i]
		if dataset.Kind == "prices" && dataset.LastError != nil && usableQuotes[dataset.CredentialID] {
			dataset.RefreshError, dataset.LastError = dataset.LastError, nil
		}
	}
}

// Each configured school model is a separate catalog identity. The activation
// model remains grouped by upstream name for preview and publication.
func bindingsForCatalogView(rows []catalog.ModelCapability, view *activationModel) []catalog.ModelCapability {
	if len(view.schools) != 1 {
		return rows
	}
	var school string
	for _, item := range view.schools {
		school = item.name
	}
	filtered := slices.Clone(rows)
	for i := range filtered {
		filtered[i].Bindings = slices.DeleteFunc(slices.Clone(filtered[i].Bindings), func(binding catalog.BeichenBinding) bool {
			return !strings.EqualFold(strings.TrimSpace(binding.SchoolModel), strings.TrimSpace(school))
		})
	}
	return filtered
}

func (m *activationModel) overviewWithStatus(ctx context.Context, tx *sql.Tx,
	records map[string]map[uuid.UUID]*Model, syncFailed map[uuid.UUID]bool, capabilities []catalog.ModelCapability,
) (CatalogModel, error) {
	target := m.target("")
	var plan activationPlan
	if target != nil {
		plan = m.plan(target)
	}
	item := m.overview(target, plan)
	item.Blockers = []CatalogBlocker{}
	item.Protocols = []string{}
	item.BoundRoutes = []CatalogBoundRoute{}
	for _, school := range m.schools {
		item.Published = item.Published || school.status == "active"
	}
	priceStates := item.applyRouteEvidence(capabilities, records)
	switch {
	case len(priceStates) > 1:
		item.PriceState = "mixed"
	case len(priceStates) == 1:
		for state := range priceStates {
			item.PriceState = state
		}
	case item.BoundCatalogPending:
		item.PriceState = "unverified"
	default:
		item.PriceState = "missing"
		if slices.ContainsFunc(capabilities, func(row catalog.ModelCapability) bool {
			return row.CatalogState == "catalog_stale" || row.CatalogState == "catalog_unverified"
		}) {
			item.PriceState = "unverified"
		}
	}

	if item.ReadyCount > 0 {
		item.Activation = "active"
		if m.aliasConflict {
			item.CanEnable = false
			item.Activation = "blocked"
			item.Blockers = appendCatalogBlocker(item.Blockers, CatalogBlocker{blockerMultiSchool, multiSchoolMessage})
		}
		if plan.blocker.Code == blockerAuthFailed {
			item.Blockers = append(item.Blockers, plan.blocker)
		}
		return item, nil
	}
	var price activationPrice
	if item.CanEnable {
		var err error
		price, err = plan.priceReadiness(ctx, tx, records)
		if err != nil {
			return CatalogModel{}, err
		}
		if price.ready() && !m.aliasConflict {
			item.Activation = "ready"
			return item, nil
		}
	}

	item.Activation = "blocked"
	if m.aliasConflict {
		item.CanEnable = false
	}
	if target != nil && target.status == "not_configured" {
		item.Blockers = append(item.Blockers, CatalogBlocker{"not_configured", "请确认此目录模型对应的校内模型和线路"})
	}
	if !item.CanEnable && (m.aliasConflict || m.multiSchoolTargets) {
		item.Blockers = append(item.Blockers, CatalogBlocker{blockerMultiSchool, multiSchoolMessage})
	}
	if !item.CanEnable {
		blocker := plan.blocker
		if blocker.Code == "" {
			blocker = m.targetBlocker("")
		}
		if (m.aliasConflict || m.multiSchoolTargets) && blocker.Code == blockerNoCandidate {
			blocker = CatalogBlocker{blockerMultiSchool, multiSchoolMessage}
		}
		item.Blockers = appendCatalogBlocker(item.Blockers, blocker)
	}
	if price.blocker != nil {
		if item.PriceBlocker == nil || item.PriceBlocker.Code != "price_snapshot_expired" || price.blocker.Code != "missing_price" {
			item.PriceBlocker = price.blocker
		}
		code := blockerPriceMissing
		if price.conditional {
			code = "conditional_price"
		}
		item.Blockers = appendCatalogBlocker(item.Blockers, CatalogBlocker{code, item.PriceBlocker.Message})
	}
	for _, id := range m.keys {
		if syncFailed[id] {
			item.Blockers = append(item.Blockers, CatalogBlocker{"sync_failed", "关联渠道的目录、账单倍率或价格同步失败，请查看渠道错误"})
			break
		}
	}
	return item, nil
}

func appendCatalogBlocker(blockers []CatalogBlocker, blocker CatalogBlocker) []CatalogBlocker {
	if blocker.Code == "" || slices.ContainsFunc(blockers, func(existing CatalogBlocker) bool {
		return existing.Code == blocker.Code
	}) {
		return blockers
	}
	return append(blockers, blocker)
}

func (item *CatalogModel) applyRouteEvidence(rows []catalog.ModelCapability,
	records map[string]map[uuid.UUID]*Model) map[string]bool {
	type routeKey struct {
		credential uuid.UUID
		protocol   string
	}
	candidates, ready := make(map[routeKey]bool), make(map[routeKey]bool)
	bound, covered := make(map[uuid.UUID]bool), make(map[uuid.UUID]bool)
	var publishedProtocols, candidateProtocols []catalog.ProtocolCapability
	priceStates := make(map[string]bool)
	invalidBinding, groupDenied, groupPending := false, false, false
	item.StudentPrice = noStudentPriceRoute("")
	var pricePriority int
	for _, row := range rows {
		current := row.CatalogState == "key_listed" && row.KeyEnabled
		if current {
			for _, option := range row.ProtocolDetails {
				candidates[routeKey{*row.CredentialID, option.Protocol}] = true
				candidateProtocols = append(candidateProtocols, option)
			}
		}
		priceBasis := !item.Published && len(row.Bindings) > 0 && current
		for _, b := range row.Bindings {
			var record *Model
			if row.CredentialID != nil {
				record = records[strings.ToLower(row.Model)][*row.CredentialID]
			}
			studentPrice := studentPriceForRoute(record, row, b.Published, b.BindingReady)
			item.BoundRoutes = append(item.BoundRoutes, CatalogBoundRoute{
				BeichenBinding: b, UpstreamModel: row.Model, CredentialID: row.CredentialID, CredentialLabel: row.CredentialLabel,
				GroupID: row.GroupID, GroupStatus: row.GroupStatus, GroupCheckedAt: row.GroupCheckedAt, KeyEnabled: row.KeyEnabled,
				CatalogState: row.CatalogState, PriceState: row.PriceState, PriceCovered: row.PriceCovered, StudentPrice: studentPrice,
			})
			if row.CredentialID != nil && row.KeyEnabled {
				bound[*row.CredentialID] = true
				if current {
					covered[*row.CredentialID] = true
				}
				item.BoundCatalogPending = item.BoundCatalogPending || row.CatalogState == "catalog_stale" ||
					row.CatalogState == "catalog_unverified" || row.GroupStatus == "pending"
			}
			invalidBinding = invalidBinding || b.Published && !b.BindingReady
			groupDenied = groupDenied || b.Published && row.KeyEnabled && row.GroupStatus == "not_allowed"
			groupPending = groupPending || b.Published && row.KeyEnabled && !b.BindingReady &&
				(row.GroupStatus == "pending" || row.GroupStatus == "not_found")
			if b.BindingReady {
				rank, currentRank := studentPrice.rank(), item.StudentPrice.rank()
				if rank > currentRank || rank == currentRank && b.Priority < pricePriority {
					item.StudentPrice, pricePriority = studentPrice, b.Priority
				}
				key := routeKey{*row.CredentialID, b.Protocol}
				ready[key], candidates[key], priceBasis = true, true, true
				publishedProtocols = append(publishedProtocols, b.ProtocolCapability)
			}
		}
		if priceBasis {
			item.addPriceEvidence(row, priceStates)
		}
	}
	item.BoundKeyCount, item.CoveredBoundKeyCount = len(bound), len(covered)
	item.RouteCount, item.ReadyCount = len(candidates), len(ready)
	item.Active = item.Published && item.ReadyCount > 0

	if item.Published {
		item.ModelAvailability = catalog.SummarizeModelAvailability(item.Model, "active", publishedProtocols)
		if invalidBinding || item.ReadyCount == 0 {
			item.BindingIssue = &CatalogBlocker{"no_ready_binding", "请检查已启用的北辰线路和绑定 Key。"}
			switch {
			case groupDenied && item.ReadyCount == 0:
				item.BindingIssue = &CatalogBlocker{"group_not_allowed", "绑定 Key 分组无权限。"}
				item.ModelAvailability.AvailabilityReason = item.BindingIssue.Message
				item.ActionHint = "请在北辰恢复该 Key 的分组权限，再重扫目录。"
			case item.BoundCatalogPending && item.ReadyCount == 0:
				item.BindingIssue = &CatalogBlocker{"catalog_stale", "绑定 Key 的目录或分组待复核，请重扫目录并重新探活。"}
				item.ModelAvailability.Availability = catalog.AvailabilityUnverified
				item.ModelAvailability.AvailabilityReason = item.BindingIssue.Message
			case item.ReadyCount > 0:
				item.BindingIssue.Message = "部分已发布线路需要检查绑定 Key 和目录。"
				if groupDenied || groupPending {
					item.BindingIssue = &CatalogBlocker{"partial_group_check", "部分线路的绑定 Key 分组待核对。"}
				}
			case len(candidates) == 0:
				item.BindingIssue = &CatalogBlocker{"catalog_missing", "学校已启用该模型，当前 Key 目录里已找不到它。请同步目录并核查供货渠道。"}
			}
		}
		if item.ReadyCount == 0 && item.BindingIssue != nil {
			item.StudentPrice.Reason = item.BindingIssue.Message
		}
	} else {
		item.StudentPrice = unpublishedStudentPrice()
		item.ModelAvailability = catalog.SummarizeModelAvailability(item.Model, "active", candidateProtocols)
		item.ModelAvailability.Availability = "not_published"
		item.ModelAvailability.AvailabilityReason = ""
		if len(item.BoundRoutes) == 0 {
			for _, row := range rows {
				if row.KeyEnabled && row.CatalogState == "key_listed" {
					item.addPriceEvidence(row, priceStates)
				}
			}
		}
	}
	item.ProbeState, item.LastProbeAt = item.ModelAvailability.ProbeState, item.ModelAvailability.LastProbeAt
	if item.ProbeState == "not_applicable" {
		item.ProbeState = "unprobed"
	}
	switch {
	case !item.PriceCovered && item.PriceBlocker != nil:
		item.ModelAvailability.Availability = catalog.AvailabilityUnavailable
		item.ModelAvailability.AvailabilityReason = item.PriceBlocker.Message
		item.ModelAvailability.RecommendedProtocol = nil
		item.ActionHint = "请同步相关 Key 的官网报价，或设置手动价。"
	case item.ModelAvailability.Availability == catalog.AvailabilityAvailable:
		item.ActionHint = "可使用推荐协议接入。"
	case item.ReadyCount > 0 && item.ProbeState == "unprobed":
		item.ActionHint = "发起探活以确认已发布协议。"
	}
	return priceStates
}

func unpublishedStudentPrice() StudentPrice {
	return StudentPrice{studentPriceNoRoute, "老师尚未发布该模型"}
}

func noStudentPriceRoute(groupStatus string) StudentPrice {
	switch groupStatus {
	case "not_allowed":
		return StudentPrice{studentPriceNoRoute, "该线路的绑定 Key 分组无权限"}
	case "not_found":
		return StudentPrice{studentPriceNoRoute, "该线路的绑定 Key 分组不存在"}
	case "pending":
		return StudentPrice{studentPriceNoRoute, "该线路的绑定 Key 分组待核对"}
	default:
		return StudentPrice{studentPriceNoRoute, "没有已就绪的发布线路"}
	}
}

func (price StudentPrice) rank() int {
	switch price.Status {
	case studentPriceNormal, studentPriceManual:
		return 4
	case studentPriceHigher:
		return 3
	case studentPriceRatePending:
		return 2
	case studentPriceNoBase:
		return 1
	}
	return 0
}

func studentPriceForRoute(model *Model, route catalog.ModelCapability, published, ready bool) StudentPrice {
	if !published {
		return unpublishedStudentPrice()
	}
	if !ready {
		return noStudentPriceRoute(route.GroupStatus)
	}
	if model != nil && route.PriceCovered {
		if model.schoolMultiplier == "" {
			return StudentPrice{studentPriceRatePending, "当前线路的学校系数待同步"}
		}
		fixed := model.SchoolRates != nil && model.SchoolRates.Input != nil && model.SchoolRates.Output != nil
		switch route.PriceState {
		case "manual":
			if fixed {
				return StudentPrice{studentPriceManual, "使用教师设置的基础单价和学校系数"}
			}
		case "conditional":
			if !model.QuoteUsable {
				return StudentPrice{studentPriceRatePending, "该条件价缺少同一报价快照的生效倍率"}
			}
			if onlyHigherPriceTiers(model.PriceConditions, model.PriceConditionsExact) {
				return StudentPrice{studentPriceHigher, "官网只提供较高输入 Token 档位的单价"}
			}
			return StudentPrice{studentPriceNormal, "使用当前线路的官网条件价"}
		case "confirmed":
			if fixed {
				return StudentPrice{studentPriceNormal, "使用当前线路已应用生效倍率的单价"}
			}
		}
	}
	if route.PriceBlocker != nil && (route.PriceBlocker.Code != "missing_price" || route.MultiplierState == "current") {
		return StudentPrice{studentPriceNoBase, route.PriceBlocker.Message}
	}
	if route.MultiplierState != "current" {
		return StudentPrice{studentPriceRatePending, "当前线路的生效倍率待核对"}
	}
	return StudentPrice{studentPriceNoBase, "当前线路缺少可用的基础单价"}
}

func onlyHigherPriceTiers(raw json.RawMessage, exact map[string]any) bool {
	intervals, _ := exact["intervals"].([]any)
	if len(intervals) == 0 {
		return false
	}
	for _, interval := range intervals {
		tier, _ := interval.(map[string]any)
		if tier["min_tokens"] == "0" {
			return false
		}
	}
	// Multiplier intervals have a base quote outside their ranges. The
	// normalized terms replace those multipliers with absolute tier prices.
	var terms struct {
		Intervals []map[string]json.RawMessage `json:"intervals"`
	}
	_ = json.Unmarshal(raw, &terms)
	for _, tier := range terms.Intervals {
		for _, field := range []string{"input_multiplier", "output_multiplier", "cache_read_multiplier", "cache_write_multiplier"} {
			if value := tier[field]; len(value) > 0 && !bytes.Equal(value, []byte("null")) {
				return false
			}
		}
	}
	return true
}

func (item *CatalogModel) addPriceEvidence(row catalog.ModelCapability, states map[string]bool) {
	states[row.PriceState] = true
	item.PriceCovered = item.PriceCovered || row.PriceCovered
	if item.PriceBlocker == nil && row.PriceBlocker != nil {
		item.PriceBlocker = &CatalogBlocker{Code: row.PriceBlocker.Code, Message: row.PriceBlocker.Message}
	}
}

func readComparison(ctx context.Context, tx *sql.Tx, providerID uuid.UUID, start, now time.Time, view *Overview) error {
	end := start.AddDate(0, 0, 1)
	rows, err := tx.QueryContext(ctx, comparisonSQL, providerID, start, end,
		start.Format(dayLayout), end.Format(dayLayout), now.In(beijing).Format(dayLayout), now.UTC())
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item Comparison
		err := rows.Scan(&item.CredentialID, &item.Model, &item.LocalRequests, &item.UpstreamRequests,
			&item.LocalInputTokens, &item.UpstreamInputTokens, &item.LocalOutputTokens, &item.UpstreamOutputTokens,
			&item.LocalCacheReadTokens, &item.UpstreamCacheReadTokens, &item.LocalCacheWriteTokens, &item.UpstreamCacheWriteTokens,
			&item.EstimatedUpstreamMicros, &item.BilledUpstreamMicros, &item.TrialCreditMilli, &item.TrialUnpricedCalls,
			&item.UpstreamUsageSource, &item.TrialCredits, &item.ChargedCredits, &item.PendingReviewCalls)
		if err != nil {
			return err
		}
		view.Comparison = append(view.Comparison, item)
	}
	return rows.Err()
}

// 日对账、区间对账和学校用量共用归属及缓存归一化，避免把 OpenAI 的缓存输入重复计数。
const attributedColumnsSQL = `a.credential_id, a.model, a.sent_at, c.beichen_state, c.beichen_credit_exact,
    CASE WHEN c.protocol = 'anthropic' THEN c.prompt_tokens::bigint
         WHEN c.protocol = 'google_gemini_v1beta' AND cache.reads <= c.prompt_tokens
           THEN (c.prompt_tokens::bigint - cache.reads)::numeric + COALESCE(c.tool_prompt_tokens,0)
         WHEN cache.reads::numeric + cache.writes <= c.prompt_tokens
           AND (c.cache_miss_tokens IS NULL OR c.cache_miss_tokens::numeric + cache.reads + cache.writes = c.prompt_tokens)
           THEN c.prompt_tokens::bigint - cache.reads - cache.writes
         WHEN (cache.reads IS NULL OR cache.writes IS NULL) AND (c.prompt_tokens IS NULL OR
           c.cache_miss_tokens::numeric + COALESCE(cache.reads,0) + COALESCE(cache.writes,0) <= c.prompt_tokens)
           THEN c.cache_miss_tokens
    END AS input_tokens,
    CASE WHEN c.protocol='google_gemini_v1beta' AND num_nonnulls(c.completion_tokens,c.reasoning_tokens)>0
         THEN COALESCE(c.completion_tokens,0)::numeric + COALESCE(c.reasoning_tokens,0)
         ELSE c.completion_tokens::numeric END AS output_tokens,
    cache.reads AS cache_read_tokens, cache.writes AS cache_write_tokens`

const attributedFromSQL = `
  FROM gateway_calls c
  CROSS JOIN LATERAL (SELECT
    COALESCE(c.cache_hit_tokens, CASE WHEN COALESCE(c.usage_reason,'response_interrupted')='response_interrupted'
      AND c.protocol IN ('openai_compatible','openai_responses','openai_realtime')
      AND c.cache_miss_tokens::numeric + c.cache_write_tokens = c.prompt_tokens THEN 0::bigint END) AS reads,
    COALESCE(c.cache_write_tokens, CASE WHEN COALESCE(c.usage_reason,'response_interrupted')='response_interrupted'
      AND c.protocol IN ('openai_compatible','openai_responses','openai_realtime')
      AND c.cache_miss_tokens::numeric + c.cache_hit_tokens = c.prompt_tokens THEN 0::bigint END) AS writes
  ) cache
  CROSS JOIN LATERAL (SELECT
    CASE WHEN c.beichen_dispatch_kind = 'beichen' THEN c.beichen_dispatch_credential_id ELSE c.provider_credential_id END AS credential_id,
    CASE WHEN c.beichen_dispatch_kind = 'beichen' THEN c.beichen_dispatch_model ELSE c.upstream_model_name END AS model,
    CASE WHEN c.beichen_dispatch_kind = 'beichen' THEN COALESCE(c.beichen_price_at, c.beichen_dispatch_at) ELSE c.sent_at END AS sent_at
  ) a
  WHERE ($1::uuid IS NULL OR c.provider_id = $1 OR (c.provider_id IS NULL AND c.beichen_dispatch_kind = 'beichen' AND EXISTS (
    SELECT 1 FROM provider_credentials pc WHERE pc.id = a.credential_id AND pc.provider_id = $1
  ))) AND ($2::timestamptz IS NULL OR a.sent_at >= $2) AND a.sent_at < $3::timestamptz
    AND a.credential_id IS NOT NULL AND a.model <> ''
    AND (c.beichen_dispatch_kind = 'beichen' OR c.binding_id IS NOT NULL) AND c.outcome <> 'pending'`

const comparisonSQL = `
WITH attributed AS (
  SELECT ` + attributedColumnsSQL + `, c.usage_state` + attributedFromSQL + `
), priced_calls AS (
  SELECT a.*, COALESCE(alias.school_model, a.model) AS school_model,
    CASE WHEN usage_state = 'reported' AND cache_read_tokens IS NOT NULL AND cache_write_tokens IS NOT NULL THEN
      beichen_weighted_tokens(input_tokens::bigint, output_tokens::bigint, cache_read_tokens, cache_write_tokens,
      p.upstream_input, p.upstream_output, p.upstream_cache_read, p.upstream_cache_write) END AS estimated_weight,
    CASE WHEN usage_state = 'reported' AND cache_read_tokens IS NOT NULL AND cache_write_tokens IS NOT NULL THEN
      beichen_weighted_tokens(input_tokens::bigint, output_tokens::bigint, cache_read_tokens, cache_write_tokens,
      p.school_input, p.school_output, p.school_cache_read, p.school_cache_write)
      * CASE WHEN p.mode = 'follow' THEN 100 ELSE 1 END END AS trial_weight
  FROM attributed a ` + joinModelAliasSQL + `
  CROSS JOIN LATERAL beichen_price_at(a.credential_id, a.model, a.sent_at) p
), local AS (
  SELECT credential_id, school_model AS model, count(*) AS requests,
    CASE WHEN count(input_tokens) = count(*) THEN sum(input_tokens) END AS input_tokens,
    CASE WHEN count(output_tokens) = count(*) THEN sum(output_tokens) END AS output_tokens,
    CASE WHEN count(cache_read_tokens) = count(*) THEN sum(cache_read_tokens) END AS cache_read_tokens,
    CASE WHEN count(cache_write_tokens) = count(*) THEN sum(cache_write_tokens) END AS cache_write_tokens,
    CASE WHEN count(estimated_weight) = count(*) THEN round(sum(estimated_weight) / 1000000) END AS estimated_micros,
    round(sum(trial_weight) / 1000000000) AS trial_credit_milli,
    trim_scale(sum(trial_weight) * 0.000000000001::numeric)::text AS trial_credits,
    trim_scale(sum(beichen_credit_exact) FILTER (WHERE beichen_state = 'charged'))::text AS charged_credits,
    count(*) FILTER (WHERE beichen_state = 'pending_review') AS pending_review_calls,
    count(*) FILTER (WHERE trial_weight IS NULL) AS unpriced_calls
  FROM priced_calls
  GROUP BY credential_id, school_model
), usage_scope AS (
  SELECT $1::uuid AS provider_id,$4::text::date AS first_day,$5::text::date AS end_day,
    $6::text::date AS today,$7::timestamptz AS as_of
)` + usage.BeichenDailyEvidenceSQL + `,
upstream_rows AS (
  SELECT a.*, COALESCE(alias.school_model, a.model) AS school_model,
    CASE WHEN a.source IN ('key_usage','website_receipts') THEN 'usage' ELSE a.source END AS comparison_source
  FROM model_usage_rows a ` + joinModelAliasSQL + `
), upstream AS (
  SELECT credential_id, school_model AS model,
    CASE WHEN count(requests) = count(*) THEN sum(requests) END AS requests,
    CASE WHEN count(input_tokens) = count(*) THEN sum(input_tokens) END AS input_tokens,
    CASE WHEN count(output_tokens) = count(*) THEN sum(output_tokens) END AS output_tokens,
    CASE WHEN count(cache_read_tokens) = count(*) THEN sum(cache_read_tokens) END AS cache_read_tokens,
    CASE WHEN count(cache_write_tokens) = count(*) THEN sum(cache_write_tokens) END AS cache_write_tokens,
    CASE WHEN count(actual_cost) = count(*) AND NOT bool_or(stale) AND bool_and(unit IS NULL OR unit='CNY')
         THEN sum(actual_cost) * 1000000 END AS billed_upstream_micros,
    string_agg(DISTINCT comparison_source, ',' ORDER BY comparison_source) AS source
  FROM upstream_rows GROUP BY credential_id, school_model
), confirmed_empty AS (
  SELECT d.credential_id FROM scoped_days d
  WHERE d.requests=0 AND NOT d.stale
    AND NOT EXISTS (SELECT 1 FROM website_days w WHERE w.credential_id=d.credential_id AND w.day=d.day)
)
SELECT COALESCE(l.credential_id, u.credential_id), COALESCE(l.model, u.model), l.requests,
  CASE WHEN u.requests <= 9223372036854775807 THEN u.requests::bigint WHEN u.credential_id IS NULL AND e.credential_id IS NOT NULL THEN 0::bigint END,
  CASE WHEN l.input_tokens <= 9223372036854775807 THEN l.input_tokens::bigint END,
  CASE WHEN u.input_tokens <= 9223372036854775807 THEN u.input_tokens::bigint END,
  CASE WHEN l.output_tokens <= 9223372036854775807 THEN l.output_tokens::bigint END,
  CASE WHEN u.output_tokens <= 9223372036854775807 THEN u.output_tokens::bigint END,
  CASE WHEN l.cache_read_tokens <= 9223372036854775807 THEN l.cache_read_tokens::bigint END,
  CASE WHEN u.cache_read_tokens <= 9223372036854775807 THEN u.cache_read_tokens::bigint END,
  CASE WHEN l.cache_write_tokens <= 9223372036854775807 THEN l.cache_write_tokens::bigint END,
  CASE WHEN u.cache_write_tokens <= 9223372036854775807 THEN u.cache_write_tokens::bigint END,
  CASE WHEN l.estimated_micros <= 9223372036854775807 THEN l.estimated_micros::bigint END,
  CASE WHEN u.billed_upstream_micros BETWEEN -9223372036854775808 AND 9223372036854775807 THEN u.billed_upstream_micros::bigint END,
  CASE WHEN l.trial_credit_milli <= 9223372036854775807 THEN l.trial_credit_milli::bigint END, l.unpriced_calls,
  COALESCE(u.source, CASE WHEN e.credential_id IS NOT NULL THEN 'usage' END), l.trial_credits,
  l.charged_credits, l.pending_review_calls
FROM local l FULL JOIN upstream u USING (credential_id, model)
LEFT JOIN confirmed_empty e ON e.credential_id = l.credential_id
ORDER BY 1, 2`
