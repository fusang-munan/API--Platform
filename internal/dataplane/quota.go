package dataplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"time"

	"github.com/starprince1234/Nebula-api/internal/usage"
)

func quotaTokenField(path string) string {
	switch path {
	case "/v1/chat/completions", "/v1/messages":
		return "max_tokens"
	case "/v1/responses":
		return "max_output_tokens"
	default:
		return ""
	}
}

func (g *Gateway) quotaTokenLimit(ctx context.Context, r usage.Reservation, route usage.CallRoute, field string) int64 {
	if !route.Beichen || field == "" {
		return 0
	}
	price, err := g.usage.BeichenOutputPrice(ctx, route)
	if err != nil {
		return 0
	}
	return outputBudgetTokens(min(r.MemberAvailableMilli, r.ProjectAvailableMilli), price)
}

func outputBudgetTokens(availableMilli int64, yuanPerMillion string) int64 {
	price, ok := new(big.Rat).SetString(yuanPerMillion)
	if !ok || price.Sign() <= 0 {
		return 0
	}
	// 1000 milli-credits per credit and 100 credits per yuan.
	// One million tokens makes the conversion factor 1_000_000 / 1000 / 100.
	tokens := new(big.Rat).SetInt64(max(0, availableMilli))
	tokens.Mul(tokens, big.NewRat(10, 1)).Quo(tokens, price)
	rounded := new(big.Int).Quo(tokens.Num(), tokens.Denom())
	if !rounded.IsInt64() {
		return math.MaxInt64
	}
	return max(1, rounded.Int64())
}

func applyOutputBudget(payload map[string]json.RawMessage, field string, limit int64) int64 {
	if limit == 0 {
		return 0
	}
	if requested, err := strconv.ParseInt(string(payload[field]), 10, 64); err == nil {
		limit = max(1, min(requested, limit))
	}
	payload[field] = json.RawMessage(strconv.FormatInt(limit, 10))
	return limit
}

func modelCallRejection(err error) (status int, kind, code, message string) {
	var quota *usage.QuotaError
	if !errors.As(err, &quota) {
		return http.StatusBadRequest, "invalid_request_error", "billing_unavailable", "Unable to authorize this model call."
	}
	switch quota.Code {
	case usage.StudentConcurrencyExceeded:
		message = "Too many model calls are in progress for this student."
	case usage.PendingReviewLimit:
		message = "Too many model calls are awaiting billing review for this student."
	default:
		message = fmt.Sprintf("%s quota is exhausted until %s.", quota.Name,
			quota.EndsAt.In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("2006-01-02 15:04 MST"))
	}
	return http.StatusTooManyRequests, "rate_limit_error", quota.Code, message
}

func writeModelCallRejection(w http.ResponseWriter, path string, err error) {
	status, kind, code, message := modelCallRejection(err)
	// Some providers use a fixed error schema without a string code.
	w.Header().Set("X-Nebula-Error-Code", code)
	writeProtocolError(w, path, status, kind, code, message, "")
}

func modelCallErrorEvent(err error) map[string]any {
	_, kind, code, message := modelCallRejection(err)
	return map[string]any{"type": "error", "error": map[string]string{"type": kind, "code": code, "message": message}}
}
