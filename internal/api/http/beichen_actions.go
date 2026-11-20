package httpapi

import (
	"math/big"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/beichen"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

func (s *Server) requestBeichenProbes(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	provider, ok := pathID(c, "provider_id")
	if !ok || !bindBeichenJSON(c, &struct{}{}, true) {
		return
	}
	run, err := s.service.Beichen().RequestProbes(c.Request.Context(), provider)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusAccepted, run)
}

func (s *Server) latestBeichenProbes(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	provider, ok := pathID(c, "provider_id")
	if !ok {
		return
	}
	view, err := s.service.Beichen().LatestProbes(c.Request.Context(), provider)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, view)
}

func (s *Server) enableBeichenModelsBatch(c *gin.Context) {
	provider, ok := pathID(c, "provider_id")
	if !ok {
		return
	}
	var input struct {
		Models []string `json:"models"`
	}
	if !bindBeichenJSON(c, &input, false) {
		return
	}
	if len(input.Models) == 0 || len(input.Models) > 200 {
		writeError(c, domain.NewError(domain.CodeValidation, "每批请提交 1 至 200 个模型"))
		return
	}
	result, err := s.service.Beichen().EnableModelsBatch(c.Request.Context(), provider, identity(c).UserID, input.Models)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, result)
}

func (s *Server) setBeichenModelPrice(c *gin.Context) {
	provider, ok := pathID(c, "provider_id")
	if !ok {
		return
	}
	var input struct {
		beichen.ModelPriceScope
		Model string `json:"model"`
		Mode  string `json:"mode"`
		Rates *struct {
			Input      string  `json:"input"`
			Output     string  `json:"output"`
			CacheRead  *string `json:"cache_read"`
			CacheWrite *string `json:"cache_write"`
		} `json:"rates"`
	}
	if !bindBeichenJSON(c, &input, false) {
		return
	}
	price := beichen.PriceInput{Mode: input.Mode}
	if input.Mode != "manual" && input.Mode != "follow" || input.Mode == "follow" && input.Rates != nil || input.Mode == "manual" && input.Rates == nil {
		writeError(c, domain.NewError(domain.CodeValidation, "请选择跟随官网或手动定价；仅手动定价可提交单价"))
		return
	}
	if input.Rates != nil {
		// 只接收普通十进制字符串，以有理数精确缩放，拒绝浮点舍入、分数和科学记数法。
		parseRate := func(value string) (*int64, bool) {
			if len(value) == 0 || len(value) > 64 || strings.Count(value, ".") > 1 || value[0] == '.' || value[len(value)-1] == '.' {
				return nil, false
			}
			for _, ch := range value {
				if ch != '.' && (ch < '0' || ch > '9') {
					return nil, false
				}
			}
			rate, ok := new(big.Rat).SetString(value)
			if !ok {
				return nil, false
			}
			rate.Mul(rate, big.NewRat(1_000_000, 1))
			if !rate.IsInt() || !rate.Num().IsInt64() {
				return nil, false
			}
			result := rate.Num().Int64()
			return &result, result >= 0 && result <= 1_000_000_000_000
		}
		read, write := "0", "0"
		if input.Rates.CacheRead != nil {
			read = *input.Rates.CacheRead
		}
		if input.Rates.CacheWrite != nil {
			write = *input.Rates.CacheWrite
		}
		rates := &beichen.Rates{}
		for i, destination := range []**int64{&rates.Input, &rates.Output, &rates.CacheRead, &rates.CacheWrite} {
			value, valid := parseRate([]string{input.Rates.Input, input.Rates.Output, read, write}[i])
			if !valid {
				writeError(c, domain.NewError(domain.CodeValidation, "单价须为非负十进制积分，精确到百万分之一积分，且不超过 1000000 积分/百万 Token"))
				return
			}
			*destination = value
		}
		price.Rates = rates
	}
	credentials, err := s.service.Beichen().SetModelPrice(c.Request.Context(), provider, identity(c).UserID, input.Model, price, input.ModelPriceScope)
	if err != nil {
		writeError(c, err)
		return
	}
	applied := make([]uuid.UUID, len(credentials))
	for i, credential := range credentials {
		applied[i] = credential.CredentialID
	}
	respond(c, http.StatusOK, gin.H{"saved": true, "credentials": credentials, "applied_credential_ids": applied})
}
