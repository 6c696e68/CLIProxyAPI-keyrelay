package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
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

// keyShopPartnerRecorder captures what the plugin management handler receives.
type keyShopPartnerRecorder struct {
	mu       sync.Mutex
	requests []pluginapi.ManagementRequest
}

func (r *keyShopPartnerRecorder) HandleManagement(_ context.Context, req pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
	r.mu.Lock()
	r.requests = append(r.requests, pluginapi.ManagementRequest{
		Method: req.Method, Path: req.Path, Headers: req.Headers.Clone(), Query: req.Query, Body: append([]byte(nil), req.Body...),
	})
	r.mu.Unlock()
	return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Body: []byte("ok")}, nil
}

func (r *keyShopPartnerRecorder) snapshot() []pluginapi.ManagementRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]pluginapi.ManagementRequest, len(r.requests))
	copy(out, r.requests)
	return out
}

type keyShopPartnerPlugin struct {
	routes []pluginapi.ManagementRoute
}

func (p *keyShopPartnerPlugin) RegisterManagement(context.Context, pluginapi.ManagementRegistrationRequest) (pluginapi.ManagementRegistrationResponse, error) {
	return pluginapi.ManagementRegistrationResponse{Routes: p.routes}, nil
}

// newKeyShopPartnerHost registers the real partner route table plus one admin
// route, so a leaked path would be observable instead of a plain 404.
func newKeyShopPartnerHost(t *testing.T, partner, admin *keyShopPartnerRecorder) *pluginhost.Host {
	t.Helper()
	routes := make([]pluginapi.ManagementRoute, 0, len(keyShopPartnerRoutes)+1)
	for _, route := range keyShopPartnerRoutes {
		routes = append(routes, pluginapi.ManagementRoute{
			Method:  route.method,
			Path:    "/plugins/key-shop/partner/v1/" + route.op,
			Handler: partner,
		})
	}
	routes = append(routes, pluginapi.ManagementRoute{
		Method: http.MethodGet, Path: "/plugins/key-shop/keys", Handler: admin,
	})
	host := pluginhost.New()
	host.RegisterPluginForTest("key-shop", pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
		ManagementAPI: &keyShopPartnerPlugin{routes: routes},
	}})
	host.RegisterManagementRoutes(t.Context(), nil)
	return host
}

func newKeyShopPartnerServer(host *pluginhost.Host) *Server {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	_ = engine.SetTrustedProxies(nil)
	srv := &Server{engine: engine, pluginHost: host}
	engine.NoRoute(srv.pluginManagementNoRoute)
	srv.registerKeyShopPublicRoutes()
	return srv
}

func TestKeyShopPartnerAliasForwardsOnlySevenOps(t *testing.T) {
	partner := &keyShopPartnerRecorder{}
	admin := &keyShopPartnerRecorder{}
	srv := newKeyShopPartnerServer(newKeyShopPartnerHost(t, partner, admin))
	body := []byte(`{"ref":"ref-0001","note":" Giữ nguyên "}`)
	for _, route := range keyShopPartnerRoutes {
		var reader *bytes.Reader
		if route.method == http.MethodPost {
			reader = bytes.NewReader(body)
		} else {
			reader = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(route.method, keyShopPartnerPathPrefix+"/"+route.op, reader)
		req.Host = "api.shop.example:8443"
		req.RemoteAddr = "203.0.113.9:5555"
		req.Header.Set(keyShopPartnerHeader, "0")
		req.Header["x-key-shop-partner"] = []string{"0"}
		req.Header["x-key-shop-partner-role"] = []string{"admin"}
		req.Header["X-KEY-SHOP-PARTNER-FORWARD"] = []string{"1"}
		req.Header.Set(keyShopPartnerClientIPHeader, "6.6.6.6")
		req.Header["x-key-shop-client-ip"] = []string{"6.6.6.6"}
		rec := httptest.NewRecorder()
		srv.engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s status=%d body=%s", route.method, route.op, rec.Code, rec.Body.String())
		}
	}
	requests := partner.snapshot()
	if len(requests) != len(keyShopPartnerRoutes) {
		t.Fatalf("forwarded %d requests, want %d", len(requests), len(keyShopPartnerRoutes))
	}
	for i, route := range keyShopPartnerRoutes {
		request := requests[i]
		if request.Method != route.method || request.Path != keyShopPartnerManagementPathPrefix+"/"+route.op {
			t.Fatalf("request[%d] method=%s path=%s", i, request.Method, request.Path)
		}
		values := request.Headers.Values(keyShopPartnerHeader)
		if len(values) != 1 || values[0] != "1" {
			t.Fatalf("request[%d] partner header=%v", i, values)
		}
		for name := range request.Headers {
			if strings.HasPrefix(strings.ToLower(name), strings.ToLower(keyShopPartnerHeader)) && !strings.EqualFold(name, keyShopPartnerHeader) {
				t.Fatalf("request[%d] spoofed header %q survived", i, name)
			}
		}
		if got := request.Headers.Get(keyShopPartnerClientIPHeader); got != "203.0.113.9" {
			t.Fatalf("request[%d] client ip=%q", i, got)
		}
		if got := request.Headers.Get("Host"); got != "api.shop.example:8443" {
			t.Fatalf("request[%d] host=%q", i, got)
		}
		if values := request.Headers.Values(keyShopPartnerClientIPHeader); len(values) != 1 {
			t.Fatalf("request[%d] client ip values=%v", i, values)
		}
		if route.method == http.MethodPost && !bytes.Equal(request.Body, body) {
			t.Fatalf("request[%d] body=%q want %q", i, request.Body, body)
		}
	}
	if forwarded := admin.snapshot(); len(forwarded) != 0 {
		t.Fatalf("admin route was reached %d time(s)", len(forwarded))
	}
}

func TestKeyShopPartnerAliasSkipsManagementAvailability(t *testing.T) {
	partner := &keyShopPartnerRecorder{}
	admin := &keyShopPartnerRecorder{}
	srv := newKeyShopPartnerServer(newKeyShopPartnerHost(t, partner, admin))
	// Management auth and availability must not be consulted for the alias.
	srv.cfg = &config.Config{Home: config.HomeConfig{Enabled: true}}
	srv.managementRoutesEnabled.Store(false)
	req := httptest.NewRequest(http.MethodGet, keyShopPartnerPathPrefix+"/health", nil)
	req.Header.Set(keyShopPartnerHeader, "1")
	rec := httptest.NewRecorder()
	srv.engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(partner.snapshot()) != 1 {
		t.Fatalf("partner handler was not called")
	}
}

func TestKeyShopPartnerAliasUsesGenericBodyCap(t *testing.T) {
	partner := &keyShopPartnerRecorder{}
	admin := &keyShopPartnerRecorder{}
	host := newKeyShopPartnerHost(t, partner, admin)
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	_ = engine.SetTrustedProxies(nil)
	cfg := &config.Config{}
	cfg.MaxRequestBodyBytes = 32
	srv := &Server{engine: engine, pluginHost: host, cfg: cfg}
	engine.Use(srv.maxRequestBodyBytesMiddleware())
	srv.registerKeyShopPublicRoutes()
	body := bytes.Repeat([]byte("x"), 64)
	req := httptest.NewRequest(http.MethodPost, keyShopPartnerPathPrefix+"/mint", bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.Header.Set(keyShopPartnerHeader, "1")
	rec := httptest.NewRecorder()
	srv.engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d want 413 body=%s", rec.Code, rec.Body.String())
	}
	if len(partner.snapshot()) != 0 {
		t.Fatal("the oversized body reached the plugin")
	}
}

func TestKeyShopPartnerAliasRejectsOtherPaths(t *testing.T) {
	partner := &keyShopPartnerRecorder{}
	admin := &keyShopPartnerRecorder{}
	srv := newKeyShopPartnerServer(newKeyShopPartnerHost(t, partner, admin))
	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"unknown op", http.MethodPost, keyShopPartnerPathPrefix + "/keys"},
		{"dot dot", http.MethodPost, keyShopPartnerPathPrefix + "/../keys"},
		{"wrong method get", http.MethodGet, keyShopPartnerPathPrefix + "/mint"},
		{"wrong method post", http.MethodPost, keyShopPartnerPathPrefix + "/health"},
		{"trailing slash", http.MethodPost, keyShopPartnerPathPrefix + "/mint/"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set(keyShopPartnerHeader, "1")
		rec := httptest.NewRecorder()
		srv.engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status=%d want 404 body=%s", tc.name, rec.Code, rec.Body.String())
		}
	}
	if len(partner.snapshot()) != 0 || len(admin.snapshot()) != 0 {
		t.Fatalf("rejected paths reached a handler: partner=%d admin=%d", len(partner.snapshot()), len(admin.snapshot()))
	}
}

func TestKeyShopPartnerAliasMissingRoute404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	host := pluginhost.New()
	srv := newKeyShopPartnerServer(host)
	req := httptest.NewRequest(http.MethodPost, keyShopPartnerPathPrefix+"/mint", strings.NewReader("{}"))
	req.Header.Set(keyShopPartnerHeader, "1")
	rec := httptest.NewRecorder()
	srv.engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", rec.Code)
	}
}
