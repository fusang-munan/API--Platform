package beichen

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

const websiteDirectoryInterval = 15 * time.Minute

var (
	errWebsiteDirectoryEmpty   = errors.New("北辰官网目录未返回可读取的模型")
	errWebsiteDirectoryPartial = errors.New("北辰官网部分平台目录无法读取")
)

type WebsiteDirectoryStatus struct {
	State       string     `json:"state"`
	LastSuccess *time.Time `json:"last_success"`
	LastError   *string    `json:"last_error"`
}

type WebsiteGroup struct {
	ID     int64  `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
}

type directoryGroup struct {
	WebsiteGroup
	Platform     string       `json:"platform,omitempty"`
	Subscription string       `json:"subscription_type,omitempty"`
	Rate         *json.Number `json:"rate_multiplier,omitempty"`
	PeakEnabled  *bool        `json:"peak_rate_enabled,omitempty"`
	PeakStart    string       `json:"peak_start,omitempty"`
	PeakEnd      string       `json:"peak_end,omitempty"`
	PeakRate     *json.Number `json:"peak_rate_multiplier,omitempty"`
}

type directoryModel struct {
	Name       string          `json:"name"`
	Platform   string          `json:"platform,omitempty"`
	Pricing    json.RawMessage `json:"pricing"`
	ObservedAt time.Time       `json:"observed_at"`
}

type directoryPlatform struct {
	Scope         string           `json:"scope"`
	ChannelID     int64            `json:"channel_id,omitempty"`
	Channel       string           `json:"channel,omitempty"`
	ChannelStatus string           `json:"channel_status,omitempty"`
	Platform      string           `json:"platform"`
	ObservedAt    time.Time        `json:"observed_at"`
	Groups        []directoryGroup `json:"groups"`
	Models        []directoryModel `json:"models"`
	Incomplete    bool             `json:"incomplete,omitempty"`
}

func decodeWebsiteDirectory(body []byte, observedAt time.Time) ([]directoryPlatform, error) {
	data, err := panelData(body)
	if err != nil {
		return nil, err
	}
	var channels []json.RawMessage
	if json.Unmarshal(data, &channels) != nil || len(channels) == 0 {
		return nil, errWebsiteDirectoryEmpty
	}
	var platforms []directoryPlatform
	partial, modelCount := false, 0
	for i, raw := range channels {
		var channel struct {
			ID        int64             `json:"id"`
			Name      string            `json:"name"`
			Status    string            `json:"status"`
			Platforms []json.RawMessage `json:"platforms"`
		}
		if json.Unmarshal(raw, &channel) != nil || channel.Platforms == nil {
			partial = true
			continue
		}
		for _, rawPlatform := range channel.Platforms {
			section, ok := decodeDirectoryPlatform(rawPlatform, observedAt)
			if !ok {
				partial = true
				continue
			}
			channelName := strings.TrimSpace(channel.Name)
			section.ChannelID, section.Channel, section.ChannelStatus = channel.ID, channelName, channel.Status
			section.Scope = directoryScope(section, i)
			platforms = append(platforms, section)
			modelCount += len(section.Models)
			partial = partial || section.Incomplete
		}
	}
	if modelCount == 0 {
		return nil, errWebsiteDirectoryEmpty
	}
	if partial {
		return platforms, errWebsiteDirectoryPartial
	}
	return platforms, nil
}

func decodeDirectoryPlatform(raw json.RawMessage, observedAt time.Time) (directoryPlatform, bool) {
	var input struct {
		Platform  string            `json:"platform"`
		Groups    json.RawMessage   `json:"groups"`
		Supported []json.RawMessage `json:"supported_models"`
		Models    []json.RawMessage `json:"models"`
	}
	if json.Unmarshal(raw, &input) != nil || !validModel(strings.TrimSpace(input.Platform)) {
		return directoryPlatform{}, false
	}
	rows := input.Supported
	if rows == nil {
		rows = input.Models
	}
	if rows == nil {
		return directoryPlatform{}, false
	}
	section := directoryPlatform{Platform: strings.TrimSpace(input.Platform), ObservedAt: observedAt,
		Groups: []directoryGroup{}, Models: []directoryModel{}}
	var groups []json.RawMessage
	if len(input.Groups) != 0 && json.Unmarshal(input.Groups, &groups) != nil {
		section.Incomplete = true
	}
	for _, item := range groups {
		var group directoryGroup
		if json.Unmarshal(item, &group) != nil || group.ID < 0 || group.ID > 9007199254740991 ||
			(group.ID == 0 && strings.TrimSpace(group.Name) == "") {
			section.Incomplete = true
			continue
		}
		section.Groups = append(section.Groups, group)
	}
	for _, item := range rows {
		var model struct {
			directoryModel
			ID string `json:"id"`
		}
		err := json.Unmarshal(item, &model)
		name := strings.TrimSpace(model.Name)
		if name == "" {
			name = strings.TrimSpace(model.ID)
		}
		if err != nil || !validModel(name) {
			section.Incomplete = true
			continue
		}
		model.Name, model.ObservedAt = name, observedAt
		section.Models = append(section.Models, model.directoryModel)
	}
	return section, true
}

func directoryScope(section directoryPlatform, index int) string {
	platform := strings.ToLower(section.Platform)
	if section.ChannelID > 0 {
		return fmt.Sprintf("%d/%s", section.ChannelID, platform)
	}
	if section.Channel != "" {
		return fmt.Sprintf("%q/%s", strings.ToLower(section.Channel), platform)
	}
	groups := make([]int64, 0, len(section.Groups))
	for _, group := range section.Groups {
		if group.ID > 0 {
			groups = append(groups, group.ID)
		}
	}
	if len(groups) != 0 {
		slices.Sort(groups)
		return fmt.Sprintf("%v/%s", groups, platform)
	}
	return fmt.Sprintf("channel-%d/%s", index, platform)
}

// On partial reads, replace complete scopes and preserve observations that were
// obscured by a bad channel, platform or model record.
func mergeWebsiteDirectory(previous, current []directoryPlatform) []directoryPlatform {
	seen := make(map[string]int, len(current))
	for i, section := range current {
		seen[section.Scope] = i
	}
	for _, old := range previous {
		i, found := seen[old.Scope]
		if !found {
			current = append(current, old)
			continue
		}
		section := &current[i]
		if !section.Incomplete {
			continue
		}
		for _, group := range old.Groups {
			if !slices.ContainsFunc(section.Groups, func(candidate directoryGroup) bool {
				return group.ID == candidate.ID && (group.ID != 0 || group.Name == candidate.Name)
			}) {
				section.Groups = append(section.Groups, group)
			}
		}
		models := make(map[string]bool, len(section.Models))
		for _, model := range section.Models {
			models[model.Name] = true
		}
		for _, model := range old.Models {
			if !models[model.Name] {
				section.Models = append(section.Models, model)
			}
		}
	}
	return current
}

func (s *Service) syncWebsiteDirectories(ctx context.Context, provider *uuid.UUID) error {
	if !s.WebSessionConfigured() {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT p.id FROM providers p
LEFT JOIN beichen_website_directories d ON d.provider_id=p.id
WHERE `+beichenProvider+` AND p.status='active' AND ($1::uuid IS NULL OR p.id=$1)
  AND ($1 IS NOT NULL OR COALESCE(d.next_attempt, '-infinity'::timestamptz)<=statement_timestamp())
ORDER BY COALESCE(d.next_attempt, '-infinity'::timestamptz), p.id`, provider)
	if err != nil {
		return unavailable("读取北辰官网目录同步任务失败", err)
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return unavailable("读取北辰官网目录同步任务失败", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return unavailable("读取北辰官网目录同步任务失败", err)
	}
	if err := rows.Err(); err != nil {
		return unavailable("读取北辰官网目录同步任务失败", err)
	}
	var failed error
	for _, id := range ids {
		failed = errors.Join(failed, s.syncWebsiteDirectory(ctx, id, provider != nil))
	}
	if failed != nil {
		return unavailable("北辰官网目录未完整刷新，请查看模型页的目录状态", failed)
	}
	return nil
}

func (s *Service) syncWebsiteDirectory(ctx context.Context, provider uuid.UUID, manual bool) error {
	lease := uuid.New()
	var previous []byte
	err := s.db.QueryRowContext(ctx, `
INSERT INTO beichen_website_directories(provider_id,lease_id,lease_until)
VALUES ($1,$2,clock_timestamp()+$3::int*interval '1 second')
ON CONFLICT(provider_id) DO UPDATE SET lease_id=EXCLUDED.lease_id,lease_until=EXCLUDED.lease_until
WHERE (beichen_website_directories.lease_until IS NULL OR beichen_website_directories.lease_until<=clock_timestamp())
  AND ($4 OR beichen_website_directories.next_attempt<=clock_timestamp())
RETURNING platforms`, provider, lease, leaseSeconds, manual).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	fetch, cancel := context.WithTimeout(ctx, jobTimeout)
	platforms, observedErr := s.fetchWebsiteDirectory(fetch)
	cancel()
	if errors.Is(observedErr, errWebsiteDirectoryPartial) {
		var old []directoryPlatform
		if err := json.Unmarshal(previous, &old); err != nil {
			return err
		}
		platforms = mergeWebsiteDirectory(old, platforms)
	}

	var snapshot, message *string
	if len(platforms) != 0 {
		raw, err := json.Marshal(platforms)
		if err != nil {
			return err
		}
		if len(raw) <= 4*maxResponseBytes {
			body := string(raw)
			snapshot = &body
		} else {
			observedErr = errors.New("北辰官网目录超过存储上限")
		}
	}
	if observedErr != nil {
		safeMessage := observedErr.Error()
		message = &safeMessage
	}
	var saved uuid.UUID
	err = s.db.QueryRowContext(ctx, `
UPDATE beichen_website_directories SET platforms=COALESCE($3::jsonb,platforms),
  last_success=CASE WHEN $4::text IS NULL THEN clock_timestamp() ELSE last_success END,
  last_error=$4,
  failure_count=CASE WHEN $4 IS NULL THEN 0 ELSE LEAST(failure_count+1,6) END,
  next_attempt=clock_timestamp()+CASE WHEN $4 IS NULL THEN $5::int*interval '1 minute'
    ELSE power(2,LEAST(failure_count,5))*interval '1 minute' END,
  lease_id=NULL,lease_until=NULL
WHERE provider_id=$1 AND lease_id=$2 AND lease_until>clock_timestamp()
RETURNING provider_id`, provider, lease, snapshot, message, int(websiteDirectoryInterval/time.Minute)).Scan(&saved)
	return errors.Join(observedErr, err)
}

func (s *Service) fetchWebsiteDirectory(ctx context.Context) ([]directoryPlatform, error) {
	token, err := s.webPanelToken(ctx)
	if err != nil {
		return nil, errors.New("北辰官网目录会话不可用")
	}
	read := func(bearer string) ([]directoryPlatform, error) {
		body, err := s.fetchPanelBody(ctx, bearer)
		if err != nil {
			return nil, err
		}
		return decodeWebsiteDirectory(body, s.now().UTC())
	}
	platforms, err := read(token)
	if !panelAuthFailed(err) {
		return platforms, err
	}
	s.webReadError(token, upstreamHTTPStatus(http.StatusUnauthorized))
	token, err = s.webPanelToken(ctx)
	if err != nil {
		return nil, errors.New("北辰官网目录会话续期失败")
	}
	platforms, err = read(token)
	if panelAuthFailed(err) {
		s.webReadError(token, upstreamHTTPStatus(http.StatusUnauthorized))
	}
	return platforms, err
}

func readWebsiteDirectory(ctx context.Context, tx *sql.Tx, provider uuid.UUID) (WebsiteDirectoryStatus, []directoryPlatform, error) {
	status := WebsiteDirectoryStatus{State: "unverified"}
	var raw []byte
	var stale bool
	err := tx.QueryRowContext(ctx, `
SELECT platforms,last_success,last_error,last_error IS NOT NULL OR
  COALESCE(last_success < statement_timestamp()-$2::int*interval '1 minute',false)
FROM beichen_website_directories WHERE provider_id=$1`, provider, int(2*websiteDirectoryInterval/time.Minute)).
		Scan(&raw, &status.LastSuccess, &status.LastError, &stale)
	if errors.Is(err, sql.ErrNoRows) {
		return status, nil, nil
	}
	if err != nil {
		return status, nil, err
	}
	if status.LastSuccess != nil {
		at := status.LastSuccess.UTC()
		status.LastSuccess = &at
		status.State = "fresh"
	}
	var platforms []directoryPlatform
	if err := json.Unmarshal(raw, &platforms); err != nil {
		return status, nil, err
	}
	if len(platforms) != 0 && stale {
		status.State = "stale"
	}
	return status, platforms, nil
}
