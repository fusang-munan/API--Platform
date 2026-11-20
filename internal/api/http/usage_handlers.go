package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/controlplane"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func monthQuery(c *gin.Context) (time.Time, error) {
	return usage.ParseMonth(strings.TrimSpace(c.Query("month")), time.Now())
}

func reportRangeQuery(c *gin.Context) (string, bool) {
	value := c.DefaultQuery("range", "all")
	if value == "" {
		value = "all"
	}
	switch value {
	case "day", "month", "all":
		return value, true
	default:
		// 区间参数沿用统一错误 envelope，但按报表契约使用 422。
		c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{
			"error": gin.H{"code": domain.CodeValidation, "message": "range 须为 day、month 或 all",
				"details": []gin.H{{"field": "range", "reason": "invalid_enum"}}},
			"request_id": requestID(c),
		})
		return "", false
	}
}

func (s *Server) teacherModelUsage(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	value, ok := reportRangeQuery(c)
	if !ok {
		return
	}
	var providerID *uuid.UUID
	if raw := c.Query("provider_id"); raw != "" {
		id, err := controlplane.UUID(raw)
		if err != nil {
			writeError(c, err)
			return
		}
		providerID = &id
	}
	view, err := s.service.Beichen().ModelUsage(c.Request.Context(), providerID, value, time.Now())
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, view)
}

func (s *Server) studentUsage(c *gin.Context) {
	month, err := monthQuery(c)
	if err != nil {
		writeError(c, err)
		return
	}
	v, err := s.service.StudentUsage(c.Request.Context(), identity(c).UserID, month)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, v)
}

func studentRecentRangeQuery(c *gin.Context) (string, bool) {
	value := c.DefaultQuery("range", "today")
	if value == "" {
		value = "today"
	}
	switch value {
	case "today", "month", "all":
		return value, true
	default:
		c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{
			"error": gin.H{"code": domain.CodeValidation, "message": "range 须为 today、month 或 all",
				"details": []gin.H{{"field": "range", "reason": "invalid_enum"}}},
			"request_id": requestID(c),
		})
		return "", false
	}
}

func (s *Server) studentRecentCalls(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	value, ok := studentRecentRangeQuery(c)
	if !ok {
		return
	}
	start, err := usage.StudentRecentStart(value, time.Now())
	if err != nil {
		writeError(c, err)
		return
	}
	limit := 20
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > 100 {
			writeError(c, domain.NewError(domain.CodeValidation, "limit must be between 1 and 100"))
			return
		}
		limit = parsed
	}
	cursor, err := usage.DecodeCursor(c.Query("cursor"))
	if err != nil {
		writeError(c, err)
		return
	}
	v, err := s.service.StudentRecentCalls(c.Request.Context(), identity(c).UserID, start, cursor, limit)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, v)
}

func (s *Server) studentCallReceipt(c *gin.Context) {
	v, err := s.service.StudentCallReceipt(c.Request.Context(), identity(c).UserID, c.Param("request_id"))
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, v)
}

func (s *Server) mentorProjectUsage(c *gin.Context) {
	id, ok := pathID(c, "project_id")
	if !ok {
		return
	}
	month, err := monthQuery(c)
	if err != nil {
		writeError(c, err)
		return
	}
	v, err := s.service.MentorProjectUsage(c.Request.Context(), identity(c).UserID, id, month)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, v)
}

func (s *Server) updateMentorKeyQuota(c *gin.Context) {
	id, ok := pathID(c, "api_key_id")
	if !ok {
		return
	}
	var input struct {
		MonthlyCredits    string `json:"monthly_credits" binding:"required"`
		QuotaChangeReason string `json:"quota_change_reason"`
	}
	if !bindJSON(c, &input) {
		return
	}
	quota, err := domain.ParseCredits(input.MonthlyCredits)
	if err != nil {
		writeError(c, err)
		return
	}
	if err := s.service.UpdateMentorKeyQuota(c.Request.Context(), identity(c).UserID, id, quota, input.QuotaChangeReason); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) updateTeacherMemberQuota(c *gin.Context) {
	projectID, ok := pathID(c, "project_id")
	if !ok {
		return
	}
	userID, ok := pathID(c, "user_id")
	if !ok {
		return
	}
	var input struct {
		MonthlyCredits string `json:"monthly_credits" binding:"required"`
		Reason         string `json:"quota_change_reason" binding:"max=1000"`
		IncreaseOnly   bool   `json:"increase_only"`
	}
	if !bindJSON(c, &input) {
		return
	}
	quota, err := domain.ParseCredits(input.MonthlyCredits)
	if err == nil {
		err = s.service.UpdateTeacherMemberQuota(c.Request.Context(), identity(c).UserID, projectID, userID, quota, input.Reason, input.IncreaseOnly)
	}
	if err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
func parseLogFilter(c *gin.Context) (usage.LogFilter, error) {
	f := usage.LogFilter{Status: strings.TrimSpace(c.Query("status")), Limit: 50}
	if raw := c.Query("limit"); raw != "" {
		if _, err := fmt.Sscan(raw, &f.Limit); err != nil {
			return f, domain.NewError(domain.CodeValidation, "invalid limit")
		}
	}
	cur, err := usage.DecodeCursor(c.Query("cursor"))
	if err != nil {
		return f, err
	}
	f.Cursor = cur
	for name, destination := range map[string]**time.Time{"start": &f.Start, "end": &f.End} {
		if raw := strings.TrimSpace(c.Query(name)); raw != "" {
			parsed, parseErr := time.Parse(time.RFC3339, raw)
			if parseErr != nil {
				return f, domain.NewError(domain.CodeValidation, name+" must use RFC3339")
			}
			*destination = &parsed
		}
	}
	for name, dst := range map[string]**uuid.UUID{"project_id": &f.ProjectID, "user_id": &f.UserID, "api_key_id": &f.APIKeyID} {
		if raw := strings.TrimSpace(c.Query(name)); raw != "" {
			id, err := controlplane.UUID(raw)
			if err != nil {
				return f, err
			}
			*dst = &id
		}
	}
	if raw := strings.TrimSpace(c.Query("model_id")); raw != "" {
		f.ModelID = &raw
	}
	return f, nil
}
func (s *Server) mentorCallLogs(c *gin.Context) {
	f, err := parseLogFilter(c)
	if err != nil {
		writeError(c, err)
		return
	}
	v, err := s.service.MentorCallLogs(c.Request.Context(), identity(c).UserID, f)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, v)
}
func (s *Server) mentorCallLog(c *gin.Context) {
	id, ok := pathID(c, "call_id")
	if !ok {
		return
	}
	v, err := s.service.MentorCallLog(c.Request.Context(), identity(c).UserID, id)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, v)
}
func (s *Server) mentorCallTurns(c *gin.Context) {
	f, err := parseLogFilter(c)
	if err != nil {
		writeError(c, err)
		return
	}
	v, err := s.service.MentorCallTurns(c.Request.Context(), identity(c).UserID, f)
	if err != nil {
		writeError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	respond(c, http.StatusOK, v)
}
func (s *Server) mentorCallTurn(c *gin.Context) {
	id, ok := pathID(c, "turn_id")
	if !ok {
		return
	}
	v, err := s.service.MentorCallTurn(c.Request.Context(), identity(c).UserID, id)
	if err != nil {
		writeError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	respond(c, http.StatusOK, v)
}
func (s *Server) mentorInputs(c *gin.Context) {
	f, err := parseLogFilter(c)
	if err != nil {
		writeError(c, err)
		return
	}
	v, err := s.service.MentorInputs(c.Request.Context(), identity(c).UserID, usage.InputFilter{LogFilter: f, Query: c.Query("q")})
	if err != nil {
		writeError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	respond(c, http.StatusOK, v)
}
func (s *Server) mentorInput(c *gin.Context) {
	id, ok := pathID(c, "call_id")
	if !ok {
		return
	}
	v, err := s.service.MentorInput(c.Request.Context(), identity(c).UserID, id)
	if err != nil {
		writeError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	respond(c, http.StatusOK, v)
}
func (s *Server) teacherProjectUsage(c *gin.Context) {
	id, ok := pathID(c, "project_id")
	if !ok {
		return
	}
	month, err := monthQuery(c)
	if err != nil {
		writeError(c, err)
		return
	}
	v, err := s.service.TeacherProjectUsage(c.Request.Context(), id, month)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, v)
}

func (s *Server) teacherProjectSpend(c *gin.Context) {
	month, err := monthQuery(c)
	if err != nil {
		writeError(c, err)
		return
	}
	rows, err := s.service.TeacherProjectSpend(c.Request.Context(), month, c.Query("organization_id"), c.Query("q"))
	if err != nil {
		writeError(c, err)
		return
	}
	respondList(c, rows)
}
