package keyshop

import (
	"encoding/json"
	"sync"
	"time"
)

const idempotencyWindow = 2 * time.Minute

type idempotentResult struct {
	status int
	body   []byte
	at     time.Time
}

type createWaiter struct {
	done chan struct{}
}

// createReplay remembers a successful key-create response for a short window
// so a repeated POST with the same client request id does not mint new keys.
// An id that is already creating is claimed until that call finishes.
type createReplay struct {
	mu       sync.Mutex
	items    map[string]idempotentResult
	inflight map[string]*createWaiter
	now      func() time.Time
}

func (r *createReplay) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func (r *createReplay) cached(id string) (map[string]any, bool) {
	item, ok := r.items[id]
	if !ok || r.clock().Sub(item.at) > idempotencyWindow {
		delete(r.items, id)
		return nil, false
	}
	return map[string]any{
		"StatusCode": item.status,
		"Headers":    map[string][]string{"Content-Type": {"application/json"}},
		"Body":       append([]byte(nil), item.body...),
	}, true
}

// begin reports a cached response, asks the caller to wait for the owner, or
// grants ownership of id. An empty id is always owned and is not remembered.
func (r *createReplay) begin(id string) (map[string]any, <-chan struct{}, bool) {
	if id == "" {
		return nil, nil, true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if resp, ok := r.cached(id); ok {
		return resp, nil, false
	}
	if w, ok := r.inflight[id]; ok {
		return nil, w.done, false
	}
	if r.inflight == nil {
		r.inflight = map[string]*createWaiter{}
	}
	w := &createWaiter{done: make(chan struct{})}
	r.inflight[id] = w
	return nil, nil, true
}

func (r *createReplay) finish(id string, resp map[string]any) {
	if id == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rememberLocked(id, resp)
	if w := r.inflight[id]; w != nil {
		close(w.done)
		delete(r.inflight, id)
	}
}

func (r *createReplay) rememberLocked(id string, resp map[string]any) {
	status, _ := resp["StatusCode"].(int)
	if status != 200 {
		return
	}
	body, _ := resp["Body"].([]byte)
	if len(body) == 0 {
		return
	}
	if r.items == nil {
		r.items = map[string]idempotentResult{}
	}
	now := r.clock()
	for key, item := range r.items {
		if now.Sub(item.at) > idempotencyWindow {
			delete(r.items, key)
		}
	}
	r.items[id] = idempotentResult{status: status, body: append([]byte(nil), body...), at: now}
}

func clientRequestID(body []byte) string {
	var in struct {
		RequestID string `json:"request_id"`
	}
	if json.Unmarshal(body, &in) != nil {
		return ""
	}
	return in.RequestID
}
