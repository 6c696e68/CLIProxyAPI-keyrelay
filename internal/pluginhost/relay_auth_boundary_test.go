package pluginhost

import (
	"context"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestRelayAuthBoundaryUsesTokenFreeCoreSeam(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	request := coreauth.RelayRequest{
		TargetBaseURL: "https://relay.example/v1",
		Model:         "gpt-5.4",
		Method:        "GET",
		RelativePath:  "/models",
		ReplaySafe:    true,
	}
	policy := coreauth.RelayRequestPolicy{
		RotationMode:   "round-robin",
		PlusPreference: "ignore",
		MaxRetries:     0,
	}
	_, _ = manager.ExecuteRelayRequest(context.Background(), request, policy)
}
