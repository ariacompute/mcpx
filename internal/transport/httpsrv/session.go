package httpsrv

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// sessionStore keeps MCP sessions in memory. A session carries no state the
// gateway needs besides "this client completed the handshake", so losing the
// map on restart only costs the client a new initialize.
type sessionStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]time.Time
	now     func() time.Time
}

func newSessionStore(ttl time.Duration) *sessionStore {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	return &sessionStore{
		ttl:     ttl,
		entries: make(map[string]time.Time),
		now:     time.Now,
	}
}

// Create registers a new session and returns its id.
func (s *sessionStore) Create() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictLocked()
	s.entries[id] = s.now().Add(s.ttl)
	return id, nil
}

// Touch validates a session and extends its life. An unknown or expired id is
// reported as absent so the transport can answer 404, which is what the spec
// tells a client to recover from by re-initializing.
func (s *sessionStore) Touch(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictLocked()
	if _, ok := s.entries[id]; !ok {
		return false
	}
	s.entries[id] = s.now().Add(s.ttl)
	return true
}

// Delete terminates a session; it reports whether the id existed.
func (s *sessionStore) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[id]; !ok {
		return false
	}
	delete(s.entries, id)
	return true
}

// Count is used by /healthz.
func (s *sessionStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictLocked()
	return len(s.entries)
}

func (s *sessionStore) evictLocked() {
	now := s.now()
	for id, expiry := range s.entries {
		if now.After(expiry) {
			delete(s.entries, id)
		}
	}
}
