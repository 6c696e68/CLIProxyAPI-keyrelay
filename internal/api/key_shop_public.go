package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	keyShopDashboardPath         = "/client"
	keyShopSessionPath           = "/client/api/session"
	keyShopResourceDashboardPath = "/v0/resource/plugins/key-shop/dashboard"
	keyShopResourceSessionPath   = "/v0/resource/plugins/key-shop/api/session"
	keyShopAliasHeader           = "X-Key-Shop-Alias"
)

func (s *Server) registerKeyShopPublicRoutes() {
	if s == nil || s.engine == nil {
		return
	}
	s.engine.GET(keyShopDashboardPath, s.serveKeyShopAlias)
	s.engine.GET(keyShopSessionPath, s.serveKeyShopAlias)
	s.engine.GET("/key-dashboard", func(c *gin.Context) {
		c.Redirect(http.StatusFound, keyShopDashboardPath)
	})
	s.engine.GET("/key-dashboard/api/session", s.serveKeyShopAlias)
}

func (s *Server) serveKeyShopAlias(c *gin.Context) {
	if s == nil || s.pluginHost == nil || c == nil || c.Request == nil || c.Request.URL == nil {
		if c != nil {
			c.AbortWithStatus(http.StatusNotFound)
		}
		return
	}
	rewritten := keyShopResourceDashboardPath
	if c.Request.URL.Path == keyShopSessionPath || c.Request.URL.Path == "/key-dashboard/api/session" {
		rewritten = keyShopResourceSessionPath
	}
	c.Request.Header.Set(keyShopAliasHeader, "1")
	c.Request.URL.Path = rewritten
	if !s.pluginHost.ServeResourceHTTP(c.Writer, c.Request) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Abort()
}

func keyShopShouldRedirect(path, alias string) bool {
	return path == keyShopResourceDashboardPath && strings.TrimSpace(alias) != "1"
}
