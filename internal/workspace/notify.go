package workspace

import (
	"sync"
	"sync/atomic"
)

// ChangeEvent describes a mutation performed on the workspace.
type ChangeEvent struct {
	Seq  uint64 `json:"seq"`
	Op   string `json:"op"`             // "write", "delete", "mkdir", "rename", "switch"
	Path string `json:"path,omitempty"` // affected relative path (or new path on rename)
	From string `json:"from,omitempty"` // previous relative path on rename
}

type changeHub struct {
	seq  atomic.Uint64
	mu   sync.Mutex
	subs map[chan ChangeEvent]struct{}
}

// notify is always called on &w.hub (a value field of Workspace), so h is
// never nil.
func (h *changeHub) notify(op, relPath, from string) {
	seq := h.seq.Add(1)
	ev := ChangeEvent{
		Seq:  seq,
		Op:   op,
		Path: relPath,
		From: from,
	}
	h.mu.Lock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
			// Slow subscriber: drop intermediate event; client will do a full refresh.
		}
	}
	h.mu.Unlock()
}

// Revision returns a monotonically increasing counter bumped on every
// workspace mutation.
func (w *Workspace) Revision() uint64 {
	return w.hub.seq.Load()
}

// Subscribe registers a listener that receives workspace mutation events.
// Call the returned unsubscribe function when done.
func (w *Workspace) Subscribe() (<-chan ChangeEvent, func()) {
	ch := make(chan ChangeEvent, 32)
	w.hub.mu.Lock()
	if w.hub.subs == nil {
		w.hub.subs = make(map[chan ChangeEvent]struct{})
	}
	w.hub.subs[ch] = struct{}{}
	w.hub.mu.Unlock()

	return ch, func() {
		w.hub.mu.Lock()
		delete(w.hub.subs, ch)
		w.hub.mu.Unlock()
	}
}
