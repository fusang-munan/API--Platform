package httpapi

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/starprince1234/Nebula-api/internal/catalog"
)

func (s *Server) beichenCapabilities(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	providerID, ok := pathID(c, "provider_id")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	database, _ := s.health.Database.(*sql.DB)
	view, err := catalog.ReadBeichenCapabilities(ctx, database, providerID)
	if err != nil {
		writeError(c, err)
		return
	}
	respond(c, http.StatusOK, view)
}
