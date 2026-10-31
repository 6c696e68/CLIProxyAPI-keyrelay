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

	keyShopPartnerPathPrefix           = "/keyshop/partner/v1"
	keyShopPartnerManagementPathPrefix = "/v0/management/plugins/key-shop/partner/v1"
	keyShopPartnerHeader               = "X-Key-Shop-Partner"
	keyShopPartnerClientIPHeader       = "X-Key-Shop-Client-IP"
)

// keyShopPartnerRoutes is the complete public partner API. Every other path
// under /keyshop/ must stay 404, so nothing may fall through to an admin route.
var keyShopPartnerRoutes = []struct {
	method string
	op     string
}{
	{http.MethodGet, "health"},
	{http.MethodGet, "plans"},
	{http.MethodPost, "mint"},
	{http.MethodPost, "confirm"},
	{http.MethodPost, "void"},
	{http.MethodPost, "status"},
	{http.MethodPost, "refs"},
}

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
	for _, route := range keyShopPartnerRoutes {
		path := keyShopPartnerPathPrefix + "/" + route.op
		s.engine.Handle(route.method, path, s.serveKeyShopPartnerAlias)
		// A trailing slash is not one of the seven exact paths. It must 404
		// rather than let Gin redirect it onto the partner route.
		s.engine.Handle(route.method, path+"/", keyShopPartnerNotFound)
	}
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

// serveKeyShopPartnerAlias forwards one exact partner op to the plugin
// management route without management auth. The partner header proves the
// request came through this alias, so every client-supplied variant is removed.
func (s *Server) serveKeyShopPartnerAlias(c *gin.Context) {
	if s == nil || s.pluginHost == nil || c == nil || c.Request == nil || c.Request.URL == nil {
		if c != nil {
			c.AbortWithStatus(http.StatusNotFound)
		}
		return
	}
	op := keyShopPartnerOperation(c.Request.Method, c.Request.URL.Path)
	if op == "" {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	deleteHeadersWithPrefix(c.Request.Header, keyShopPartnerHeader)
	deleteHeadersWithPrefix(c.Request.Header, keyShopPartnerClientIPHeader)
	// net/http keeps Host outside the header map, but the plugin reads Host
	// from the forwarded headers. Publish the original host so responses
	// without public_base_url use it.
	if host := strings.TrimSpace(c.Request.Host); host != "" {
		c.Request.Header.Set("Host", host)
	}
	c.Request.Header.Set(keyShopPartnerHeader, "1")
	c.Request.Header.Set(keyShopPartnerClientIPHeader, c.ClientIP())
	c.Request.URL.Path = keyShopPartnerManagementPathPrefix + "/" + op
	if !s.pluginHost.ServeManagementHTTP(c.Writer, c.Request) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Abort()
}

func keyShopPartnerOperation(method, path string) string {
	for _, route := range keyShopPartnerRoutes {
		if route.method == method && path == keyShopPartnerPathPrefix+"/"+route.op {
			return route.op
		}
	}
	return ""
}

func keyShopPartnerNotFound(c *gin.Context) {
	c.AbortWithStatus(http.StatusNotFound)
}

func deleteHeadersWithPrefix(headers http.Header, prefix string) {
	if headers == nil {
		return
	}
	folded := strings.ToLower(prefix)
	names := make([]string, 0, len(headers))
	for name := range headers {
		if strings.HasPrefix(strings.ToLower(name), folded) {
			names = append(names, name)
		}
	}
	for _, name := range names {
		delete(headers, name)
	}
}

func keyShopShouldRedirect(path, alias string) bool {
	return path == keyShopResourceDashboardPath && strings.TrimSpace(alias) != "1"
}
