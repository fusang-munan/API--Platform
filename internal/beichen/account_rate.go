package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	keyBillingRateSource           = "key_billing"
	groupRateSource                = "group_rate"
	websiteAccountRatesReadFailure = "官网账户倍率表读取失败"
	keyBillingDefaultRateMismatch  = "Key 账单默认倍率与官网分组不一致"
	keyBillingPeakMismatch         = "Key 账单高峰状态与官网分组不一致"
	keyBillingEffectiveMismatch    = "Key 账单生效倍率与官网分组默认倍率不一致"
)

var errWebsiteAccountRateUnverified = errors.New("北辰官网账户倍率未核实，分组倍率仅供参考")
var errAccountRateConflict = errors.New("北辰账户倍率证据冲突")

func accountRateUnverified(reason string) error {
	return fmt.Errorf("%w：%s", errWebsiteAccountRateUnverified, reason)
}

func accountRateConflict(reason string) error {
	return fmt.Errorf("%w：%w：%s", errWebsiteAccountRateUnverified, errAccountRateConflict, reason)
}

func accountRateReadFailed(reason string, err error) error {
	// Only a typed status code may reach last_error from the upstream error.
	var status upstreamHTTPStatus
	if errors.As(err, &status) {
		reason += fmt.Sprintf("（HTTP %d）", int(status))
	}
	return accountRateUnverified(reason)
}

func (group *WebsiteGroup) setKeyAccountRate(info billingInfo) {
	group.AccountRate = &info.Multiplier
	group.AccountRateSource = keyBillingRateSource
	group.AccountRateKeyID = info.APIKeyID
}

func setGroupAccountRate(group *directoryGroup, identity listedKey) bool {
	if group.ID != identity.GroupID || !validWebsiteGroupID(group.ID) || !validWebsiteGroupID(identity.ID) ||
		group.Subscription != "standard" || group.Rate == nil || websiteGroupRate(*group.Rate) == nil || group.PeakEnabled == nil ||
		*group.PeakEnabled ||
		keyBillingIdentityIssue(billingInfo{GroupRate: *group.Rate, PeakEnabled: group.PeakEnabled}, identity) != "" {
		return false
	}
	rate := *group.Rate
	group.AccountRate, group.AccountRateSource, group.AccountRateKeyID = &rate, groupRateSource, 0
	return true
}

// The caller has identified the live Key but has no verified account-table
// entry for its group.
func (s *Service) fetchVerifiedKeyAccountRate(ctx context.Context, credential uuid.UUID, key string,
	identity listedKey, group directoryGroup) (billingInfo, time.Time, error) {
	if group.ID != identity.GroupID || !validWebsiteGroupID(group.ID) || !validWebsiteGroupID(identity.ID) {
		return billingInfo{}, time.Time{}, accountRateUnverified("官网分组身份或默认倍率无法核对")
	}
	if group.Subscription != "standard" {
		return billingInfo{}, time.Time{}, accountRateUnverified("官网分组不是标准余额计费")
	}
	if group.PeakEnabled == nil || *group.PeakEnabled {
		return billingInfo{}, time.Time{}, accountRateUnverified("官网分组未确认关闭高峰计费")
	}
	if group.Rate == nil || websiteGroupRate(*group.Rate) == nil {
		return billingInfo{}, time.Time{}, accountRateUnverified("官网分组身份或默认倍率无法核对")
	}
	info, err := s.fetchKeyBillingInfo(ctx, key, identity.GroupID)
	if errors.Is(err, errKeyGroupNotAllowed) {
		return billingInfo{}, time.Time{}, err
	}
	if err != nil {
		var readErr keyBillingReadError
		if errors.As(err, &readErr) {
			err = accountRateReadFailed("Key 账单读取失败", readErr.cause)
		} else {
			err = accountRateUnverified("Key 账单格式或生效倍率无效")
		}
		return billingInfo{}, time.Time{}, err
	}
	// The standard response can omit group_id. This fallback still requires
	// it as independent proof of the group.
	if billingPanelGroup(info) == 0 {
		return billingInfo{}, time.Time{}, accountRateUnverified("Key 账单缺少分组号（group_id 未提供或为 0）")
	}
	if reason := keyBillingIdentityIssue(info, identity); reason != "" {
		return billingInfo{}, time.Time{}, accountRateConflict(reason)
	}
	if info.APIKeyID == 0 {
		return billingInfo{}, time.Time{}, accountRateUnverified("Key 账单缺少 Key 编号（api_key_id 未提供或为 0）")
	}
	if info.GroupRate == "" {
		// A standard response may omit the default while still naming the
		// same group. Without an effective rate there is no account evidence.
		if info.Multiplier == "" {
			return billingInfo{}, time.Time{}, accountRateUnverified("Key 账单缺少分组默认倍率")
		}
	}
	if info.GroupRate != "" && !samePanelRate(*group.Rate, info.GroupRate) {
		return billingInfo{}, time.Time{}, accountRateConflict(keyBillingDefaultRateMismatch)
	}
	if info.Multiplier == "" {
		return billingInfo{}, time.Time{}, accountRateUnverified("Key 账单缺少生效倍率")
	}
	// A parsed billing response with a different effective rate is evidence,
	// even when recent usage cannot independently confirm that sample. It
	// cannot be replaced by the group default.
	if !samePanelRate(*group.Rate, info.Multiplier) {
		return billingInfo{}, time.Time{}, accountRateConflict(keyBillingEffectiveMismatch)
	}
	if info.PeakEnabled == nil {
		return billingInfo{}, time.Time{}, accountRateUnverified("Key 账单缺少高峰状态")
	}
	if *info.PeakEnabled || (info.AppliedPeak != "" && !samePanelRate(info.AppliedPeak, "1")) {
		return billingInfo{}, time.Time{}, accountRateConflict(keyBillingPeakMismatch)
	}
	if (info.UserRate != nil && !samePanelRate(*info.UserRate, info.Multiplier)) ||
		(info.ResolvedRate != "" && !samePanelRate(info.ResolvedRate, info.Multiplier)) {
		return billingInfo{}, time.Time{}, accountRateConflict("Key 账单的账户倍率与生效倍率不一致")
	}
	at, err := s.currentKeyBillingAt(ctx, info)
	if errors.Is(err, errKeyBillingSampleExpired) {
		return billingInfo{}, time.Time{}, accountRateUnverified("Key 账单采样时段已结束")
	}
	if err != nil {
		return billingInfo{}, time.Time{}, accountRateUnverified("Key 账单采样时间无法核对")
	}
	if err := s.confirmRecentKeyUsageRate(ctx, credential, identity, info.Multiplier, at); err != nil {
		return billingInfo{}, time.Time{}, err
	}
	return info, at, nil
}

func (s *Service) confirmRecentKeyUsageRate(ctx context.Context, credential uuid.UUID, identity listedKey,
	rate json.Number, at time.Time) error {
	today := at.In(beijing).Format(dayLayout)
	var raw []byte
	err := s.db.QueryRowContext(ctx, `
SELECT d.daily_usage FROM beichen_key_usage_days d
JOIN provider_credentials c ON c.id=d.credential_id AND d.key_digest=sha256(c.credential_ciphertext)
WHERE d.credential_id=$1 AND c.status='active' AND d.website_key_id=$2
  AND d.day=$3::text::date AND d.fetched_at BETWEEN $4 AND $5`,
		credential, identity.ID, today, at.Add(-keyUsageFreshness), at).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return accountRateUnverified("近 30 分钟用量读取失败")
	}
	const insufficientUsage = "近 30 分钟用量不足以核对"
	var day websiteDailyUsage
	if err != nil || json.Unmarshal(raw, &day) != nil || day.Date != today ||
		day.GroupID != identity.GroupID || day.Requests == nil || *day.Requests <= 0 {
		return accountRateUnverified(insufficientUsage)
	}
	observed := observedUsageMultiplier(day.usageCounters)
	if observed == nil {
		return accountRateUnverified(insufficientUsage)
	}
	if expected := exactDecimal(rate); expected == nil || observed.Cmp(expected) != 0 {
		return accountRateUnverified("近 30 分钟用量的实付与原价比值和 Key 账单倍率不一致")
	}
	return nil
}
