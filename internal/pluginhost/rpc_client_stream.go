package pluginhost

import (
	"context"
	"fmt"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func (a *rpcPluginAdapter) ExecuteStream(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
	if a == nil || a.host == nil || a.host.streams == nil {
		return pluginapi.ExecutorStreamResponse{}, fmt.Errorf("plugin stream bridge is unavailable")
	}
	ctx = withHostCallbackIdentity(ctx, a.id, a.instance)
	streamID, chunks, cleanupStream := a.host.streams.open(ctx)
	callbackID, closeCallback := a.openHostCallbackContext(ctx)
	cleanup := combinedCleanup(cleanupStream, closeCallback)
	a.host.streams.mu.Lock()
	streamState := a.host.streams.streams[streamID]
	a.host.streams.mu.Unlock()
	if streamState == nil {
		cleanup()
		return pluginapi.ExecutorStreamResponse{}, context.Canceled
	}
	rpcReq := rpcExecutorRequest{
		ExecutorRequest: req,
		StreamID:        streamID,
		HostCallbackID:  callbackID,
	}
	resp, errCall := callPlugin[rpcExecutorStreamResponse](ctx, a.client, pluginabi.MethodExecutorExecuteStream, rpcReq)
	if errCall != nil {
		cleanup()
		return pluginapi.ExecutorStreamResponse{}, errCall
	}
	if len(resp.Chunks) > 0 {
		cleanup()
		out := make(chan pluginapi.ExecutorStreamChunk, len(resp.Chunks))
		for _, chunk := range resp.Chunks {
			out <- chunk
		}
		close(out)
		return pluginapi.ExecutorStreamResponse{Headers: resp.Headers, Chunks: out}, nil
	}
	// Async streaming plugins can return before they finish emitting chunks, so keep callbacks alive until the stream ends.
	return pluginapi.ExecutorStreamResponse{
		Headers: resp.Headers,
		Chunks:  cleanupWhenStreamDone(ctx, chunks, streamState.abort, cleanup),
	}, nil
}

func combinedCleanup(cleanups ...func()) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			for _, cleanup := range cleanups {
				if cleanup != nil {
					cleanup()
				}
			}
		})
	}
}

func cleanupWhenStreamDone(ctx context.Context, chunks <-chan pluginapi.ExecutorStreamChunk, aborted <-chan struct{}, cleanup func()) <-chan pluginapi.ExecutorStreamChunk {
	out := make(chan pluginapi.ExecutorStreamChunk)
	go func() {
		defer func() {
			if cleanup != nil {
				cleanup()
			}
			close(out)
		}()
		var done <-chan struct{}
		if ctx != nil {
			done = ctx.Done()
		}
		for {
			select {
			case <-aborted:
				return
			case <-done:
				return
			case chunk, ok := <-chunks:
				if !ok {
					return
				}
				select {
				case out <- chunk:
				case <-done:
					return
				case <-aborted:
					return
				}
			}
		}
	}()
	return out
}
