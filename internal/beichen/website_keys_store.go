package beichen

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

type WebsiteKeyInventoryReport struct {
	WebsiteKeyInventory
	WebsiteKeyInventoryDiagnostics
}

type WebsiteKeyInventoryDiagnostics struct {
	LastError   string         `json:"last_error,omitempty"`
	FailedPage  int            `json:"failed_page,omitempty"`
	MatchIssues map[string]int `json:"match_issues,omitempty"`
}

func (s *Service) WebsiteKeys(ctx context.Context, provider uuid.UUID) (WebsiteKeyInventoryReport, error) {
	view := WebsiteKeyInventoryReport{WebsiteKeyInventory: WebsiteKeyInventory{Status: "unsynced", Keys: []WebsiteKey{}}}
	if s.db == nil {
		return view, unavailable("北辰 Key 清单未配置", nil)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return view, err
	}
	defer tx.Rollback()
	if err := checkProvider(ctx, tx, provider); err != nil {
		return view, err
	}
	var diagnostics sql.NullString
	err = tx.QueryRowContext(ctx, `
SELECT sync.status,sync.last_seen,state.last_error
FROM beichen_website_key_sync sync
LEFT JOIN maintenance_state state ON state.name='beichen_key_inventory/'||sync.provider_id::text
WHERE sync.provider_id=$1`, provider).Scan(&view.Status, &view.LastSeen, &diagnostics)
	if errors.Is(err, sql.ErrNoRows) {
		return view, nil
	}
	if err != nil {
		return view, err
	}
	if diagnostics.Valid {
		if err := json.Unmarshal([]byte(diagnostics.String), &view.WebsiteKeyInventoryDiagnostics); err != nil {
			return view, err
		}
	}
	if view.Status == "current" && (view.LastSeen == nil || s.now().Sub(*view.LastSeen) > 2*websiteDirectoryInterval) {
		view.Status = "stale"
	}
	rows, err := tx.QueryContext(ctx, `
SELECT k.upstream_key_id,k.name,k.key_prefix,k.group_id,k.group_name,trim_scale(k.rate)::text,
       v.peak_enabled,trim_scale(v.peak_rate)::text,v.peak_start,v.peak_end,v.observed_at,
       CASE WHEN v.changed THEN v.observed_at END,
       k.status,k.enabled,k.created_at,c.id,
       CASE WHEN c.id IS NULL AND k.match_status='matched' THEN 'unmatched' ELSE k.match_status END
FROM beichen_website_keys k
LEFT JOIN provider_credentials c ON c.id=k.credential_id AND c.provider_id=k.provider_id
 AND c.status='active' AND k.ciphertext_digest=sha256(c.credential_ciphertext)
LEFT JOIN LATERAL (
  SELECT peak_enabled,peak_rate,peak_start,peak_end,observed_at,changed
  FROM beichen_website_key_rate_versions
  WHERE provider_id=k.provider_id AND upstream_key_id=k.upstream_key_id
  ORDER BY id DESC LIMIT 1
) v ON true
WHERE k.provider_id=$1 ORDER BY k.upstream_key_id`, provider)
	if err != nil {
		return view, err
	}
	defer rows.Close()
	for rows.Next() {
		key := WebsiteKey{LastSeen: view.LastSeen}
		if err := rows.Scan(&key.ID, &key.Name, &key.KeyPrefix, &key.GroupID, &key.GroupName, &key.Rate,
			&key.Peak.Enabled, &key.Peak.Rate, &key.Peak.Start, &key.Peak.End, &key.ObservedAt, &key.ChangedAt, &key.Status,
			&key.Enabled, &key.CreatedAt, &key.CredentialID, &key.MatchStatus); err != nil {
			return view, err
		}
		view.Keys = append(view.Keys, key)
	}
	return view, rows.Err()
}

func (s *Service) RefreshWebsiteKeys(ctx context.Context, provider uuid.UUID) (WebsiteKeyInventoryReport, error) {
	if s.db == nil || s.crypto == nil {
		return WebsiteKeyInventoryReport{}, unavailable("北辰 Key 清单同步未配置", nil)
	}
	if err := checkProvider(ctx, s.db, provider); err != nil {
		return WebsiteKeyInventoryReport{}, err
	}
	if err := s.refreshWebsiteKeys(ctx, provider, true); err != nil {
		return WebsiteKeyInventoryReport{}, err
	}
	return s.WebsiteKeys(ctx, provider)
}

func (s *Service) syncWebsiteKeyInventories(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `
SELECT p.id FROM providers p
LEFT JOIN beichen_panel_accounts a ON a.provider_id=p.id
LEFT JOIN beichen_website_key_sync r ON r.provider_id=p.id
WHERE `+beichenProvider+` AND p.status='active' AND (COALESCE(a.status<>'none',$1) OR (a.status='none' AND $2))
  AND COALESCE(r.next_attempt,'-infinity'::timestamptz)<=clock_timestamp()
ORDER BY p.id`, s.WebSessionConfigured() || s.localLogin != nil || s.panelToken != "" || s.panelFallback != "" || s.sharedPanel != nil,
		s.deliveredWebSessionConfigured())
	if err != nil {
		return err
	}
	defer rows.Close()
	var providers []uuid.UUID
	for rows.Next() {
		var provider uuid.UUID
		if err := rows.Scan(&provider); err != nil {
			return err
		}
		providers = append(providers, provider)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var failed error
	for _, provider := range providers {
		failed = errors.Join(failed, s.refreshWebsiteKeys(ctx, provider, false))
	}
	return failed
}

func (s *Service) refreshWebsiteKeys(ctx context.Context, provider uuid.UUID, manual bool) error {
	_, err := s.refreshWebsiteKeysWith(ctx, provider, manual, func(ctx context.Context) (websiteKeyObservation, error) {
		return s.readWebsiteKeys(ctx, provider)
	})
	return err
}

var errWebsiteKeySessionChanged = errors.New("读取 Key 清单期间北辰网页会话已更换")

func (s *Service) refreshWebsiteKeysWith(ctx context.Context, provider uuid.UUID, manual bool,
	read func(context.Context) (websiteKeyObservation, error)) ([]WebsiteKey, error) {
	lease := uuid.New()
	var oldStatus string
	err := s.db.QueryRowContext(ctx, `
INSERT INTO beichen_website_key_sync(provider_id,lease_id,lease_until)
VALUES ($1,$2,clock_timestamp()+$3::int*interval '1 second')
ON CONFLICT(provider_id) DO UPDATE SET lease_id=EXCLUDED.lease_id,lease_until=EXCLUDED.lease_until
WHERE (beichen_website_key_sync.lease_until IS NULL OR beichen_website_key_sync.lease_until<=clock_timestamp())
  AND ($4 OR beichen_website_key_sync.next_attempt<=clock_timestamp())
RETURNING status`, provider, lease, leaseSeconds, manual).Scan(&oldStatus)
	if errors.Is(err, sql.ErrNoRows) {
		if manual {
			return nil, domain.NewError(domain.CodeInvalidTransition, "北辰 Key 清单正在同步，请稍后刷新")
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	observedAt := s.now().UTC().Truncate(time.Microsecond)
	readCtx, cancel := context.WithTimeout(ctx, jobTimeout)
	var page *keyListPageError
	var capturedVersion []byte
	readCtx = context.WithValue(readCtx, keyListReadTraceKey{}, &page)
	readCtx = context.WithValue(readCtx, capturedWebVersionKey{}, &capturedVersion)
	observation, readErr := read(readCtx)
	cancel()
	if readErr != nil {
		status := "unavailable"
		report := WebsiteKeyInventoryDiagnostics{LastError: "北辰官网 Key 清单读取未完成，沿用上次清单"}
		if panelAuthFailed(readErr) || errors.Is(readErr, errEnvironmentPanelToken) ||
			errors.Is(readErr, errPanelTokenExpired) || errors.Is(readErr, errWebSignIn) {
			status = "session_expired"
			report.LastError = "北辰网页会话失效，沿用上次清单"
		}
		if page != nil || errors.As(readErr, &page) {
			report.FailedPage = page.page
			report.LastError = page.Error() + "，沿用上次清单"
		}
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		return nil, finishWebsiteKeyInventory(saveCtx, s.db, provider, lease, status, nil, report)
	}
	if observation.tokenVersion == nil {
		observation.tokenVersion = capturedVersion
	}
	keys, err := s.applyWebsiteKeys(ctx, provider, lease, observation, observedAt, oldStatus != "current")
	if errors.Is(err, errWebsiteKeySessionChanged) {
		return nil, nil
	}
	return keys, err
}

type websiteKeyWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func finishWebsiteKeyInventory(ctx context.Context, db websiteKeyWriter, provider, lease uuid.UUID, status string, observedAt *time.Time,
	diagnostics WebsiteKeyInventoryDiagnostics) error {
	var details *string
	if diagnostics.LastError != "" || len(diagnostics.MatchIssues) != 0 {
		raw, err := json.Marshal(diagnostics)
		if err != nil {
			return err
		}
		value := string(raw)
		details = &value
	}
	result, err := db.ExecContext(ctx, `
WITH saved AS (
  UPDATE beichen_website_key_sync SET status=$3,last_seen=COALESCE($4,last_seen),
    next_attempt=clock_timestamp()+$5::int*interval '1 minute',lease_id=NULL,lease_until=NULL
  WHERE provider_id=$1 AND lease_id=$2 AND lease_until>clock_timestamp()
  RETURNING provider_id
)
INSERT INTO maintenance_state (id,name,last_error,last_success_at,created_at,updated_at)
SELECT $6,'beichen_key_inventory/'||provider_id::text,$7,$4,clock_timestamp(),clock_timestamp() FROM saved
ON CONFLICT (name) DO UPDATE SET last_error=EXCLUDED.last_error,
  last_success_at=COALESCE(EXCLUDED.last_success_at,maintenance_state.last_success_at),updated_at=EXCLUDED.updated_at`,
		provider, lease, status, observedAt, int(websiteDirectoryInterval/time.Minute), uuid.Must(uuid.NewV7()), details)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return errors.New("北辰 Key 清单同步时限已过")
	}
	return err
}

type websiteCredentialMatch struct {
	id         uuid.UUID
	ciphertext []byte
	revision   time.Time
	candidates []int
}

func (s *Service) applyWebsiteKeys(ctx context.Context, provider, lease uuid.UUID, observation websiteKeyObservation, observedAt time.Time, recovered bool) ([]WebsiteKey, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var active uuid.UUID
	err = tx.QueryRowContext(ctx, `SELECT p.id FROM providers p WHERE p.id=$1 AND p.status='active' AND `+
		beichenProvider+` FOR SHARE`, provider).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, invalid("北辰供应商须处于启用状态才可更新 Key 清单")
	}
	if err != nil {
		return nil, err
	}
	current, unlock, err := s.lockWebsiteSessionVersion(ctx, tx, provider, observation.tokenVersion)
	if err != nil {
		return nil, err
	}
	if unlock != nil {
		defer unlock()
	}
	if !current {
		if err := finishWebsiteKeyInventory(ctx, tx, provider, lease, "unavailable", nil,
			WebsiteKeyInventoryDiagnostics{LastError: errWebsiteKeySessionChanged.Error()}); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, errWebsiteKeySessionChanged
	}
	listed := observation.keys
	credentials, claims, err := s.findWebsiteCredentialMatches(ctx, tx, provider, listed)
	if err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM beichen_website_keys WHERE provider_id=$1", provider); err != nil {
		return nil, err
	}
	matched := make(map[int]websiteCredentialMatch)
	issues := make(map[string]int)
	for _, credential := range credentials {
		var current WebsiteKey
		switch len(credential.candidates) {
		case 0:
			issues["insufficient_key_evidence"]++
		case 1:
			i := credential.candidates[0]
			if claims[i] == 1 {
				key := listed[i]
				current = websiteKeyView(key)
				groupID := current.GroupID
				if groupID == nil && key.GroupID == 0 && key.groupIDKnown {
					groupID = &key.GroupID
				}
				accepted, err := saveConfirmedKeyBinding(ctx, tx, credential.id, websiteKeyBindingObservation{
					KeyID: key.ID, GroupID: groupID, CheckedAt: observedAt, Revision: credential.revision,
				}, true)
				if err != nil {
					return nil, err
				}
				if !accepted {
					continue
				}
				matched[i] = credential
			} else {
				issues["multiple_school_credentials"]++
			}
		default:
			issues["tied_website_candidates"]++
		}
		if err := saveWebsiteKeyVersion(ctx, tx, credential, current, observedAt, recovered); err != nil {
			return nil, err
		}
	}
	keys := make([]WebsiteKey, 0, len(listed))
	for i, listedKey := range listed {
		key := websiteKeyView(listedKey)
		var ciphertextDigest []byte
		if credential, ok := matched[i]; ok {
			key.CredentialID, key.MatchStatus = &credential.id, "matched"
			digest := sha256.Sum256(credential.ciphertext)
			ciphertextDigest = digest[:]
		} else if claims[i] != 0 {
			key.MatchStatus = "ambiguous"
		}
		_, err := tx.ExecContext(ctx, `
INSERT INTO beichen_website_keys
 (provider_id,upstream_key_id,name,key_prefix,group_id,group_name,rate,status,enabled,created_at,credential_id,ciphertext_digest,match_status)
VALUES ($1,$2,$3,$4,$5,$6,$7::text::numeric,$8,$9,$10,$11,$12,$13)`, provider, key.ID, key.Name, key.KeyPrefix,
			key.GroupID, key.GroupName, key.Rate, key.Status, key.Enabled, key.CreatedAt, key.CredentialID, ciphertextDigest, key.MatchStatus)
		if err != nil {
			return nil, err
		}
		if err := recordWebsiteKeyRate(ctx, tx, provider, &key, observedAt); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	if err := finishWebsiteKeyInventory(ctx, tx, provider, lease, "current", &observedAt,
		WebsiteKeyInventoryDiagnostics{MatchIssues: issues}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return keys, nil
}

func recordWebsiteKeyRate(ctx context.Context, tx *sql.Tx, provider uuid.UUID, key *WebsiteKey, observedAt time.Time) error {
	return tx.QueryRowContext(ctx, `
WITH observation (provider_id,upstream_key_id,group_id,group_name,rate,peak_enabled,peak_rate,peak_start,peak_end,observed_at) AS (
  VALUES ($1::uuid,$2::bigint,$3::bigint,$4::text,$5::text::numeric,$6::boolean,$7::text::numeric,$8::text,$9::text,$10::timestamptz)
), previous AS (
  SELECT group_id,group_name,rate,peak_enabled,peak_rate,peak_start,peak_end,observed_at,changed
  FROM beichen_website_key_rate_versions
  WHERE provider_id=$1 AND upstream_key_id=$2
  ORDER BY id DESC LIMIT 1
), inserted AS (
  INSERT INTO beichen_website_key_rate_versions
    (provider_id,upstream_key_id,group_id,group_name,rate,peak_enabled,peak_rate,peak_start,peak_end,observed_at,changed)
  SELECT o.*, EXISTS (SELECT 1 FROM previous)
  FROM observation o
  WHERE NOT EXISTS (
    SELECT 1 FROM previous p
    WHERE ROW(p.group_id,p.group_name,p.rate,p.peak_enabled,p.peak_rate,p.peak_start,p.peak_end)
      IS NOT DISTINCT FROM ROW(o.group_id,o.group_name,o.rate,o.peak_enabled,o.peak_rate,o.peak_start,o.peak_end)
  )
  RETURNING observed_at,changed
)
SELECT observed_at,CASE WHEN changed THEN observed_at END FROM inserted
UNION ALL
SELECT observed_at,CASE WHEN changed THEN observed_at END FROM previous WHERE NOT EXISTS (SELECT 1 FROM inserted)`,
		provider, key.ID, key.GroupID, key.GroupName, key.Rate, key.Peak.Enabled, key.Peak.Rate,
		key.Peak.Start, key.Peak.End, observedAt).Scan(&key.ObservedAt, &key.ChangedAt)
}

func (s *Service) findWebsiteCredentialMatches(ctx context.Context, tx *sql.Tx, provider uuid.UUID, keys []listedKey) ([]websiteCredentialMatch, []int, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT c.id,c.credential_ciphertext,c.updated_at,c.label::text,COALESCE(p.channel,c.label::text)
FROM provider_credentials c LEFT JOIN beichen_provision_channels p ON p.credential_id=c.id
WHERE c.provider_id=$1 AND c.status='active' ORDER BY c.id FOR NO KEY UPDATE OF c`, provider)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var credentials []websiteCredentialMatch
	claims := make([]int, len(keys))
	for rows.Next() {
		var credential websiteCredentialMatch
		var label, channel string
		if err := rows.Scan(&credential.id, &credential.ciphertext, &credential.revision, &label, &channel); err != nil {
			return nil, nil, err
		}
		secret, err := s.crypto.DecryptCredential(credential.ciphertext)
		if err == nil {
			credential.candidates = websiteKeyCandidates(keys, secret, channel, label)
			for _, i := range credential.candidates {
				claims[i]++
			}
		}
		credentials = append(credentials, credential)
	}
	return credentials, claims, rows.Err()
}

func saveWebsiteKeyVersion(ctx context.Context, tx *sql.Tx, credential websiteCredentialMatch, key WebsiteKey, observedAt time.Time, recovered bool) error {
	effectiveAt, err := priceTimestamp(ctx, tx)
	if err != nil {
		return err
	}
	changed := false
	if key.GroupID != nil && key.Rate != nil {
		// This is the group's base multiplier. It has no verified per-user
		// override or peak rule, so it cannot be used alone to settle a call.
		result, err := tx.ExecContext(ctx, `
INSERT INTO beichen_multiplier_versions(credential_id,effective_at,multiplier,group_id,group_rate,observed_at,stale_at)
SELECT $1,$2,$3::text::numeric,$4,$3::text::numeric,$5,$2
WHERE NOT EXISTS (
  SELECT 1 FROM (SELECT group_id,group_rate FROM beichen_multiplier_versions WHERE credential_id=$1
                ORDER BY effective_at DESC,id DESC LIMIT 1) previous
  WHERE previous.group_id=$4 AND previous.group_rate=$3::text::numeric
)`, credential.id, effectiveAt, key.Rate, key.GroupID, observedAt)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		changed = count != 0
	}
	unknown := key.GroupID == nil || key.Rate == nil || key.Enabled == nil || !*key.Enabled
	if unknown {
		if err := markMultiplierStale(ctx, tx, credential.id, effectiveAt); err != nil {
			return err
		}
	}
	if changed || unknown {
		if err := invalidateChangedPanelQuote(ctx, tx, credential.id, credential.revision, effectiveAt, 0, false); err != nil {
			return err
		}
	}
	if changed || unknown || recovered {
		return schedulePanelRefresh(ctx, tx, credential.id)
	}
	return nil
}
