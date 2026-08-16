package handlers

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/tidwall/gjson"
)

func TestApplyConfiguredModelMapping_RewritesBodyAndEchoesOriginal(t *testing.T) {
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		ModelMapping: []config.ModelMappingRule{
			{Match: "gpt-5.4", Target: "gpt-5.5"},
		},
	}, nil)
	model, body, result := handler.applyConfiguredModelMapping("gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	if model != "gpt-5.5" || !result.Changed || !result.ForceMapping {
		t.Fatalf("unexpected mapping result model=%q result=%+v", model, result)
	}
	if got := gjson.GetBytes(body, "model").String(); got != "gpt-5.5" {
		t.Fatalf("request body model = %q, want gpt-5.5", got)
	}
	rewritten := rewriteMappedResponseBody([]byte(`{"model":"gpt-5.5"}`), result)
	if got := gjson.GetBytes(rewritten, "model").String(); got != "gpt-5.4" {
		t.Fatalf("response model = %q, want gpt-5.4", got)
	}
}

func TestApplyConfiguredModelMapping_NoRules(t *testing.T) {
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	model, body, result := handler.applyConfiguredModelMapping("gpt-5.4", []byte(`{"model":"gpt-5.4"}`))
	if result.Changed || model != "gpt-5.4" {
		t.Fatalf("unexpected result model=%q result=%+v", model, result)
	}
	if got := gjson.GetBytes(body, "model").String(); got != "gpt-5.4" {
		t.Fatalf("body model = %q", got)
	}
}
