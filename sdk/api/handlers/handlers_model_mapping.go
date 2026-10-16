package handlers

import (
	"bytes"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelmapping"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func (h *BaseAPIHandler) applyConfiguredModelMapping(modelName string, rawJSON []byte) (string, []byte, modelmapping.Result) {
	var rules []config.ModelMappingRule
	if h != nil {
		if cfg := h.CurrentConfig(); cfg != nil {
			rules = cfg.ModelMapping
		}
	}
	result := modelmapping.Apply(rules, modelName)
	if !result.Changed {
		return modelName, rawJSON, result
	}
	if len(rawJSON) > 0 && gjson.GetBytes(rawJSON, "model").Exists() {
		if updated, err := sjson.SetBytes(rawJSON, "model", result.Model); err == nil {
			rawJSON = updated
		}
	}
	return result.Model, rawJSON, result
}

func rewriteMappedResponseBody(body []byte, result modelmapping.Result) []byte {
	if !result.Changed || !result.ForceMapping || result.Original == "" {
		return body
	}
	return coreauth.RewriteResponseModel(body, result.Original)
}

func rewriteMappedStreamPayload(payload []byte, result modelmapping.Result) []byte {
	if !result.Changed || !result.ForceMapping || result.Original == "" || len(payload) == 0 {
		return payload
	}
	if bytes.Contains(payload, []byte("data:")) {
		return coreauth.RewriteSSEModel(payload, result.Original)
	}
	return coreauth.RewriteResponseModel(payload, result.Original)
}

func (h *BaseAPIHandler) mappingResultForOriginal(originalRequestedModel string) modelmapping.Result {
	_, _, result := h.applyConfiguredModelMapping(originalRequestedModel, nil)
	return result
}
