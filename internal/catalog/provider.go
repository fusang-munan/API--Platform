package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type ProviderEntry struct {
	ID                 string         `json:"id"`
	Object             string         `json:"object"`
	DisplayName        string         `json:"display_name"`
	CreatedAt          string         `json:"created_at"`
	OwnedBy            string         `json:"owned_by"`
	Description        string         `json:"description"`
	SupportedProtocols []string       `json:"supported_protocols"`
	Raw                map[string]any `json:"-"`
}

const beichenOrigin = "https://api.beichenapi.com"

func IsBeichenBaseURL(baseURL string) bool {
	switch strings.TrimSuffix(strings.ToLower(strings.TrimSpace(baseURL)), "/") {
	case beichenOrigin, beichenOrigin + "/v1":
		return true
	}
	return false
}

func FetchProviderModels(ctx context.Context, baseURL, credential string, client *http.Client) ([]ProviderEntry, error) {
	if IsBeichenBaseURL(baseURL) {
		snapshot, err := FetchBeichenModelSnapshot(ctx, credential, client)
		return snapshot.Entries, err
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	url := strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(credential))
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream models returned HTTP %d", resp.StatusCode)
	}
	return ParseProviderModels(body)
}

const (
	beichenModelsPath = "/v1/models"
	beichenPageBytes  = 2 << 20
	beichenMaxModels  = 2000
	beichenMaxPages   = 100
)

// BeichenModelSnapshot separates positive listings from evidence of absence.
type BeichenModelSnapshot struct {
	Entries  []ProviderEntry
	Complete bool
}

func FetchBeichenModelSnapshot(ctx context.Context, credential string, client *http.Client) (BeichenModelSnapshot, error) {
	credential = strings.TrimSpace(credential)
	if credential == "" || len(credential) > 4096 || strings.ContainsAny(credential, "\r\n") {
		return BeichenModelSnapshot{}, errors.New("北辰凭据格式无效")
	}
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	current := &url.URL{Scheme: "https", Host: "api.beichenapi.com", Path: beichenModelsPath}
	seenPages, seenModels := map[string]bool{}, map[string]bool{}
	all := []ProviderEntry{}
	total, totalPages, bytesRead := -1, 0, 0
	for page := 1; ; page++ {
		fail := func(err error) (BeichenModelSnapshot, error) {
			return BeichenModelSnapshot{}, fmt.Errorf("北辰模型目录第 %d 页不完整：%w", page, err)
		}
		pageKey := current.Query().Encode()
		if page > beichenMaxPages || seenPages[pageKey] {
			return fail(errors.New("分页重复或超过 100 页"))
		}
		seenPages[pageKey] = true
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, current.String(), nil)
		if err != nil {
			return fail(errors.New("分页请求地址无效"))
		}
		req.Header.Set("Authorization", "Bearer "+credential)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Cache-Control", "no-cache")
		body, headers, err := ReadBeichenGET(client, req, beichenPageBytes)
		if err != nil {
			return fail(err)
		}
		bytesRead += len(body)
		if bytesRead > 16<<20 {
			return fail(errors.New("合计响应超过 16 MiB"))
		}
		entries, err := ParseBeichenModels(body)
		if err != nil {
			return fail(err)
		}
		paging, err := decodeBeichenModelPaging(body)
		if err != nil {
			return fail(err)
		}
		if paging.Total != nil {
			if n := *paging.Total; n < 0 || n > beichenMaxModels || (total >= 0 && total != n) {
				return fail(errors.New("分页公布的型号总数无效或发生变化"))
			}
			total = *paging.Total
		}
		if err := paging.checkPage(page, len(entries), &totalPages); err != nil {
			return fail(err)
		}
		if page > 1 && len(entries) == 0 {
			return fail(errors.New("续页返回空清单"))
		}
		for _, entry := range entries {
			id := strings.ToLower(entry.ID)
			if seenModels[id] {
				return fail(errors.New("分页间有重复型号"))
			}
			seenModels[id] = true
			all = append(all, entry)
		}
		if len(all) > beichenMaxModels || (total >= 0 && len(all) > total) {
			return fail(errors.New("型号数量超过上限或公布总数"))
		}
		next, err := paging.nextURL(current, headers, page)
		if err != nil {
			return fail(err)
		}
		if next == nil {
			if total >= 0 && len(all) != total {
				return fail(errors.New("已读型号数量与公布总数不一致"))
			}
			complete := page > 1 || total >= 0 || totalPages > 0 || paging.HasMore != nil ||
				paging.NextPageToken != nil || paging.NextPageTokenCamel != nil
			return BeichenModelSnapshot{Entries: all, Complete: complete}, nil
		}
		if len(entries) == 0 {
			return fail(errors.New("续页前返回空清单"))
		}
		current = next
	}
}

type beichenModelPaging struct {
	HasMore            *bool           `json:"has_more"`
	Next               string          `json:"next"`
	NextCursor         string          `json:"next_cursor"`
	NextPageToken      json.RawMessage `json:"next_page_token"`
	NextPageTokenCamel json.RawMessage `json:"nextPageToken"`
	LastID             string          `json:"last_id"`
	Page               *int            `json:"page"`
	Pages              *int            `json:"pages"`
	TotalPages         *int            `json:"total_pages"`
	Total              *int            `json:"total"`
}

func decodeBeichenModelPaging(body []byte) (beichenModelPaging, error) {
	if bytes.HasPrefix(bytes.TrimSpace(body), []byte("[")) {
		return beichenModelPaging{}, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return beichenModelPaging{}, errors.New("分页字段格式无效")
	}
	levels := []map[string]json.RawMessage{root}
	if bytes.HasPrefix(bytes.TrimSpace(root["data"]), []byte("{")) {
		var data map[string]json.RawMessage
		if err := json.Unmarshal(root["data"], &data); err != nil {
			return beichenModelPaging{}, errors.New("分页字段格式无效")
		}
		levels = append(levels, data)
	}
	for _, level := range levels {
		if raw := level["pagination"]; raw != nil {
			var nested map[string]json.RawMessage
			if err := json.Unmarshal(raw, &nested); err != nil {
				return beichenModelPaging{}, errors.New("分页字段格式无效")
			}
			levels = append(levels, nested)
		}
	}
	fields := map[string]json.RawMessage{}
	for _, level := range levels {
		for name, raw := range level {
			switch name {
			case "hasMore":
				name = "has_more"
			case "nextCursor":
				name = "next_cursor"
			case "next_url", "nextUrl":
				name = "next"
			case "lastId":
				name = "last_id"
			case "totalPages":
				name = "total_pages"
			case "has_more", "next", "next_cursor", "next_page_token", "nextPageToken", "last_id", "page", "pages", "total_pages", "total":
			default:
				continue
			}
			if prior := fields[name]; prior != nil && !bytes.Equal(prior, raw) {
				return beichenModelPaging{}, errors.New("不同层级的分页字段冲突")
			}
			fields[name] = raw
		}
	}
	var paging beichenModelPaging
	raw, err := json.Marshal(fields)
	if err != nil || json.Unmarshal(raw, &paging) != nil {
		return beichenModelPaging{}, errors.New("分页字段格式无效")
	}
	return paging, nil
}

var (
	modelPageLink = regexp.MustCompile(`<([^<>]+)>\s*((?:;[^<>,]*)*)`)
	modelLinkRel  = regexp.MustCompile(`(?i)(?:^|;)\s*rel\s*=\s*(?:"([^"]*)"|([^;\s]+))`)
)

func (p *beichenModelPaging) checkPage(page, size int, totalPages *int) error {
	if p.Page != nil && *p.Page != page {
		return errors.New("响应页码与请求不一致")
	}
	if p.TotalPages != nil {
		if p.Pages != nil && *p.Pages != *p.TotalPages {
			return errors.New("分页总页数冲突")
		}
		p.Pages = p.TotalPages
	}
	if p.Pages != nil {
		count := *p.Pages
		if count == 0 && page == 1 && size == 0 {
			count = 1
		}
		if count < page || count > beichenMaxPages || (*totalPages > 0 && *totalPages != count) {
			return errors.New("分页总页数无效或发生变化")
		}
		*totalPages = count
	}
	if *totalPages > 0 {
		p.Pages = totalPages
	}
	return nil
}

func (p beichenModelPaging) nextURL(current *url.URL, headers http.Header, page int) (*url.URL, error) {
	var camelToken, snakeToken string
	if (p.NextPageTokenCamel != nil && json.Unmarshal(p.NextPageTokenCamel, &camelToken) != nil) ||
		(p.NextPageToken != nil && json.Unmarshal(p.NextPageToken, &snakeToken) != nil) {
		return nil, errors.New("续页令牌格式无效")
	}
	if p.NextPageTokenCamel != nil && p.NextPageToken != nil && camelToken != snakeToken {
		return nil, errors.New("存在冲突的续页令牌")
	}
	token, tokenParam := snakeToken, "page_token"
	if p.NextPageTokenCamel != nil {
		token, tokenParam = camelToken, "pageToken"
	}
	next := p.Next
	for _, value := range headers.Values("Link") {
		for _, link := range modelPageLink.FindAllStringSubmatch(value, -1) {
			rel := modelLinkRel.FindStringSubmatch(link[2])
			if rel == nil || !strings.Contains(" "+strings.ToLower(strings.Join(strings.Fields(rel[1]+rel[2]), " "))+" ", " next ") {
				continue
			}
			if next != "" && next != link[1] {
				return nil, errors.New("存在冲突的续页地址")
			}
			next = link[1]
		}
	}
	moreByPage := p.Pages != nil && *p.Pages > page
	moreByFlag := p.HasMore != nil && *p.HasMore
	hasContinuation := next != "" || p.NextCursor != "" || token != ""
	if (p.HasMore != nil && !*p.HasMore && (moreByPage || hasContinuation)) ||
		(p.Pages != nil && !moreByPage && (moreByFlag || hasContinuation)) {
		return nil, errors.New("分页结束标志冲突")
	}
	if next != "" {
		target, err := current.Parse(next)
		if err != nil || len(next) > 4096 || target.Scheme != current.Scheme || target.Host != current.Host ||
			target.User != nil || target.EscapedPath() != beichenModelsPath || target.Fragment != "" {
			return nil, errors.New("续页地址必须位于北辰模型接口")
		}
		return target, nil
	}
	target := *current
	query := target.Query()
	switch {
	case token != "":
		if len(token) > 4096 {
			return nil, errors.New("续页令牌超过长度上限")
		}
		query.Del("pageToken")
		query.Del("page_token")
		query.Set(tokenParam, token)
	case p.NextCursor != "":
		if len(p.NextCursor) > 4096 {
			return nil, errors.New("续页游标超过长度上限")
		}
		query.Set("cursor", p.NextCursor)
	case moreByPage:
		query.Set("page", strconv.Itoa(page+1))
	case moreByFlag && p.LastID != "":
		if len(p.LastID) > 4096 {
			return nil, errors.New("续页型号标识超过长度上限")
		}
		query.Set("after", p.LastID)
	case moreByFlag:
		return nil, errors.New("声明还有续页，但缺少游标或链接")
	default:
		return nil, nil
	}
	target.RawQuery = query.Encode()
	return &target, nil
}

type BeichenHTTPStatus int

func (status BeichenHTTPStatus) Error() string {
	return fmt.Sprintf("北辰接口返回 HTTP %d", int(status))
}

var ErrBeichenKeyGroupNotAllowed = errors.New("账号无分组权限")

type beichenRateLimit struct{ retryAt time.Time }

func (e *beichenRateLimit) Error() string { return e.Unwrap().Error() }
func (e *beichenRateLimit) Unwrap() error { return BeichenHTTPStatus(http.StatusTooManyRequests) }

// BeichenRetryAt reads the upstream Retry-After deadline through wrapped errors.
func BeichenRetryAt(err error) time.Time {
	var limit *beichenRateLimit
	if errors.As(err, &limit) {
		return limit.retryAt
	}
	return time.Time{}
}

func beichenRetryAt(header string, now time.Time) time.Time {
	header = strings.TrimSpace(header)
	const maxSeconds = int64((1<<63 - 1) / time.Second)
	if seconds, err := strconv.ParseInt(header, 10, 64); err == nil && seconds >= 0 && seconds <= maxSeconds {
		return now.Add(time.Duration(seconds) * time.Second)
	}
	at, _ := http.ParseTime(header)
	return at
}

// ReadBeichenGET retries read-only requests once within the caller's time budget.
func ReadBeichenGET(client *http.Client, req *http.Request, limit int) ([]byte, http.Header, error) {
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	body, headers, err := readBeichenGET(&noRedirect, req, limit)
	var timeout net.Error
	rateLimited := errors.Is(err, BeichenHTTPStatus(http.StatusTooManyRequests))
	timedOut := errors.As(err, &timeout) && timeout.Timeout()
	if req.Method != http.MethodGet || (!rateLimited && !timedOut) || req.Context().Err() != nil {
		return body, headers, err
	}
	delay := max(200*time.Millisecond, time.Until(BeichenRetryAt(err)))
	if deadline, ok := req.Context().Deadline(); delay > 2*time.Second || (ok && time.Until(deadline) <= delay) {
		return nil, headers, err
	}
	reason := "超时"
	if rateLimited {
		reason = "HTTP 429"
	}
	slog.Info("北辰抓取短暂失败，退避后重试", "reason", reason, "delay_ms", delay.Milliseconds())
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-req.Context().Done():
		return nil, headers, err
	case <-timer.C:
	}
	body, headers, retryErr := readBeichenGET(&noRedirect, req, limit)
	if retryErr != nil {
		return nil, headers, fmt.Errorf("北辰抓取因%s重试一次后仍未完成：%w", reason, retryErr)
	}
	return body, headers, nil
}

func readBeichenGET(client *http.Client, req *http.Request, limit int) ([]byte, http.Header, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, safeBeichenReadError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusForbidden && req.URL.Host == "api.beichenapi.com" && BeichenGroupDenied(resp.Body) {
			return nil, resp.Header, ErrBeichenKeyGroupNotAllowed
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			if at := beichenRetryAt(resp.Header.Get("Retry-After"), time.Now()); !at.IsZero() {
				return nil, resp.Header, &beichenRateLimit{retryAt: at}
			}
		}
		return nil, resp.Header, BeichenHTTPStatus(resp.StatusCode)
	}
	if resp.ContentLength > int64(limit) {
		return nil, resp.Header, fmt.Errorf("北辰接口响应超过 %d MiB", limit>>20)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return nil, resp.Header, safeBeichenReadError(err)
	}
	if len(body) > limit {
		return nil, resp.Header, fmt.Errorf("北辰接口响应超过 %d MiB", limit>>20)
	}
	return body, resp.Header, nil
}

func safeBeichenReadError(err error) error {
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return fmt.Errorf("北辰网络请求超时：%w", context.DeadlineExceeded)
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return errors.New("北辰网络请求或读取响应失败")
}

const BeichenErrorBodyLimit = 4096

// BeichenGroupDenied recognizes a structured denial without exposing the body.
func BeichenGroupDenied(reader io.Reader) bool {
	body, err := io.ReadAll(io.LimitReader(reader, BeichenErrorBodyLimit+1))
	if err != nil || len(body) > BeichenErrorBodyLimit {
		return false
	}
	type errorCode struct {
		Code   string `json:"code"`
		Type   string `json:"type"`
		Reason string `json:"reason"`
	}
	var reply struct {
		errorCode
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &reply) != nil {
		return false
	}
	var nested errorCode
	_ = json.Unmarshal(reply.Error, &nested)
	for _, fields := range []errorCode{reply.errorCode, nested} {
		if fields.Code == "GROUP_NOT_ALLOWED" || fields.Type == "GROUP_NOT_ALLOWED" || fields.Reason == "GROUP_NOT_ALLOWED" {
			return true
		}
	}
	return false
}

func ParseProviderModels(body []byte) ([]ProviderEntry, error) {
	return parseProviderModels(body, false)
}

// A syntactically valid empty list can be evaluated by the snapshot writer.
func ParseBeichenModels(body []byte) ([]ProviderEntry, error) {
	return parseProviderModels(body, true)
}

func parseProviderModels(body []byte, beichen bool) ([]ProviderEntry, error) {
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("invalid upstream models response")
	}
	if envelope, ok := payload.(map[string]any); beichen && ok {
		if envelope["error"] != nil || envelope["object"] == "error" || envelope["success"] == false {
			return nil, fmt.Errorf("Beichen returned a catalog error")
		}
		if code, exists := envelope["code"]; exists && code != float64(0) {
			return nil, fmt.Errorf("Beichen returned an unsuccessful catalog response")
		}
	}
	records := modelRecords(payload)
	if envelope, ok := payload.(map[string]any); beichen && ok && records == nil {
		if data, ok := envelope["data"].(map[string]any); ok {
			records = modelRecords(data)
			if records == nil {
				records, _ = data["items"].([]any)
			}
		}
	}
	if beichen && records != nil {
		if len(records) == 0 {
			return []ProviderEntry{}, nil
		}
		if len(records) > beichenMaxModels {
			return nil, fmt.Errorf("Beichen model catalog exceeds 2000 records")
		}
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("upstream models list is empty")
	}
	seen := map[string]struct{}{}
	result := make([]ProviderEntry, 0, len(records))
	for _, record := range records {
		item, ok := record.(map[string]any)
		id, _ := item["id"].(string)
		id = strings.TrimSpace(id)
		if !ok || id == "" {
			if beichen {
				return nil, fmt.Errorf("invalid Beichen model catalog record")
			}
			continue
		}
		key := strings.ToLower(id)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		entry := ProviderEntry{ID: id, Raw: item}
		if object, ok := item["object"].(string); ok {
			entry.Object = object
		}
		if modelType, ok := item["type"].(string); ok && entry.Object == "" {
			entry.Object = modelType
		}
		if displayName, ok := item["display_name"].(string); ok {
			entry.DisplayName = strings.TrimSpace(displayName)
		}
		if createdAt, ok := item["created_at"].(string); ok {
			entry.CreatedAt = strings.TrimSpace(createdAt)
		}
		if created, ok := item["created"].(float64); ok && entry.CreatedAt == "" {
			entry.CreatedAt = time.Unix(int64(created), 0).UTC().Format(time.RFC3339)
		}
		if ownedBy, ok := item["owned_by"].(string); ok {
			entry.OwnedBy = strings.TrimSpace(ownedBy)
		}
		if description, ok := item["description"].(string); ok {
			entry.Description = plainText(description)
		}
		entry.SupportedProtocols = RegisteredProtocols(entry)
		result = append(result, entry)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("upstream models list is empty")
	}
	return result, nil
}

func modelRecords(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case map[string]any:
		for _, key := range []string{"data", "models"} {
			if records, ok := typed[key].([]any); ok {
				return records
			}
		}
	}
	return nil
}

func ProviderEntryItem(entry ProviderEntry) Item {
	tags := extractStringList(entry.Raw, "tags")
	modelTypes := extractStringList(entry.Raw, "model_type", "type")
	endpoints := extractStringSlice(entry.Raw, "supported_endpoint_types")
	if entry.OwnedBy != "" {
		tags = add(tags, entry.OwnedBy)
	}
	description := entry.Description
	if description == "" && entry.DisplayName != "" && !strings.EqualFold(entry.DisplayName, entry.ID) {
		description = entry.DisplayName
	}
	return Item{
		ModelID:                entry.ID,
		Description:            description,
		OwnedBy:                nonEmptySlice(entry.OwnedBy),
		ModelTypes:             modelTypes,
		SupportedEndpointTypes: endpoints,
		Tags:                   tags,
	}
}

func extractStringList(raw map[string]any, keys ...string) []string {
	if raw == nil {
		return nil
	}
	result := []string{}
	for _, key := range keys {
		switch value := raw[key].(type) {
		case string:
			for _, part := range strings.Split(value, ",") {
				result = add(result, part)
			}
		}
	}
	return result
}

func extractStringSlice(raw map[string]any, keys ...string) []string {
	for _, key := range keys {
		if values, ok := ResolveStringSlice(raw, key); ok {
			return values
		}
	}
	return nil
}

func ProviderDisplayName(entry ProviderEntry) string {
	if name := strings.TrimSpace(entry.DisplayName); name != "" {
		return name
	}
	return entry.ID
}

func nonEmptySlice(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return []string{value}
}
