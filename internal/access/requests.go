package access

import "sync"

// Request is a person who pressed /start and waits for an admin decision.
type Request struct {
	ChatID int64
	Name   string // human-readable: name, @username or group title
}

// Requests remembers who is waiting so that repeated /start presses do not produce
// repeated messages to the admins. It lives in memory only: after a restart the old
// buttons in the admin chat still work, and a person can simply press /start again.
type Requests struct {
	mu      sync.Mutex
	pending map[int64]Request
}

func NewRequests() *Requests {
	return &Requests{pending: make(map[int64]Request)}
}

// Add registers the request. It reports false when this chat is already waiting.
func (r *Requests) Add(req Request) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.pending[req.ChatID]; ok {
		return false
	}
	r.pending[req.ChatID] = req
	return true
}

// Take removes and returns the request of a chat (ok is false if none was waiting).
func (r *Requests) Take(chatID int64) (Request, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	req, ok := r.pending[chatID]
	delete(r.pending, chatID)
	return req, ok
}
