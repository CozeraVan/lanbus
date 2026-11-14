package peer

import (
	"os"
	"path/filepath"
	"strings"
)

// StatusReport builds a registration + live-state report for one peer data dir.
// Registration means company membership (company.json); live state comes from
// the peer's /peer/health endpoint when it is reachable.
func StatusReport(dataDir string) (map[string]any, error) {
	report := map[string]any{"data_dir": dataDir, "online": false, "registered": false}
	if raw, err := os.ReadFile(filepath.Join(dataDir, "peer.json")); err == nil {
		if obj, err := ParseObject(raw); err == nil {
			report["name"] = asString(obj["name"])
			report["peer_id"] = asString(obj["peer_id"])
			report["url"] = asString(obj["url"])
		}
	}
	if company, err := LoadCompany(dataDir); err == nil && company != nil {
		report["registered"] = true
		report["company_id"] = company.ID
		report["company_name"] = company.Name
	}
	if url := asString(report["url"]); url != "" {
		if health, err := JSONRequest(url+"/peer/health", nil, nil, 3); err == nil {
			report["online"] = true
			report["state"] = asString(health["state"])
			report["agent"] = asString(health["agent"])
			report["display_name"] = asString(health["display_name"])
			report["request_policy"] = asString(health["request_policy"])
			report["hostname"] = asString(health["hostname"])
			report["active_executions"] = health["active_executions"]
			report["pending_approvals"] = health["pending_approvals"]
			if asString(report["name"]) == "" {
				report["name"] = asString(health["name"])
			}
		}
	}
	if asString(report["state"]) == "" {
		// Older peers do not advertise state; reachable-but-unknown is not offline.
		if b, _ := report["online"].(bool); b {
			report["state"] = "unknown"
		} else {
			report["state"] = "offline"
		}
	}
	return report, nil
}

// HistoryReport returns stored conversation turns for one target (targetURL set)
// or a summary of every conversation (targetURL empty) in a peer data dir.
func HistoryReport(dataDir, targetURL string) (map[string]any, error) {
	convs := loadConversations(dataDir)
	if targetURL != "" {
		key := strings.TrimRight(targetURL, "/")
		conv, _ := convs[key].(map[string]any)
		turns := convTurns(convs, key)
		if turns == nil {
			turns = []any{}
		}
		return map[string]any{
			"target_url": key, "turns": turns,
			"context": storedContext(conv), "max_turns": PyFloat(float64(storedMaxTurns(conv))),
		}, nil
	}
	list := []any{}
	for key, cv := range convs {
		conv, _ := cv.(map[string]any)
		turns := convTurns(convs, key)
		list = append(list, map[string]any{
			"target_url": key, "turns": PyFloat(float64(len(turns))),
			"scope": asString(conv["scope"]), "updated_at": conv["updated_at"],
		})
	}
	return map[string]any{"conversations": list}, nil
}

// ReceiptsReport returns the receiver-side receipt log (newest first) for a peer
// data dir, reading the persisted receipts.json so it works even while offline.
func ReceiptsReport(dataDir string) []Receipt {
	return LoadReceipts(dataDir).List()
}