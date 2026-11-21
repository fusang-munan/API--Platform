package httpapi

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
)

func (s *Server) beichenWebsiteKeys(c *gin.Context) {
	if provider, ok := beichenAccountProvider(c); ok {
		view, err := s.service.Beichen().WebsiteKeys(c.Request.Context(), provider)
		respondBeichenAccount(c, view, err)
	}
}

func (s *Server) refreshBeichenWebsiteKeys(c *gin.Context) {
	if provider, ok := beichenAccountProvider(c); ok {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 35*time.Second)
		defer cancel()
		view, err := s.service.Beichen().RefreshWebsiteKeys(ctx, provider)
		respondBeichenAccount(c, view, err)
	}
}
