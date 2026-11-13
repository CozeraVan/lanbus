// Package peerui locates loopback-only approval pages for local direct peers.
package peerui

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Entry is a discoverable local direct-peer approval page. URL contains the
// approval secret in its fragment and must not be written to logs.
type Entry struct {
	Name string
	URL  string
}

// ApprovalURL reads a peer's public endpoint and local approval secret. The
// endpoint must be loopback HTTP so the secret cannot be sent to another host.
func ApprovalURL(dataDir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, "peer.json"))
	if err != nil {
		return "", fmt.Errorf("peerui: read peer metadata: %w", err)
	}
	var info struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return "", fmt.Errorf("peerui: invalid peer metadata: %w", err)
	}
	parsed, err := url.Parse(strings.TrimSpace(info.URL))
	if err != nil || parsed.Scheme != "http" || !isLoopback(parsed.Hostname()) || parsed.User != nil {
		return "", fmt.Errorf("peerui: approval URL must be loopback HTTP")
	}
	secRaw, err := os.ReadFile(filepath.Join(dataDir, "approval.json"))
	if err != nil {
		return "", fmt.Errorf("peerui: read approval secret: %w", err)
	}
	var sec struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(secRaw, &sec); err != nil {
		return "", fmt.Errorf("peerui: invalid approval secret: %w", err)
	}
	if sec.Secret == "" {
		return "", fmt.Errorf("peerui: approval secret is empty")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(info.URL), "/")
	return baseURL + "/peer/ui#secret=" + url.QueryEscape(sec.Secret), nil
}

// ConsoleURL returns the loopback command-console URL with the approval secret
// in its fragment. Like ApprovalURL it must never be written to logs.
func ConsoleURL(dataDir string) (string, error) {
	u, err := ApprovalURL(dataDir)
	if err != nil {
		return "", err
	}
	return strings.Replace(u, "/peer/ui#", "/peer/console#", 1), nil
}

// Entries returns valid peers under <home>/.lanbus/peers, skipping incomplete,
// stale or unsafe metadata. Invalid entries must not prevent other peers opening.
func Entries(home string) []Entry {
	root := filepath.Join(home, ".lanbus", "peers")
	items, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	entries := make([]Entry, 0, len(items))
	for _, item := range items {
		if !item.IsDir() {
			continue
		}
		approvalURL, err := ApprovalURL(filepath.Join(root, item.Name()))
		if err != nil {
			continue
		}
		entries = append(entries, Entry{Name: item.Name(), URL: approvalURL})
	}
	return entries
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
