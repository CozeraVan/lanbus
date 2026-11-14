package peer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Receipt stages trace one request through the direct pipeline so a sender can
// tell whether a message was merely sent, actually received, approved, executed
// and completed — and exactly where it failed. Stages are append-only.
const (
	StageReceived         = "received"
	StageApprovalRequired = "approval_required"
	StageApproved         = "approved"
	StageDenied           = "denied"
	StageExpired          = "expired"
	StageExecuting        = "executing"
	StageCompleted        = "completed"
	StageFailed           = "failed"
)

// receiptMax bounds the persisted log so a busy peer does not grow it forever.
const receiptMax = 200

// ReceiptStage is one timestamped lifecycle transition.
type ReceiptStage struct {
	Stage  string  `json:"stage"`
	TS     float64 `json:"ts"`
	Detail string  `json:"detail,omitempty"`
}

// Receipt is the receiver-side record of a single request's lifecycle.
type Receipt struct {
	RequestID string         `json:"request_id"`
	PeerID    string         `json:"peer_id,omitempty"`
	Hostname  string         `json:"hostname,omitempty"`
	Agent     string         `json:"agent,omitempty"`
	Scope     string         `json:"scope,omitempty"`
	Message   string         `json:"message,omitempty"`
	CreatedAt float64        `json:"created_at"`
	UpdatedAt float64        `json:"updated_at"`
	Status    string         `json:"status"`
	ExitCode  int            `json:"exit_code"`
	Error     string         `json:"error,omitempty"`
	DurationS float64        `json:"duration_s,omitempty"`
	Stages    []ReceiptStage `json:"stages"`
}

func (r *Receipt) addStage(stage, detail string) {
	r.Stages = append(r.Stages, ReceiptStage{Stage: stage, TS: now(), Detail: detail})
	r.Status = stage
}

// ReceiptStore is a bounded, persisted, concurrency-safe receipt log kept in a
// peer's data dir (receipts.json). It is local diagnostics state, not wire data.
type ReceiptStore struct {
	mu    sync.Mutex
	path  string
	items []Receipt
}

// LoadReceipts opens (or creates) the receipt log for a peer data dir.
func LoadReceipts(dataDir string) *ReceiptStore {
	rs := &ReceiptStore{path: filepath.Join(dataDir, "receipts.json")}
	if raw, err := os.ReadFile(rs.path); err == nil {
		var items []Receipt
		if json.Unmarshal(raw, &items) == nil {
			rs.items = items
		}
	}
	return rs
}

// Update finds or creates the receipt for requestID, applies fn (which usually
// records metadata and appends stages), stamps the update time, trims to the
// newest receiptMax entries and persists. Errors persisting are non-fatal: the
// in-memory log still serves live queries.
func (rs *ReceiptStore) Update(requestID string, fn func(*Receipt)) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	var r *Receipt
	for i := range rs.items {
		if rs.items[i].RequestID == requestID {
			r = &rs.items[i]
			break
		}
	}
	if r == nil {
		rs.items = append(rs.items, Receipt{RequestID: requestID, CreatedAt: now()})
		r = &rs.items[len(rs.items)-1]
	}
	fn(r)
	r.UpdatedAt = now()
	if len(rs.items) > receiptMax {
		rs.items = rs.items[len(rs.items)-receiptMax:]
	}
	_ = atomicJSON(rs.path, rs.items, true)
}

// List returns a copy of the receipts, newest first.
func (rs *ReceiptStore) List() []Receipt {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make([]Receipt, len(rs.items))
	for i, r := range rs.items {
		out[len(rs.items)-1-i] = r
	}
	return out
}

// ListGeneric returns the receipts (newest first) as generic JSON values so the
// canonical wire encoder can serialize them; it only handles generic types, not
// typed struct slices. Numbers become json.Number via ParseObject.
func (rs *ReceiptStore) ListGeneric() []any {
	raw, err := json.Marshal(map[string]any{"receipts": rs.List()})
	if err != nil {
		return []any{}
	}
	obj, err := ParseObject(raw)
	if err != nil {
		return []any{}
	}
	list, _ := obj["receipts"].([]any)
	if list == nil {
		return []any{}
	}
	return list
}