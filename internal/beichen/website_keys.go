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
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	KeyPrefix    *string    `json:"key_prefix"`
	GroupID      *int64     `json:"group_id"`
	GroupName    *string    `json:"group_name"`
	Rate         *string    `json:"rate"`
	Status       string     `json:"status"`
	Enabled      *bool      `json:"enabled"`
	CreatedAt    *time.Time `json:"created_at"`
	LastSeen     *time.Time `json:"last_seen"`
	CredentialID *uuid.UUID `json:"credential_id"`
	MatchStatus  string     `json:"match_status"`
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
	authorization, managed, err := s.managedPanelAuthorization(ctx, provider, s.panelToken)
	if err != nil {
		return websiteKeyObservation{}, err
	}
	if managed {
		return readWithPanelSession(ctx, s, provider, authorization, true, func(session panelAuthorization) (websiteKeyObservation, error) {
			keys, err := s.fetchWebsiteKeys(ctx, session.bearer)
			return websiteKeyObservation{keys: keys, tokenVersion: session.tokenVersion}, err
		})
	}
	if !s.WebSessionConfigured() {
		return websiteKeyObservation{}, errPanelAccountMissing
	}
	return readWithCapturedWebSession(ctx, s, func(bearer string) (websiteKeyObservation, error) {
		keys, err := s.fetchWebsiteKeys(ctx, bearer)
		return websiteKeyObservation{keys: keys}, err
	})
}

func (s *Service) fetchWebsiteKeys(ctx context.Context, bearer string) ([]listedKey, error) {
	keys, err := s.accountKeys(ctx, bearer)
	if err != nil {
		return nil, err
	}
	missing := make(map[int64]bool)
	for _, key := range keys {
		if validWebsiteGroupID(key.GroupID) && (key.Group == nil ||
			websiteKeyText(key.Group.Name) == nil || websiteGroupRate(key.Group.Rate) == nil) {
			missing[key.GroupID] = true
		}
	}
	if len(missing) == 0 {
		return keys, nil
	}
	body, err := s.fetchPanelBody(ctx, bearer)
	if err != nil {
		return nil, s.webReadError(bearer, err)
	}
	channels, err := decodePanelChannels(body)
	if errors.Is(err, errPanelEmpty) {
		return keys, nil
	}
	if err != nil {
		return nil, err
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
		if key.Group != nil {
			if name := websiteKeyText(key.Group.Name); name != nil {
				group.Name = *name
			}
			if websiteGroupRate(key.Group.Rate) != nil {
				group.Rate = key.Group.Rate
			}
		}
		key.Group = &group
	}
	return keys, nil
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
		if strings.IndexFunc(prefix, func(c rune) bool {
			return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_')
		}) == -1 {
			view.KeyPrefix = &prefix
		}
	}
	if validWebsiteGroupID(key.GroupID) {
		view.GroupID = &key.GroupID
		if key.Group != nil {
			view.GroupName, view.Rate = websiteKeyText(key.Group.Name), websiteGroupRate(key.Group.Rate)
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
