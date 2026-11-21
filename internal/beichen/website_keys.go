package beichen

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
)

type WebsiteKey struct {
	ID           int64          `json:"id"`
	Name         string         `json:"name"`
	KeyPrefix    *string        `json:"key_prefix"`
	GroupID      *int64         `json:"group_id"`
	GroupName    *string        `json:"group_name"`
	Rate         *string        `json:"rate"`
	Peak         WebsiteKeyPeak `json:"peak"`
	ObservedAt   *time.Time     `json:"observed_at"`
	ChangedAt    *time.Time     `json:"changed_at"`
	Status       string         `json:"status"`
	Enabled      *bool          `json:"enabled"`
	CreatedAt    *time.Time     `json:"created_at"`
	LastSeen     *time.Time     `json:"last_seen"`
	CredentialID *uuid.UUID     `json:"credential_id"`
	MatchStatus  string         `json:"match_status"`
}

type WebsiteKeyPeak struct {
	Enabled *bool   `json:"enabled"`
	Rate    *string `json:"rate"`
	Start   *string `json:"start"`
	End     *string `json:"end"`
}

type WebsiteKeyInventory struct {
	Status   string       `json:"status"`
	LastSeen *time.Time   `json:"last_seen"`
	Keys     []WebsiteKey `json:"keys"`
}

type websiteKeyObservation struct {
	keys         []listedKey
	tokenVersion []byte
}

func (s *Service) readWebsiteKeys(ctx context.Context, provider uuid.UUID) (websiteKeyObservation, error) {
	return readWithWebsiteKeySession(ctx, s, provider, s.panelToken, func(session panelAuthorization) (websiteKeyObservation, error) {
		keys, err := s.fetchWebsiteKeys(ctx, session.bearer)
		return websiteKeyObservation{keys: keys, tokenVersion: session.tokenVersion}, err
	})
}

func readWithWebsiteKeySession[T any](ctx context.Context, s *Service, provider uuid.UUID, fallback string,
	read func(panelAuthorization) (T, error)) (T, error) {
	var zero T
	authorization, managed, err := s.managedPanelAuthorization(ctx, provider, fallback)
	if err != nil {
		return zero, err
	}
	if managed {
		return readWithPanelSession(ctx, s, provider, authorization, true, read)
	}
	if !s.WebSessionConfigured() {
		return zero, errPanelAccountMissing
	}
	return readWithCapturedWebSession(ctx, s, func(bearer string) (T, error) {
		return read(panelAuthorization{bearer: bearer})
	})
}

func (s *Service) fetchWebsiteKeys(ctx context.Context, bearer string) ([]listedKey, error) {
	keys, err := s.accountKeys(ctx, bearer)
	if err != nil {
		return nil, err
	}
	missing := websiteKeysMissingGroups(keys)
	if len(missing) == 0 {
		return keys, nil
	}
	body, err := s.fetchPanelBody(ctx, bearer)
	if err != nil {
		return nil, s.webReadError(bearer, err)
	}
	if err := supplementWebsiteKeyGroups(keys, missing, body); err != nil {
		return nil, err
	}
	return keys, nil
}

func websiteKeysMissingGroups(keys []listedKey) map[int64]bool {
	missing := make(map[int64]bool)
	for _, key := range keys {
		if validWebsiteGroupID(key.GroupID) && (key.Group == nil ||
			websiteKeyText(key.Group.Name) == nil || websiteGroupRate(key.Group.Rate) == nil) {
			missing[key.GroupID] = true
		}
	}
	return missing
}

func supplementWebsiteKeyGroups(keys []listedKey, missing map[int64]bool, body []byte) error {
	channels, err := decodePanelChannels(body)
	if errors.Is(err, errPanelEmpty) {
		return nil
	}
	if err != nil {
		return err
	}
	groups := make(map[int64]panelGroup)
	conflicts := make(map[int64]bool)
	for _, channel := range channels {
		for _, section := range channel.Platforms {
			for _, group := range section.Groups {
				if !missing[group.ID] {
					continue
				}
				group.Name = strings.TrimSpace(group.Name)
				previous := groups[group.ID]
				if previous.Name != "" && group.Name != "" && previous.Name != group.Name ||
					previous.Rate != "" && group.Rate != "" && !samePanelRate(previous.Rate, group.Rate) {
					conflicts[group.ID] = true
				}
				previous.ID = group.ID
				if previous.Name == "" {
					previous.Name = strings.TrimSpace(group.Name)
				}
				if previous.Rate == "" {
					previous.Rate = group.Rate
				}
				groups[group.ID] = previous
			}
		}
	}
	for i := range keys {
		key := &keys[i]
		group, found := groups[key.GroupID]
		if !found || conflicts[key.GroupID] {
			continue
		}
		if key.Group == nil {
			key.Group = &panelGroup{ID: key.GroupID}
		}
		if websiteKeyText(key.Group.Name) == nil {
			key.Group.Name = group.Name
		}
		if websiteGroupRate(key.Group.Rate) == nil {
			key.Group.Rate = group.Rate
		}
	}
	return nil
}

func validWebsiteGroupID(id int64) bool {
	return id > 0 && id <= 9007199254740991
}

func websiteKeyText(value string) *string {
	value = strings.TrimSpace(value)
	if !validModel(value) {
		return nil
	}
	return &value
}

func websiteGroupRate(number json.Number) *string {
	rate := exactDecimal(number)
	if rate == nil || rate.Cmp(big.NewRat(1000, 1)) > 0 {
		return nil
	}
	value := number.String()
	return &value
}

func websiteKeyPeakTime(value string) *string {
	value = strings.TrimSpace(value)
	for _, layout := range []string{"15:04", "15:04:05"} {
		if at, err := time.Parse(layout, value); err == nil {
			if at.Second() != 0 {
				value = at.Format("15:04:05")
			} else {
				value = at.Format("15:04")
			}
			return &value
		}
	}
	return nil
}

func websiteKeyView(key listedKey) WebsiteKey {
	view := WebsiteKey{ID: key.ID, Status: "unknown", MatchStatus: "unmatched", CreatedAt: key.CreatedAt}
	if name := websiteKeyText(key.Name); name != nil {
		view.Name = *name
	}
	prefix, _, valid := key.fragments()
	if prefix == "" && key.prefixPreview != "" {
		prefix, valid = key.prefixPreview, true
	}
	if valid && len(prefix) >= 4 {
		prefix = prefix[:min(10, len(prefix))]
		if (key.fingerprint == nil || sha256.Sum256([]byte(prefix)) != *key.fingerprint) && strings.IndexFunc(prefix, func(c rune) bool {
			return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_')
		}) == -1 {
			view.KeyPrefix = &prefix
		}
	}
	if validWebsiteGroupID(key.GroupID) {
		view.GroupID = &key.GroupID
		if key.Group != nil {
			view.GroupName, view.Rate = websiteKeyText(key.Group.Name), websiteGroupRate(key.Group.Rate)
			view.Peak = WebsiteKeyPeak{
				Enabled: key.Group.PeakEnabled, Rate: websiteGroupRate(key.Group.PeakRate),
				Start: websiteKeyPeakTime(key.Group.PeakStart), End: websiteKeyPeakTime(key.Group.PeakEnd),
			}
		}
	}
	switch key.Status {
	case "active", "inactive", "disabled", "expired", "quota_exhausted":
		view.Status = key.Status
		enabled := key.Status == "active"
		view.Enabled = &enabled
	}
	return view
}

func websiteKeyMatchStrength(key listedKey, secret string, digest [sha256.Size]byte, channel, label string) int {
	if key.fingerprint != nil {
		if *key.fingerprint == digest {
			return 3
		}
		return 0
	}
	prefix, suffix, valid := key.fragments()
	if !valid || len(prefix)+len(suffix) >= len(secret) ||
		!strings.HasPrefix(secret, prefix) || !strings.HasSuffix(secret, suffix) {
		return 0
	}
	visible := len(strings.TrimPrefix(prefix, "sk-")) + len(suffix)
	name := strings.TrimSpace(key.Name)
	if visible >= 4 && name != "" && (strings.EqualFold(name, label) || strings.EqualFold(name, channel) ||
		strings.EqualFold(name, beichenKeyAliases[channel])) {
		return 2
	}
	if visible >= 8 {
		return 1
	}
	return 0
}

func websiteKeyCandidates(keys []listedKey, secret, channel, label string) []int {
	if secret == "" {
		return nil
	}
	digest := sha256.Sum256([]byte(secret))
	var candidates []int
	best := 1
	for i, key := range keys {
		strength := websiteKeyMatchStrength(key, secret, digest, channel, label)
		if strength < best {
			continue
		}
		if strength > best {
			best, candidates = strength, nil
		}
		candidates = append(candidates, i)
	}
	return candidates
}
