package beichen

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const accountKeysURL = "https://beichenapi.com/api/v1/keys"

type listedKey struct {
	ID            int64       `json:"id"`
	UserID        int64       `json:"user_id"`
	GroupID       int64       `json:"group_id"`
	Group         *panelGroup `json:"group"`
	Name          string      `json:"name"`
	Status        string      `json:"status"`
	Masked        string      `json:"masked_key"`
	Prefix        string      `json:"key_prefix"`
	Suffix        string      `json:"key_suffix"`
	CreatedAt     *time.Time  `json:"created_at"`
	fingerprint   *[sha256.Size]byte
	prefixPreview string
}

// The inventory retains a fingerprint and a short prefix for an unmasked Key.
func (k *listedKey) observeValue(value string) bool {
	if strings.HasPrefix(value, "sk-") && len(value) > len("sk-") && len(value) <= 4096 &&
		!strings.ContainsAny(value, "*.") && strings.IndexFunc(value, func(r rune) bool { return r <= ' ' || r >= 127 }) < 0 {
		fingerprint := sha256.Sum256([]byte(value))
		k.fingerprint = &fingerprint
		if len(value) >= 18 {
			k.prefixPreview = value[:10]
		}
		return true
	}
	if k.Masked == "" && strings.ContainsAny(value, "*•….") {
		k.Masked = value
	}
	return false
}

var errKeyList = errors.New("北辰账号的 Key 列表或分页无法确认")
var errKeyMatch = errors.New("未能唯一确认本渠道的北辰 Key 和分组")

func (s *Service) accountKeys(ctx context.Context, token string) ([]listedKey, error) {
	return s.readAccountKeys(ctx, token, nil)
}

// Only an explicit catalog crawl supplies modelKeys. These credentials are
// used in memory for the corresponding Key's request to /v1/models.
func (s *Service) readAccountKeys(ctx context.Context, token string, modelKeys map[int64]string) ([]listedKey, error) {
	if !validPanelBearer(token) {
		return nil, errKeyList
	}
	var keys []listedKey
	seen := map[int64]bool{}
	total := -1
	var owner int64
	for page := 1; ; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, accountKeysURL+"?page_size=100&page="+strconv.Itoa(page), nil)
		if err != nil {
			return nil, errKeyList
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Cache-Control", "no-cache")
		req.Header.Set("X-User-UI-Request", "1")
		body, err := s.readResponse(req)
		if err != nil {
			return nil, s.webReadError(token, err)
		}
		var reply struct {
			Code *int `json:"code"`
			Data struct {
				Items []struct {
					listedKey
					Value string `json:"key"`
				} `json:"items"`
				Total *int `json:"total"`
				Page  int  `json:"page"`
				Size  int  `json:"page_size"`
				Pages int  `json:"pages"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &reply) != nil || reply.Code == nil || *reply.Code != 0 {
			if _, err := panelData(body); panelAuthFailed(err) {
				return nil, s.webReadError(token, upstreamHTTPStatus(http.StatusUnauthorized))
			}
			return nil, errKeyList
		}
		d := reply.Data
		if d.Items == nil || d.Total == nil || *d.Total < 0 || *d.Total > 10000 || d.Page != page || d.Size != 100 ||
			(total >= 0 && *d.Total != total) || len(d.Items) != min(100, *d.Total-len(keys)) {
			return nil, errKeyList
		}
		total = *d.Total
		if d.Pages != (total+99)/100 && !(total == 0 && d.Pages == 1) {
			return nil, errKeyList
		}
		for _, item := range d.Items {
			k := item.listedKey
			if k.ID <= 0 || k.ID > 9007199254740991 || seen[k.ID] || k.UserID <= 0 || owner != 0 && k.UserID != owner {
				return nil, errKeyList
			}
			if k.Group != nil && k.Group.ID != 0 {
				if k.GroupID != 0 && k.Group.ID != k.GroupID {
					return nil, errKeyList
				}
				k.GroupID = k.Group.ID
			}
			if k.observeValue(item.Value) && modelKeys != nil {
				modelKeys[k.ID] = item.Value
			}
			seen[k.ID], owner = true, k.UserID
			keys = append(keys, k)
		}
		if len(keys) == total {
			return keys, nil
		}
	}
}

var beichenKeyAliases = map[string]string{"GM1": "国模", "Gemini": "哈吉米", "CC1": "cc不满血", "G3": "gpt"}

func (k listedKey) fragments() (prefix, suffix string, valid bool) {
	prefix, suffix = k.Prefix, k.Suffix
	if k.Masked == "" {
		return prefix, suffix, true
	}
	split := strings.IndexAny(k.Masked, "*•….")
	if split < 0 {
		return "", "", false
	}
	left := k.Masked[:split]
	right := strings.TrimLeft(k.Masked[split:], "*•….")
	if strings.ContainsAny(right, "*•….") || prefix != "" && prefix != left || suffix != "" && suffix != right {
		return "", "", false
	}
	return left, right, left != "" || right != ""
}

func identifyChannel(secret, channel, label string, keys []listedKey) (listedKey, error) {
	candidates := websiteKeyCandidates(keys, secret, channel, label)
	if len(candidates) != 1 {
		return listedKey{}, errKeyMatch
	}
	key := keys[candidates[0]]
	if !validWebsiteGroupID(key.GroupID) || key.Status != "" && key.Status != "active" {
		return listedKey{}, errKeyMatch
	}
	return key, nil
}

func (info billingInfo) matchWebsiteKey(keys []listedKey, secret, channel, label string) (listedKey, error) {
	var group int64
	if info.GroupID != nil {
		group = *info.GroupID
	}
	if info.KeyID != 0 {
		if info.APIKeyID != 0 && info.APIKeyID != info.KeyID {
			return listedKey{}, errKeyMatch
		}
		info.APIKeyID = info.KeyID
	}
	return matchAccountKey(keys, secret, channel, label, group, info.APIKeyID, info.APIKeyName)
}

func matchAccountKey(keys []listedKey, secret, channel, label string, group, id int64, billingName string) (listedKey, error) {
	var hit listedKey
	fingerprint := sha256.Sum256([]byte(secret))
	groupCount := 0
	for _, k := range keys {
		if k.GroupID == group {
			groupCount++
		}
	}
	for _, k := range keys {
		if id != 0 && k.ID != id || group != 0 && k.GroupID != group || k.GroupID <= 0 || k.GroupID > 9007199254740991 {
			continue
		}
		name := strings.TrimSpace(k.Name)
		labelMatch := strings.EqualFold(name, channel) || strings.EqualFold(name, strings.TrimSpace(label)) || strings.EqualFold(name, beichenKeyAliases[channel])
		wrongChannel := false
		for other, alias := range beichenKeyAliases {
			if channel != "" && other != channel && (strings.EqualFold(name, other) || strings.EqualFold(name, alias)) {
				wrongChannel = true
			}
		}
		if wrongChannel {
			continue
		}
		prefix, suffix, valid := k.fragments()
		if !valid || prefix != "" && !strings.HasPrefix(secret, prefix) || suffix != "" && !strings.HasSuffix(secret, suffix) {
			continue
		}
		maskMatch := len(strings.TrimPrefix(prefix, "sk-"))+len(suffix) >= 8 && (len(prefix) < len(secret) && len(suffix) < len(secret))
		if billingName != "" && !strings.EqualFold(billingName, name) {
			continue
		}
		exactKey := k.fingerprint != nil && *k.fingerprint == fingerprint
		if k.fingerprint != nil && !exactKey {
			continue
		}
		if !(exactKey || id == k.ID || maskMatch || labelMatch && group > 0 && (groupCount == 1 || billingName != "")) {
			continue
		}
		if hit.ID != 0 || (k.Status != "" && k.Status != "active") {
			return listedKey{}, errKeyMatch
		}
		hit = k
	}
	if hit.ID == 0 {
		return listedKey{}, errKeyMatch
	}
	return hit, nil
}

type discoveryJob struct {
	credential, provider uuid.UUID
	channel, label       string
	encrypted            []byte
	revision             time.Time
}

func (s *Service) discoverChannels(ctx context.Context) error {
	return s.discoverChannelKeys(ctx, nil)
}

func (s *Service) discoverChannelKeys(ctx context.Context, selected *uuid.UUID) error {
	rows, err := s.db.QueryContext(ctx, `SELECT c.id,c.provider_id,j.channel,c.label,c.credential_ciphertext,c.updated_at
FROM beichen_provision_channels j JOIN provider_credentials c ON c.id=j.credential_id
JOIN providers p ON p.id=c.provider_id JOIN beichen_panel_access a ON a.credential_id=c.id
LEFT JOIN beichen_panel_accounts session ON session.provider_id=p.id
WHERE j.discover_ids AND a.updated_by=$1 AND c.status='active' AND p.status='active' AND `+beichenProvider+`
 AND ($2::uuid IS NULL OR c.id=$2)
 AND COALESCE(session.status<>'none',$3)
 AND (a.group_id IS NULL OR a.updated_at<>c.updated_at OR j.checked_at IS NULL OR j.checked_at < clock_timestamp()-interval '15 minutes'
      OR ($2 IS NOT NULL AND j.discovery_error IS NOT NULL))
ORDER BY j.checked_at NULLS FIRST,c.id LIMIT 4`, ProvisionActorID, selected,
		s.WebSessionConfigured() || s.panelFallback != "" || s.sharedPanel != nil)
	if err != nil {
		return err
	}
	var jobs []discoveryJob
	for rows.Next() {
		var j discoveryJob
		if err := rows.Scan(&j.credential, &j.provider, &j.channel, &j.label, &j.encrypted, &j.revision); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, j)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(jobs) == 0 {
		return err
	}
	type match struct {
		key     listedKey
		err     error
		managed bool
	}
	matches := make([]match, len(jobs))
	type lookup struct {
		keys    []listedKey
		err     error
		managed bool
	}
	lookups := make(map[uuid.UUID]lookup)
	claims := make(map[int64]int)
	for i, j := range jobs {
		m := &matches[i]
		result, found := lookups[j.provider]
		if !found {
			result.keys, result.managed, result.err = s.lookupPanelKeys(ctx, j.provider)
			lookups[j.provider] = result
		}
		m.err, m.managed = result.err, result.managed
		if m.err == nil {
			var secret string
			secret, m.err = s.crypto.DecryptCredential(j.encrypted)
			if m.err == nil {
				m.key, m.err = identifyChannel(secret, j.channel, j.label, result.keys)
				if m.err == nil {
					claims[m.key.ID]++
				}
			}
		}
	}
	var failed error
	for i, j := range jobs {
		m := matches[i]
		if claims[m.key.ID] > 1 {
			m.err = errKeyMatch
		}
		if applyErr := s.applyDiscovery(ctx, j, m.key, m.err, m.managed); applyErr != nil {
			failed = applyErr
		} else if m.err != nil {
			failed = m.err
		}
	}
	return failed
}

func (s *Service) lookupPanelKeys(ctx context.Context, provider uuid.UUID) ([]listedKey, bool, error) {
	auth, managed, err := s.managedPanelAuthorization(ctx, provider, "")
	if err != nil {
		return nil, managed, err
	}
	if managed {
		keys, err := readWithPanelAuthorization(ctx, s, provider, auth, true, func(token string) ([]listedKey, error) {
			return s.accountKeys(ctx, token)
		})
		return keys, true, err
	}
	token, err := s.webPanelToken(ctx)
	if err != nil {
		return nil, false, err
	}
	keys, err := s.accountKeys(ctx, token)
	return keys, false, err
}

func (s *Service) applyDiscovery(ctx context.Context, j discoveryJob, k listedKey, lookupErr error, managed bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Serialize numeric ID claims and recheck both human decisions and key revision
	// after the network calls, before exposing an active receipt binding.
	var provider uuid.UUID
	err = tx.QueryRowContext(ctx, `SELECT p.id FROM providers p WHERE p.id=$1 AND p.status='active' AND `+beichenProvider+` FOR UPDATE`, j.provider).Scan(&provider)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var group, id, owner sql.NullInt64
	var token []byte
	var current time.Time
	err = tx.QueryRowContext(ctx, `SELECT a.group_id,a.upstream_key_id,a.token_ciphertext,a.updated_at,j.upstream_user_id
FROM beichen_panel_access a JOIN provider_credentials c ON c.id=a.credential_id
JOIN beichen_provision_channels j ON j.credential_id=c.id
WHERE a.credential_id=$1 AND a.updated_by=$2 AND c.updated_at=$3 AND c.status='active'
 AND j.discover_ids AND c.provider_id=$4 FOR UPDATE OF c,a,j`, j.credential, ProvisionActorID, j.revision, j.provider).Scan(&group, &id, &token, &current, &owner)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	actor := uuid.MustParse(ProvisionActorID)
	if lookupErr == nil {
		var occupied bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM beichen_receipt_active_bindings WHERE upstream_key_id=$1 AND credential_id<>$2)`, k.ID, j.credential).Scan(&occupied)
		if err != nil {
			return err
		}
		if occupied {
			lookupErr = errKeyMatch
		}
	}
	var problem *string
	if lookupErr != nil {
		message := "北辰官网读取失败，稍后重试"
		if errors.Is(lookupErr, errKeyMatch) {
			message = "北辰 Key 或分组无法唯一确认"
			if group.Valid {
				if err := s.WritePanelAccessTx(ctx, tx, j.credential, actor, PanelAccessInput{}); err != nil {
					return err
				}
			}
		}
		problem = &message
	} else {
		access, err := s.DeploymentPanelAccess(k.GroupID, k.ID, "")
		if err != nil {
			return err
		}
		if managed {
			access = PanelAccessInput{Enabled: true, GroupID: k.GroupID, UpstreamKeyID: &k.ID,
				webAccount: accountDigest("beichen-provider:" + j.provider.String())}
		}
		same := false
		if group.Int64 == k.GroupID && id.Int64 == k.ID && current.Equal(j.revision) && (!owner.Valid || owner.Int64 == k.UserID) {
			same, err = s.PanelSourceMatches(j.credential, token, access)
			if err != nil {
				return err
			}
		}
		if !same {
			if err := s.WritePanelAccessTx(ctx, tx, j.credential, actor, access); err != nil {
				return err
			}
			if err := QueueCredentialSyncTx(ctx, tx, j.credential); err != nil {
				return err
			}
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE beichen_provision_channels SET checked_at=clock_timestamp(),discovery_error=$2,
upstream_user_id=CASE WHEN $2::text IS NULL THEN $3 ELSE upstream_user_id END WHERE credential_id=$1`, j.credential, problem, k.UserID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
