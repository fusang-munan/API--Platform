package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/starprince1234/Nebula-api/internal/beichen"
	"github.com/starprince1234/Nebula-api/internal/controlplane"
	"github.com/starprince1234/Nebula-api/internal/domain"
)

func (s *Server) beichenChannelSwitch(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	provider, ok := pathID(c, "provider_id")
	if !ok {
		return
	}
	var scope beichen.CredentialSwitch
	query := c.Request.URL.Query()
	for name, target := range map[string]*uuid.UUID{"from_credential_id": &scope.From, "to_credential_id": &scope.To} {
		if values, exists := query[name]; exists {
			if len(values) != 1 {
				writeError(c, domain.NewError(domain.CodeValidation, "每次只能选择一个原渠道和一个接替渠道"))
				return
			}
			id, err := controlplane.UUID(values[0])
			if err == nil && id == uuid.Nil {
				err = domain.NewError(domain.CodeValidation, "需要有效的渠道 ID")
			}
			if err != nil {
				writeError(c, err)
				return
			}
			*target = id
		}
	}
	view, err := s.service.Beichen().ChannelSwitch(c.Request.Context(), provider, scope)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, view)
}

func (s *Server) confirmBeichenChannelSwitch(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	provider, ok := pathID(c, "provider_id")
	if !ok {
		return
	}
	var input struct {
		beichen.CredentialSwitch
		Token string `json:"preview_token"`
	}
	if !bindBeichenJSON(c, &input, false) {
		return
	}
	plan, err := s.service.Beichen().ConfirmCredentialSwitch(c.Request.Context(), provider, identity(c).UserID, input.CredentialSwitch, input.Token)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, plan)
}

func (s *Server) beichenModelAliases(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	provider, ok := pathID(c, "provider_id")
	if !ok {
		return
	}
	if c.Request.Method == http.MethodGet {
		aliases, err := s.service.Beichen().ModelAliases(c.Request.Context(), provider)
		if err != nil {
			writeError(c, err)
			return
		}
		respond(c, http.StatusOK, aliases)
		return
	}
	var input struct {
		CredentialID  json.RawMessage `json:"credential_id"`
		UpstreamModel string          `json:"upstream_model"`
		SchoolModel   json.RawMessage `json:"school_model"`
	}
	if !bindBeichenJSON(c, &input, false) {
		return
	}
	var credential *uuid.UUID
	var school *string
	if json.Unmarshal(input.CredentialID, &credential) != nil || json.Unmarshal(input.SchoolModel, &school) != nil {
		writeError(c, domain.NewError(domain.CodeValidation, "请显式提供渠道和校内模型，清除别名时将校内模型设为 null"))
		return
	}
	if err := s.service.Beichen().PutModelAlias(c.Request.Context(), provider, identity(c).UserID, credential, input.UpstreamModel, school); err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, gin.H{"saved": true})
}

func (s *Server) rebindBeichenModels(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	provider, ok := pathID(c, "provider_id")
	if !ok {
		return
	}
	var input struct {
		Preview *bool  `json:"preview"`
		Token   string `json:"preview_token"`
	}
	if !bindBeichenJSON(c, &input, true) {
		return
	}
	if values, ok := c.Request.URL.Query()["preview"]; ok {
		if len(values) != 1 || (values[0] != "true" && values[0] != "false") {
			writeError(c, domain.NewError(domain.CodeValidation, "preview 必须为 true 或 false"))
			return
		}
		preview := values[0] == "true"
		if input.Preview != nil && *input.Preview != preview {
			writeError(c, domain.NewError(domain.CodeValidation, "查询参数和请求体中的 preview 必须一致"))
			return
		}
		input.Preview = &preview
	}
	if input.Preview == nil {
		writeError(c, domain.NewError(domain.CodeValidation, "请指定是预览还是确认重建"))
		return
	}
	plan, err := s.service.Beichen().Rebind(c.Request.Context(), provider, identity(c).UserID, *input.Preview, input.Token)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, plan)
}

func (s *Server) rescanBeichenCatalog(c *gin.Context) {
	provider, ok := pathID(c, "provider_id")
	if !ok {
		return
	}
	var input struct {
		CredentialID string `json:"credential_id"`
	}
	if !bindBeichenJSON(c, &input, false) {
		return
	}
	credential, err := controlplane.UUID(input.CredentialID)
	if err == nil {
		err = s.service.Beichen().RescanCatalog(c.Request.Context(), provider, credential)
	}
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, gin.H{"synced": true})
}
