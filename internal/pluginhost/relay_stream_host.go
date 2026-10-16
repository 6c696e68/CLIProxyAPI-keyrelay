package pluginhost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
)

type relayHostStreamKey struct {
	pluginID string
	instance *hostCallbackInstance
	id       string
}

type relayHostStreamEntry struct {
	readMu    sync.Mutex
	body      io.ReadCloser
	cancel    context.CancelFunc
	finish    func()
	closeOnce sync.Once
}

type relayHostStreamBridge struct {
	mu      sync.Mutex
	streams map[relayHostStreamKey]*relayHostStreamEntry
}

func newRelayHostStreamBridge() *relayHostStreamBridge {
	return &relayHostStreamBridge{streams: make(map[relayHostStreamKey]*relayHostStreamEntry)}
}

func (b *relayHostStreamBridge) open(pluginID string, instance *hostCallbackInstance, body io.ReadCloser, cancel context.CancelFunc, finish func()) string {
	entry := &relayHostStreamEntry{body: body, cancel: cancel, finish: finish}
	if b == nil || body == nil {
		closeRelayHostStreamEntry(entry)
		return ""
	}
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		closeRelayHostStreamEntry(entry)
		return ""
	}
	id := hex.EncodeToString(nonce[:])
	key := relayHostStreamKey{pluginID: strings.TrimSpace(pluginID), instance: instance, id: id}
	b.mu.Lock()
	if instance != nil && instance.closed.Load() {
		b.mu.Unlock()
		closeRelayHostStreamEntry(entry)
		return ""
	}
	b.streams[key] = entry
	b.mu.Unlock()
	return id
}

func (b *relayHostStreamBridge) read(ctx context.Context, pluginID string, instance *hostCallbackInstance, id string) ([]byte, bool, error) {
	if b == nil || strings.TrimSpace(id) == "" {
		return nil, true, errors.New("relay stream id is required")
	}
	key := relayHostStreamKey{pluginID: strings.TrimSpace(pluginID), instance: instance, id: strings.TrimSpace(id)}
	b.mu.Lock()
	entry := b.streams[key]
	b.mu.Unlock()
	if entry == nil {
		return nil, true, errors.New("relay stream is not open")
	}
	entry.readMu.Lock()
	defer entry.readMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	stopCancel := context.AfterFunc(ctx, func() { b.close(pluginID, instance, id) })
	defer stopCancel()
	if err := ctx.Err(); err != nil {
		b.close(pluginID, instance, id)
		return nil, true, err
	}
	buffer := make([]byte, 32*1024)
	n, errRead := entry.body.Read(buffer)
	if errRead != nil {
		b.close(pluginID, instance, id)
		if errRead != io.EOF {
			return nil, true, errors.New("relay stream read failed")
		}
	}
	return buffer[:n], errRead == io.EOF, nil
}

func (b *relayHostStreamBridge) close(pluginID string, instance *hostCallbackInstance, id string) {
	if b == nil || strings.TrimSpace(id) == "" {
		return
	}
	key := relayHostStreamKey{pluginID: strings.TrimSpace(pluginID), instance: instance, id: strings.TrimSpace(id)}
	b.mu.Lock()
	entry := b.streams[key]
	delete(b.streams, key)
	b.mu.Unlock()
	closeRelayHostStreamEntry(entry)
}

func (b *relayHostStreamBridge) closeInstance(pluginID string, instance *hostCallbackInstance) {
	b.closeMatching(pluginID, instance, false)
}
func (b *relayHostStreamBridge) closePlugin(pluginID string) { b.closeMatching(pluginID, nil, false) }
func (b *relayHostStreamBridge) closeAll()                   { b.closeMatching("", nil, true) }

func (b *relayHostStreamBridge) closeMatching(pluginID string, instance *hostCallbackInstance, all bool) {
	if b == nil {
		return
	}
	pluginID = strings.TrimSpace(pluginID)
	b.mu.Lock()
	entries := make([]*relayHostStreamEntry, 0)
	for key, entry := range b.streams {
		if !all && (key.pluginID != pluginID || (instance != nil && key.instance != instance)) {
			continue
		}
		delete(b.streams, key)
		entries = append(entries, entry)
	}
	b.mu.Unlock()
	for _, entry := range entries {
		closeRelayHostStreamEntry(entry)
	}
}

func closeRelayHostStreamEntry(entry *relayHostStreamEntry) {
	if entry == nil {
		return
	}
	entry.closeOnce.Do(func() {
		if entry.cancel != nil {
			entry.cancel()
		}
		if entry.body != nil {
			_ = entry.body.Close()
		}
		if entry.finish != nil {
			entry.finish()
		}
	})
}
