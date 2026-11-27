package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/controlplane"
	"github.com/starprince1234/Nebula-api/internal/domain"
	"github.com/starprince1234/Nebula-api/internal/usage"
)

func teacherStudentUsageScope(c *gin.Context) (usage.TeacherStudentUsageScope, error) {
	month, err := monthQuery(c)
	if err != nil {
		return usage.TeacherStudentUsageScope{}, err
	}
	scope := usage.TeacherStudentUsageScope{Month: month}
	if raw := c.Query("project_id"); raw != "" {
		projectID, err := controlplane.UUID(raw)
		if err != nil || projectID == uuid.Nil {
			return scope, domain.NewError(domain.CodeValidation, "请选择有效的项目")
		}
		scope.ProjectID = uuid.NullUUID{UUID: projectID, Valid: true}
	}
	return scope, nil
}

func teacherStudentUsagePageValue(c *gin.Context, field string, fallback, max int) (int, error) {
	raw := c.Query(field)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > max {
		return 0, &domain.Error{Code: domain.CodeValidation, Message: "分页参数无效",
			Details: []map[string]string{{"field": field, "reason": fmt.Sprintf("须为 1 到 %d 之间的整数", max)}}}
	}
	return value, nil
}

func (s *Server) listTeacherStudentUsage(c *gin.Context) {
	scope, err := teacherStudentUsageScope(c)
	if err != nil {
		writeError(c, err)
		return
	}
	search := strings.TrimSpace(c.Query("search"))
	if !utf8.ValidString(search) || utf8.RuneCountInString(search) > 128 || strings.ContainsRune(search, 0) {
		writeError(c, domain.NewError(domain.CodeValidation, "搜索内容需为 128 字以内的有效文本"))
		return
	}
	page, err := teacherStudentUsagePageValue(c, "page", 1, 1<<31-1)
	if err != nil {
		writeError(c, err)
		return
	}
	pageSize, err := teacherStudentUsagePageValue(c, "page_size", 20, 100)
	if err != nil {
		writeError(c, err)
		return
	}
	result, err := s.service.ListTeacherStudentUsage(c.Request.Context(), usage.TeacherStudentUsageQuery{
		TeacherStudentUsageScope: scope, Search: search, Page: page, PageSize: pageSize,
		Sort: c.Query("sort"), Order: c.Query("order"),
	})
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, result)
}

func (s *Server) teacherStudentUsageDetail(c *gin.Context) {
	userID, ok := pathID(c, "user_id")
	if !ok {
		return
	}
	scope, err := teacherStudentUsageScope(c)
	if err != nil {
		writeError(c, err)
		return
	}
	result, err := s.service.TeacherStudentUsage(c.Request.Context(), userID, scope)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, result)
}
