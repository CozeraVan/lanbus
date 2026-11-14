package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/CozeraVan/lanbus/go/internal/peer"
)

// statusCmd shows whether a local agent is registered (company membership) and
// its live state (online/idle/awaiting_approval/executing). Omitting --name lists
// every local peer.
func statusCmd(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	name := fs.String("name", "", "peer name (omit to list all local peers)")
	dataDir := fs.String("data-dir", "", "peer data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dirs, err := resolvePeerDirs(*name, *dataDir, false)
	if err != nil {
		return err
	}
	for _, dir := range dirs {
		report, err := peer.StatusReport(dir)
		if err != nil {
			return err
		}
		printStatus(report)
	}
	return nil
}

// historyCmd shows conversation history with a per-turn receipt status, or lists
// conversations when --target is omitted.
func historyCmd(args []string) error {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	name := fs.String("name", "", "peer name")
	dataDir := fs.String("data-dir", "", "peer data directory")
	target := fs.String("target", "", "target URL (omit to list conversations)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dirs, err := resolvePeerDirs(*name, *dataDir, true)
	if err != nil {
		return err
	}
	report, err := peer.HistoryReport(dirs[0], *target)
	if err != nil {
		return err
	}
	if *target != "" {
		printTurns(report)
	} else {
		printConversationList(report)
	}
	return nil
}

// receiptsCmd prints the receiver-side receipt log: each request's lifecycle
// stages with timestamps, exit code and error — for diagnosing send/receive/
// execute failures.
func receiptsCmd(args []string) error {
	fs := flag.NewFlagSet("receipts", flag.ContinueOnError)
	name := fs.String("name", "", "peer name")
	dataDir := fs.String("data-dir", "", "peer data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dirs, err := resolvePeerDirs(*name, *dataDir, true)
	if err != nil {
		return err
	}
	list := peer.ReceiptsReport(dirs[0])
	if len(list) == 0 {
		fmt.Println("（无回执记录）")
		return nil
	}
	for _, r := range list {
		fmt.Printf("● %s  %s  scope=%s  exit=%d\n", r.RequestID, r.Status, r.Scope, r.ExitCode)
		fmt.Printf("  来源: %s (%s)  消息: %s\n", r.PeerID, r.Hostname, clip(r.Message, 80))
		for _, st := range r.Stages {
			fmt.Printf("    %s  %-18s %s\n", formatTS(st.TS), st.Stage, st.Detail)
		}
		if r.Error != "" {
			fmt.Printf("  错误: %s\n", clip(r.Error, 200))
		}
		fmt.Println()
	}
	return nil
}

// resolvePeerDirs resolves the peer data dir(s) to inspect. With single=true a
// specific peer (--name or --data-dir) is required; otherwise omitting --name
// enumerates all local peers under ~/.lanbus/peers.
func resolvePeerDirs(name, dataDir string, single bool) ([]string, error) {
	if dataDir != "" {
		return []string{dataDir}, nil
	}
	root := filepath.Join(defaultDataDir(), "peers")
	if name != "" {
		if name == "." || name == ".." || filepath.IsAbs(name) || strings.ContainsAny(name, `/\\`) {
			return nil, fmt.Errorf("--name must be a single peer directory name")
		}
		return []string{filepath.Join(root, name)}, nil
	}
	if single {
		return nil, fmt.Errorf("需要 --name 或 --data-dir 指定本机 peer")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("没有本机 peer（%s）: %w", root, err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(root, e.Name()))
		}
	}
	sort.Strings(dirs)
	if len(dirs) == 0 {
		return nil, fmt.Errorf("没有本机 peer（%s）", root)
	}
	return dirs, nil
}

func printStatus(r map[string]any) {
	name := str(r["name"])
	if name == "" {
		name = filepath.Base(str(r["data_dir"]))
	}
	reg := "未加入公司（独立 peer）"
	if b, _ := r["registered"].(bool); b {
		reg = fmt.Sprintf("公司「%s」(%s)", str(r["company_name"]), str(r["company_id"]))
	}
	online := "离线"
	if b, _ := r["online"].(bool); b {
		online = "在线"
	}
	fmt.Printf("● %s  [%s / %s]\n", name, online, str(r["state"]))
	fmt.Printf("  peer_id : %s\n", str(r["peer_id"]))
	fmt.Printf("  注册    : %s\n", reg)
	if str(r["agent"]) != "" {
		fmt.Printf("  agent   : %s   策略: %s\n", str(r["agent"]), str(r["request_policy"]))
	}
	fmt.Printf("  执行中  : %s   待审批: %s\n", str(r["active_executions"]), str(r["pending_approvals"]))
	fmt.Printf("  端点    : %s\n\n", str(r["url"]))
}

func printTurns(report map[string]any) {
	turns, _ := report["turns"].([]any)
	if len(turns) == 0 {
		fmt.Println("（无对话历史）")
		return
	}
	fmt.Printf("目标: %s\n", str(report["target_url"]))
	for _, tv := range turns {
		t, _ := tv.(map[string]any)
		label := "操控方"
		if str(t["role"]) == "agent" {
			label = "目标"
		}
		status := ""
		if rc, ok := t["receipt"].(map[string]any); ok {
			status = " [" + str(rc["status"]) + "]"
		}
		fmt.Printf("%s %s%s: %s\n", formatTS(t["ts"]), label, status, clip(str(t["text"]), 200))
		if rc, ok := t["receipt"].(map[string]any); ok && str(rc["error"]) != "" {
			fmt.Printf("    错误: %s\n", clip(str(rc["error"]), 200))
		}
	}
}

func printConversationList(report map[string]any) {
	list, _ := report["conversations"].([]any)
	if len(list) == 0 {
		fmt.Println("（无对话）")
		return
	}
	for _, cv := range list {
		c, _ := cv.(map[string]any)
		fmt.Printf("● %s  轮数=%s  scope=%s\n", str(c["target_url"]), str(c["turns"]), str(c["scope"]))
	}
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func clip(text string, n int) string {
	r := []rune(text)
	if len(r) <= n {
		return text
	}
	return string(r[:n]) + "…"
}

func formatTS(v any) string {
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case json.Number:
		f, _ = x.Float64()
	default:
		return "--:--:--"
	}
	if f <= 0 {
		return "--:--:--"
	}
	return time.Unix(int64(f), 0).Format("15:04:05")
}