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
	keyBillingRateSource          = "key_billing"
	keyBillingDefaultRateMismatch = "Key 账单默认倍率与官网分组不一致"
	keyBillingPeakMismatch        = "Key 账单高峰状态与官网分组不一致"
)

var errWebsiteAccountRateUnverified = errors.New("北辰官网账户倍率未核实，分组倍率仅供参考")

func accountRateUnverified(reason string) error {
	return fmt.Errorf("%w：%s", errWebsiteAccountRateUnverified, reason)
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

// The caller must have identified the live Key and confirmed that a
// successful account rates response omitted this website-listed group.
func (s *Service) fetchVerifiedKeyAccountRate(ctx context.Context, credential uuid.UUID, key string,
	identity listedKey, group directoryGroup) (billingInfo, time.Time, error) {
	if group.Subscription != "standard" {
		return billingInfo{}, time.Time{}, accountRateUnverified("官网分组不是标准余额计费")
	}
	if group.PeakEnabled == nil || *group.PeakEnabled {
		return billingInfo{}, time.Time{}, accountRateUnverified("官网分组未确认关闭高峰计费")
	}
	if group.ID != identity.GroupID || !validWebsiteGroupID(group.ID) || !validWebsiteGroupID(identity.ID) ||
		group.Rate == nil || websiteGroupRate(*group.Rate) == nil {
		return billingInfo{}, time.Time{}, accountRateUnverified("官网分组身份或默认倍率无法核对")
	}
	info, err := s.fetchKeyBillingInfo(ctx, key, identity.GroupID)
	if err != nil {
		var readErr keyBillingReadError
		switch {
		case errors.Is(err, errKeyGroupNotAllowed):
			err = accountRateUnverified("Key 账单读取失败（HTTP 403，分组无权限）")
		case errors.As(err, &readErr):
			err = accountRateReadFailed("Key 账单读取失败", readErr.cause)
		default:
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
		return billingInfo{}, time.Time{}, accountRateUnverified(reason)
	}
	if info.APIKeyID == 0 {
		return billingInfo{}, time.Time{}, accountRateUnverified("Key 账单缺少 Key 编号（api_key_id 未提供或为 0）")
	}
	if info.GroupRate == "" {
		return billingInfo{}, time.Time{}, accountRateUnverified("Key 账单缺少分组默认倍率")
	}
	if !samePanelRate(*group.Rate, info.GroupRate) {
		return billingInfo{}, time.Time{}, accountRateUnverified(keyBillingDefaultRateMismatch)
	}
	if info.PeakEnabled == nil {
		return billingInfo{}, time.Time{}, accountRateUnverified("Key 账单缺少高峰状态")
	}
	if *info.PeakEnabled || (info.AppliedPeak != "" && !samePanelRate(info.AppliedPeak, "1")) {
		return billingInfo{}, time.Time{}, accountRateUnverified(keyBillingPeakMismatch)
	}
	if (info.UserRate != nil && !samePanelRate(*info.UserRate, info.Multiplier)) ||
		(info.ResolvedRate != "" && !samePanelRate(info.ResolvedRate, info.Multiplier)) {
		return billingInfo{}, time.Time{}, accountRateUnverified("Key 账单的账户倍率与生效倍率不一致")
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
