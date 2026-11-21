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

type listedKey struct {
	ID      int64 `json:"id"`
	UserID  int64 `json:"user_id"`
	GroupID int64 `json:"group_id"`
	Group   *struct {
		ID int64 `json:"id"`
	} `json:"group"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Masked      string `json:"masked_key"`
	Prefix      string `json:"key_prefix"`
	Suffix      string `json:"key_suffix"`
	fingerprint *[sha256.Size]byte
}

// Some versions of the account endpoint return the owner's key in place of
// masked metadata. Reduce it to an in-memory fingerprint during decoding.
func (k *listedKey) UnmarshalJSON(body []byte) error {
	type fields listedKey
	item := struct {
		*fields
		Value string `json:"key"`
	}{fields: (*fields)(k)}
	if err := json.Unmarshal(body, &item); err != nil {
		return err
	}
	if value := item.Value; strings.HasPrefix(value, "sk-") && len(value) <= 4096 &&
		!strings.ContainsAny(value, "*•…. \r\n\t") {
		fingerprint := sha256.Sum256([]byte(value))
		k.fingerprint = &fingerprint
	}
	return nil
}

var errKeyList = errors.New("北辰账号的 Key 列表或分页无法确认")
var errKeyMatch = errors.New("未能唯一确认本渠道的北辰 Key 和分组")

func (s *Service) accountKeys(ctx context.Context, token string) ([]listedKey, error) {
	if !validPanelBearer(token) {
		return nil, errKeyList
	}
	var keys []listedKey
	seen := map[int64]bool{}
	total := -1
	var owner int64
	for page := 1; ; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://beichenapi.com/api/v1/keys?page_size=100&page="+strconv.Itoa(page), nil)
		if err != nil {
			return nil, errKeyList
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Cache-Control", "no-cache")
		body, err := s.readResponse(req)
		if err != nil {
			return nil, s.webReadError(token, err)
		}
		var reply struct {
			Code *int `json:"code"`
			Data struct {
				Items []listedKey `json:"items"`
				Total *int        `json:"total"`
				Page  int         `json:"page"`
				Size  int         `json:"page_size"`
				Pages int         `json:"pages"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &reply) != nil || reply.Code == nil || *reply.Code != 0 {
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
		for _, k := range d.Items {
			if k.ID <= 0 || k.ID > 9007199254740991 || seen[k.ID] || k.UserID <= 0 || owner != 0 && k.UserID != owner {
				return nil, errKeyList
			}
			if k.Group != nil && k.Group.ID != k.GroupID {
				return nil, errKeyList
			}
			seen[k.ID], owner = true, k.UserID
			keys = append(keys, k)
		}
		if len(keys) == total {
			return keys, nil
		}
	}
}

// The inference credential is provided by the school and sent only to its own
// fixed-origin billing endpoint. No inference request or search by raw key is made.
func (s *Service) identifyChannel(ctx context.Context, secret, channel, label string, keys []listedKey) (listedKey, error) {
	body, err := s.fetch(ctx, secret, billingPath, nil)
	if err != nil {
		return listedKey{}, err
	}
	var info billingInfo
	if json.Unmarshal(body, &info) != nil || info.Object != "sub2api.key_billing" || info.Scope != "token" || info.SchemaVersion != 1 {
		return listedKey{}, errKeyMatch
	}
	return info.matchWebsiteKey(keys, secret, channel, label)
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
	aliases := map[string]string{"GM1": "国模", "Gemini": "哈吉米", "CC1": "cc不满血", "G3": "gpt"}
	for _, k := range keys {
		if id != 0 && k.ID != id || group != 0 && k.GroupID != group || k.GroupID <= 0 || k.GroupID > 9007199254740991 {
			continue
		}
		name := strings.TrimSpace(k.Name)
		labelMatch := strings.EqualFold(name, channel) || strings.EqualFold(name, strings.TrimSpace(label)) || strings.EqualFold(name, aliases[channel])
		wrongChannel := false
		for other, alias := range aliases {
			if channel != "" && other != channel && (strings.EqualFold(name, other) || strings.EqualFold(name, alias)) {
				wrongChannel = true
			}
		}
		if wrongChannel {
			continue
		}
		prefix, suffix := k.Prefix, k.Suffix
		if k.Masked != "" {
			parts := strings.FieldsFunc(k.Masked, func(r rune) bool { return r == '*' || r == '•' || r == '…' || r == '.' })
			if len(parts) != 2 || parts[0] == k.Masked || (prefix != "" && prefix != parts[0]) || (suffix != "" && suffix != parts[1]) {
				continue
			}
			prefix, suffix = parts[0], parts[1]
		}
		if prefix != "" && !strings.HasPrefix(secret, prefix) || suffix != "" && !strings.HasSuffix(secret, suffix) {
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
	if !s.WebSessionConfigured() {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT c.id,c.provider_id,j.channel,c.label,c.credential_ciphertext,c.updated_at
FROM beichen_provision_channels j JOIN provider_credentials c ON c.id=j.credential_id
JOIN providers p ON p.id=c.provider_id JOIN beichen_panel_access a ON a.credential_id=c.id
WHERE j.discover_ids AND a.updated_by=$1 AND c.status='active' AND p.status='active' AND `+beichenProvider+`
 AND ($2::uuid IS NULL OR c.id=$2)
 AND (a.group_id IS NULL OR a.updated_at<>c.updated_at OR j.checked_at IS NULL OR j.checked_at < clock_timestamp()-interval '15 minutes'
      OR ($2 IS NOT NULL AND j.discovery_error IS NOT NULL))
ORDER BY j.checked_at NULLS FIRST,c.id LIMIT 4`, ProvisionActorID, selected)
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
	token, lookupErr := s.webPanelToken(ctx)
	var keys []listedKey
	if lookupErr == nil {
		keys, lookupErr = s.accountKeys(ctx, token)
	}
	type match struct {
		key listedKey
		err error
	}
	matches := make([]match, len(jobs))
	claims := make(map[int64]int)
	for i, j := range jobs {
		m := &matches[i]
		m.err = lookupErr
		if m.err == nil {
			var secret string
			secret, m.err = s.crypto.DecryptCredential(j.encrypted)
			if m.err == nil {
				m.key, m.err = s.identifyChannel(ctx, secret, j.channel, j.label, keys)
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
		if applyErr := s.applyDiscovery(ctx, j, m.key, m.err); applyErr != nil {
			failed = applyErr
		} else if m.err != nil {
			failed = m.err
		}
	}
	return failed
}

func (s *Service) applyDiscovery(ctx context.Context, j discoveryJob, k listedKey, lookupErr error) error {
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
