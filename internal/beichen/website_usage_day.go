package beichen

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"math"
	"math/big"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

const (
	maxWebsiteUsagePages = 100
	maxWebsiteUsageBytes = 16 << 20
)

type websiteUsageDay struct {
	unit   string
	daily  dailyUsagePoint
	models []modelUsagePoint
	pages  [][]byte
}

func (s *Service) fetchWebsiteUsageDay(ctx context.Context, bearer string, key listedKey, day string) (websiteUsageDay, error) {
	query := url.Values{
		"api_key_id": {strconv.FormatInt(key.ID, 10)},
		"start_date": {day}, "end_date": {day}, "timezone": {beijing.String()},
		"page_size": {strconv.Itoa(receiptPageSize)}, "sort_by": {"id"}, "sort_order": {"asc"},
	}
	var rows []receiptRow
	var pages [][]byte
	var previousID int64
	var total, size int
	for index := 1; ; index++ {
		page, err := fetchReceiptPage(ctx, s.client, bearer, query, index)
		if err != nil {
			return websiteUsageDay{}, err
		}
		if index == 1 {
			total = *page.Data.Total
		}
		size += len(page.rawResponse)
		if *page.Data.Total != total || page.Data.Pages > maxWebsiteUsagePages || size > maxWebsiteUsageBytes {
			return websiteUsageDay{}, errors.New("北辰单 Key 账日分页变化或超过快照容量，请重试或查看逐笔流水")
		}
		for _, row := range page.Data.Items {
			if row.ID <= previousID || row.KeyID != key.ID || row.UserID != key.UserID ||
				row.CreatedAt.IsZero() || row.CreatedAt.In(beijing).Format(dayLayout) != day || !validModel(row.Model) ||
				hasNegative(row.Input, row.Output, row.CacheRead, row.CacheWrite) {
				return websiteUsageDay{}, errReceiptPagination
			}
			previousID = row.ID
		}
		pages = append(pages, page.rawResponse)
		rows = append(rows, page.Data.Items...)
		if index == page.Data.Pages {
			break
		}
	}
	result, err := summarizeWebsiteUsage(rows, day)
	result.pages = pages
	return result, err
}

type websiteUsageSum struct {
	count   int64
	tokens  [4]*int64
	amounts [2]*big.Rat
	unit    string
}

func (sum *websiteUsageSum) add(row receiptRow) error {
	unit := strings.ToUpper(strings.TrimSpace(row.Currency))
	if unit == "" {
		unit = "unknown"
	}
	if len(unit) > 32 || strings.ContainsRune(unit, 0) {
		return errReceiptPagination
	}
	if sum.count == 0 {
		sum.unit = unit
	} else if sum.unit != unit {
		sum.unit = "mixed"
	}
	for i, value := range [4]*int64{row.Input, row.Output, row.CacheRead, row.CacheWrite} {
		switch {
		case value == nil:
			sum.tokens[i] = nil
		case sum.count == 0:
			copy := *value
			sum.tokens[i] = &copy
		case sum.tokens[i] != nil:
			if *value > math.MaxInt64-*sum.tokens[i] {
				return errReceiptPagination
			}
			*sum.tokens[i] += *value
		}
	}
	cost := row.Cost
	if cost == nil {
		cost = row.TotalCost
	}
	var actual *json.Number
	if !websiteMissing(row.ActualCost) {
		number := json.Number(row.ActualCost)
		actual = &number
	}
	for i, number := range [2]*json.Number{cost, actual} {
		if number == nil {
			sum.amounts[i] = nil
			continue
		}
		if !receiptNumber.MatchString(strings.TrimPrefix(number.String(), "-")) {
			return errReceiptPagination
		}
		amount, ok := new(big.Rat).SetString(number.String())
		if !ok {
			return errReceiptPagination
		}
		if sum.count == 0 {
			sum.amounts[i] = amount
		} else if sum.amounts[i] != nil {
			sum.amounts[i].Add(sum.amounts[i], amount)
		}
	}
	sum.count++
	return nil
}

func (sum websiteUsageSum) counters() (usageCounters, *int64, error) {
	c := usageCounters{Requests: &sum.count, Input: sum.tokens[0], Output: sum.tokens[1], CacheRead: sum.tokens[2]}
	total, complete := int64(0), true
	for _, count := range sum.tokens {
		if count == nil {
			complete = false
			continue
		}
		if *count > math.MaxInt64-total {
			return usageCounters{}, nil, errReceiptPagination
		}
		total += *count
	}
	if complete {
		c.TotalTokens = &total
	}
	if sum.unit != "mixed" {
		for i, target := range [2]**json.Number{&c.Cost, &c.ActualCost} {
			if sum.amounts[i] != nil {
				amount := json.Number(websiteDecimalText(sum.amounts[i]))
				*target = &amount
			}
		}
	}
	return c, sum.tokens[3], nil
}

func summarizeWebsiteUsage(rows []receiptRow, day string) (websiteUsageDay, error) {
	var total websiteUsageSum
	byModel := map[string]*websiteUsageSum{}
	for _, row := range rows {
		if byModel[row.Model] == nil {
			byModel[row.Model] = &websiteUsageSum{}
		}
		if err := total.add(row); err != nil {
			return websiteUsageDay{}, err
		}
		if err := byModel[row.Model].add(row); err != nil {
			return websiteUsageDay{}, err
		}
	}
	if len(rows) == 0 {
		zero := int64(0)
		total.tokens, total.unit = [4]*int64{&zero, &zero, &zero, &zero}, "unknown"
		total.amounts = [2]*big.Rat{new(big.Rat), new(big.Rat)}
	}
	counters, write, err := total.counters()
	if err != nil {
		return websiteUsageDay{}, err
	}
	result := websiteUsageDay{unit: total.unit, daily: dailyUsagePoint{Date: day, usageCounters: counters, CacheWrite: write},
		models: make([]modelUsagePoint, 0, len(byModel))}
	for _, model := range slices.Sorted(maps.Keys(byModel)) {
		part, modelWrite, err := byModel[model].counters()
		if err != nil {
			return websiteUsageDay{}, err
		}
		if byModel[model].unit != total.unit {
			part.Cost, part.ActualCost = nil, nil
		}
		result.models = append(result.models, modelUsagePoint{Model: model, Date: day, usageCounters: part, CacheWrite: modelWrite})
	}
	return result, nil
}
