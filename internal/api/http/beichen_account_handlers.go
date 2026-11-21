package httpapi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/beichen"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

func beichenAccountProvider(c *gin.Context) (uuid.UUID, bool) {
	c.Header("Cache-Control", "private, no-store")
	id, err := uuid.Parse(c.Param("provider_id"))
	if err != nil || id == uuid.Nil {
		writeError(c, domain.NewError(domain.CodeValidation, "请输入有效的北辰供应商 ID"))
		return uuid.Nil, false
	}
	return id, true
}

func respondBeichenAccount(c *gin.Context, data any, err error) {
	if err == nil {
		respond(c, http.StatusOK, data)
		return
	}
	var appErr *domain.Error
	if errors.As(err, &appErr) {
		writeError(c, domain.NewError(appErr.Code, appErr.Message))
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		writeError(c, domain.NewError(domain.CodeDependencyUnavailable, "北辰网页登录操作已超时或取消，请重试"))
		return
	}
	writeError(c, domain.NewError(domain.CodeDependencyUnavailable, "北辰网页登录操作未完成，请稍后重试"))
}

func (s *Server) beichenAccountStatus(c *gin.Context) {
	if provider, ok := beichenAccountProvider(c); ok {
		status, err := s.service.Beichen().PanelAccountStatus(c.Request.Context(), provider)
		respondBeichenAccount(c, status, err)
	}
}

func (s *Server) beichenAccountLogin(c *gin.Context) {
	provider, ok := beichenAccountProvider(c)
	if !ok {
		return
	}
	if !panelManagementHTTPS(c.Request, s.config.AllowedOrigin) {
		writeError(c, domain.NewError(domain.CodeValidation, "请先配置可信 HTTPS 管理入口，再提交北辰凭证"))
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10))
	decoder.DisallowUnknownFields()
	var input beichen.PanelAccountLogin
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		writeError(c, domain.NewError(domain.CodeValidation, "北辰网页登录请求格式无效"))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 35*time.Second)
	defer cancel()
	status, err := s.service.Beichen().LoginPanelAccount(ctx, provider, identity(c).UserID, input)
	respondBeichenAccount(c, status, err)
}

func (s *Server) beichenAccountVerify(c *gin.Context) {
	if provider, ok := beichenAccountProvider(c); ok {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 35*time.Second)
		defer cancel()
		status, err := s.service.Beichen().VerifyPanelAccount(ctx, provider)
		respondBeichenAccount(c, status, err)
	}
}

func (s *Server) beichenAccountClear(c *gin.Context) {
	if provider, ok := beichenAccountProvider(c); ok {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 35*time.Second)
		defer cancel()
		status, err := s.service.Beichen().ClearPanelAccount(ctx, provider, identity(c).UserID)
		respondBeichenAccount(c, status, err)
	}
}

func (s *Server) beichenAccountCrawl(c *gin.Context) {
	if provider, ok := beichenAccountProvider(c); ok {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 35*time.Second)
		defer cancel()
		summary, err := s.service.Beichen().CrawlPanelDirectory(ctx, provider)
		respondBeichenAccount(c, summary, err)
	}
}

func (s *Server) beichenAccountExport(c *gin.Context) {
	provider, ok := beichenAccountProvider(c)
	if !ok {
		return
	}
	if !panelManagementHTTPS(c.Request, s.config.AllowedOrigin) {
		writeError(c, domain.NewError(domain.CodeValidation, "请使用可信 HTTPS 管理入口导出本机配置"))
		return
	}
	var input struct {
		Confirm bool `json:"confirm"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || !input.Confirm {
		writeError(c, domain.NewError(domain.CodeValidation, "请先确认导出北辰本机配置"))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	config, err := s.service.Beichen().ExportPanelConfiguration(ctx, provider, identity(c).UserID, s.config.AllowedOrigin)
	if err != nil {
		respondBeichenAccount(c, nil, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="beichen-session.env"`)
	c.Data(http.StatusOK, "application/octet-stream", []byte(config.Env()))
}

func (s *Server) beichenLocalSession(c *gin.Context) {
	provider, ok := beichenAccountProvider(c)
	if !ok {
		return
	}
	grant, hasGrant := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer nblbc_")
	if !hasGrant || !panelManagementHTTPS(c.Request, s.config.AllowedOrigin) {
		writeError(c, domain.NewError(domain.CodeForbidden, "需要使用 HTTPS 和有效的北辰本机授权"))
		return
	}
	var input struct {
		Rejected string `json:"rejected_token_sha256"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		writeError(c, domain.NewError(domain.CodeValidation, "北辰本机会话请求格式无效"))
		return
	}
	if input.Rejected != "" {
		digest, err := hex.DecodeString(input.Rejected)
		if err != nil || len(digest) != 32 {
			writeError(c, domain.NewError(domain.CodeValidation, "北辰会话校验值无效"))
			return
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 32*time.Second)
	defer cancel()
	session, err := s.service.Beichen().ReadSharedPanelSession(ctx, provider, "nblbc_"+grant, input.Rejected)
	respondBeichenAccount(c, session, err)
}
