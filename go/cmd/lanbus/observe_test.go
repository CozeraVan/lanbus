package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func captureStdout(fn func() error) (string, error) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		return "", err
	}
	os.Stdout = w
	fnErr := fn()
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return string(out), fnErr
}

func TestObserveCommands(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("peer.json", `{"name":"agent1","peer_id":"pid1","url":"http://127.0.0.1:1"}`)
	write("company.json", `{"company_id":"c1","company_name":"Acme","secret":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`)
	write("console-conversations.json", `{"http://t":{"target_url":"http://t","scope":"chat","turns":[{"role":"user","text":"hi","ts":1700000000},{"role":"agent","text":"yo","ts":1700000000,"receipt":{"status":"completed","request_id":"r1","exit_code":0}}]}}`)
	write("receipts.json", `[{"request_id":"r1","peer_id":"p","scope":"chat","status":"completed","exit_code":0,"stages":[{"stage":"received","ts":1700000000},{"stage":"completed","ts":1700000001}]}]`)

	out, err := captureStdout(func() error { return statusCmd([]string{"--data-dir", dir}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "agent1") || !strings.Contains(out, "Acme") || !strings.Contains(out, "离线") {
		t.Fatalf("status output missing fields:\n%s", out)
	}

	out, err = captureStdout(func() error { return historyCmd([]string{"--data-dir", dir, "--target", "http://t"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hi") || !strings.Contains(out, "completed") {
		t.Fatalf("history output missing fields:\n%s", out)
	}

	out, err = captureStdout(func() error { return receiptsCmd([]string{"--data-dir", dir}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "r1") || !strings.Contains(out, "received") || !strings.Contains(out, "completed") {
		t.Fatalf("receipts output missing fields:\n%s", out)
	}
}

func TestObserveRejectsUnsafeName(t *testing.T) {
	if err := statusCmd([]string{"--name", "../escape"}); err == nil {
		t.Fatal("expected error for traversal name")
	}
}