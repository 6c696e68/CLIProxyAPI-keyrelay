package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
)

func TestKeyShopShouldRedirectOnlyBareDashboard(t *testing.T) {
	if !keyShopShouldRedirect(keyShopResourceDashboardPath, "") {
		t.Fatal("bare dashboard should redirect")
	}
	if keyShopShouldRedirect(keyShopResourceDashboardPath, "1") {
		t.Fatal("alias request should not redirect")
	}
	if keyShopShouldRedirect(keyShopResourceSessionPath, "") {
		t.Fatal("session path should not redirect")
	}
}

func TestKeyShopAliasRewritesAndAborts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	host := pluginhost.New()
	srv := &Server{engine: gin.New(), pluginHost: host}
	srv.registerKeyShopPublicRoutes()
	req := httptest.NewRequest(http.MethodGet, keyShopDashboardPath, nil)
	rec := httptest.NewRecorder()
	srv.engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when plugin resource is absent", rec.Code)
	}
}

func TestKeyShopResourceRedirect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, keyShopResourceDashboardPath, nil)
	if keyShopShouldRedirect(c.Request.URL.Path, c.GetHeader(keyShopAliasHeader)) {
		c.Redirect(http.StatusFound, keyShopDashboardPath)
	}
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != keyShopDashboardPath {
		t.Fatalf("status=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}
}
