package browser

import (
	"context"
	"errors"
	"sync"
)

var ErrViewUnavailable = errors.New("browser: view is unavailable")

// ViewSnapshot is a bounded, read-only observation of an active browser
// session. The Core facade converts it to a transport DTO.
type ViewSnapshot struct {
	ContentType string
	Width       int
	Height      int
	Data        []byte
}

// Viewer is implemented by pipeline workers that can obtain a snapshot from
// the browser they already own. It does not expose a CDP endpoint to callers.
type Viewer interface {
	Snapshot(context.Context, int, int) (ViewSnapshot, error)
}

// ViewRegistry tracks only active request sessions. Entries are ephemeral and
// disappear before a worker is closed.
type ViewRegistry struct {
	mu      sync.RWMutex
	viewers map[string]Viewer
}

func NewViewRegistry() *ViewRegistry {
	return &ViewRegistry{viewers: make(map[string]Viewer)}
}

func (r *ViewRegistry) Register(requestID string, viewer Viewer) {
	if r == nil || requestID == "" || viewer == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.viewers == nil {
		r.viewers = make(map[string]Viewer)
	}
	r.viewers[requestID] = viewer
}

func (r *ViewRegistry) Unregister(requestID string, viewer Viewer) {
	if r == nil || requestID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Request IDs are unique for active queue claims. The viewer interface may
	// hold a non-comparable concrete value, so comparing it here could panic;
	// the deferred unregister for that request owns this registry slot.
	delete(r.viewers, requestID)
}

func (r *ViewRegistry) Snapshot(ctx context.Context, requestID string, width, height int) (ViewSnapshot, error) {
	if r == nil {
		return ViewSnapshot{}, ErrViewUnavailable
	}
	r.mu.RLock()
	viewer := r.viewers[requestID]
	r.mu.RUnlock()
	if viewer == nil {
		return ViewSnapshot{}, ErrViewUnavailable
	}
	return viewer.Snapshot(ctx, width, height)
}
