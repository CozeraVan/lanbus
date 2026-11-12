package peer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const SessionIdleS = 1800

// GrantStore mirrors Python GrantStore: pair grants persist in grants.json,
// session grants live in memory only and die on restart or 30-min idle.
type GrantStore struct {
	path   string
	mu     sync.RWMutex
	saved  map[string]map[string]any
	memory map[string]map[string]any
}

func NewGrantStore(dataDir string) (*GrantStore, error) {
	store := &GrantStore{
		path:   filepath.Join(dataDir, "grants.json"),
		saved:  map[string]map[string]any{},
		memory: map[string]map[string]any{},
	}
	raw, err := os.ReadFile(store.path)
	if os.IsNotExist(err) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	obj, err := ParseObject(raw)
	if err != nil {
		return nil, err
	}
	for id, item := range obj {
		if grant, ok := item.(map[string]any); ok {
			store.saved[id] = grant
		}
	}
	return store, nil
}

func (s *GrantStore) PutGrant(value map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := copyGrant(value)
	item["last_used"] = PyFloat(float64(time.Now().UnixNano()) / 1e9)
	if asString(item["mode"]) == "session" {
		s.memory[asString(item["grant_id"])] = item
		return nil
	}
	s.saved[asString(item["grant_id"])] = item
	return s.persistLocked()
}

func (s *GrantStore) Grant(grantID, peerID string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.memory[grantID]
	if item == nil {
		item = s.saved[grantID]
	}
	if item == nil || (peerID != "" && asString(item["peer_id"]) != peerID) {
		return nil
	}
	if asString(item["mode"]) == "session" {
		last := asFloat(item["last_used"])
		if last == 0 {
			last = asFloat(item["created_at"])
		}
		if float64(time.Now().UnixNano())/1e9-last > SessionIdleS {
			delete(s.memory, grantID)
			return nil
		}
	}
	item["last_used"] = PyFloat(float64(time.Now().UnixNano()) / 1e9)
	return copyGrant(item)
}

func (s *GrantStore) Revoke(peerID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := false
	for id, item := range s.memory {
		if asString(item["peer_id"]) == peerID {
			delete(s.memory, id)
			removed = true
		}
	}
	for id, item := range s.saved {
		if asString(item["peer_id"]) == peerID {
			delete(s.saved, id)
			removed = true
		}
	}
	if removed {
		if err := s.persistLocked(); err != nil {
			return removed
		}
	}
	return removed
}

func (s *GrantStore) ListGrants() []map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []map[string]any{}
	for _, item := range s.saved {
		out = append(out, copyGrant(item))
	}
	for _, item := range s.memory {
		out = append(out, copyGrant(item))
	}
	return out
}

func (s *GrantStore) persistLocked() error {
	return atomicJSON(s.path, s.saved, true)
}

func copyGrant(value map[string]any) map[string]any {
	out := make(map[string]any, len(value))
	for k, v := range value {
		out[k] = v
	}
	return out
}

var _ = json.Number("")