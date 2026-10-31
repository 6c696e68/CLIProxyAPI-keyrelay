package openai

import (
	codexmodels "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/models"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	responsesclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/responses"
)

// codexClientModelsResponse builds the Codex client model list. The caller may
// pass the already-filtered models, and the no-arg form keeps the full registry
// list. Both a string client version and a model slice are accepted so callers
// that only vary the version stay valid.
func (h *OpenAIAPIHandler) codexClientModelsResponse(args ...any) map[string]any {
	version := ""
	var models []map[string]any
	hasModels := false
	for _, arg := range args {
		switch value := arg.(type) {
		case string:
			version = value
		case []map[string]any:
			models = value
			hasModels = true
		}
	}
	if !hasModels {
		models = h.Models()
	}
	optimizeMultiAgentV2 := false
	var applyPatchCapabilityForModel codexmodels.ApplyPatchCapabilityForModelFunc
	if h != nil {
		if cfg := h.CurrentConfig(); cfg != nil {
			optimizeMultiAgentV2 = cfg.Client.Codex.OptimizeMultiAgentV2
			if cfg.Client.Codex.EnableApplyPatch {
				applyPatchCapabilityForModel = h.SupportsApplyPatchModel
			}
		}
	}
	var toolSearchCapabilityForModel codexmodels.ToolSearchCapabilityForModelFunc
	if h != nil && responsesclaude.ClientToolSearchEnabled() {
		toolSearchCapabilityForModel = h.SupportsNativeToolSearchModel
	}
	modelRegistry := registry.GetGlobalRegistry()
	return codexmodels.BuildResponseForClientWithToolCapabilities(models, modelRegistry.GetModelProviders, modelRegistry.GetResponsesWebSearchCapability, applyPatchCapabilityForModel, toolSearchCapabilityForModel, optimizeMultiAgentV2, version)
}

// CodexClientModelsResponse builds a Codex client model response.
func CodexClientModelsResponse(models []map[string]any) map[string]any {
	return codexmodels.BuildResponse(models, nil, false)
}

// CodexClientModelsResponseWithMultiAgentV2 builds a Codex client model response
// and advertises multi-agent v2 for synthesized models when enabled.
func CodexClientModelsResponseWithMultiAgentV2(models []map[string]any, enabled bool) map[string]any {
	return codexmodels.BuildResponse(models, nil, enabled)
}

// CodexClientModelsResponseForClient builds a Codex client model response
// tailored for a specific client version.
func CodexClientModelsResponseForClient(models []map[string]any, clientVersion string, enabled bool) map[string]any {
	return codexmodels.BuildResponseForClient(models, nil, enabled, clientVersion)
}
