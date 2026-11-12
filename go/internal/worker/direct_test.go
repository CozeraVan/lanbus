package worker

import "testing"

func TestApplyPermissionMode(t *testing.T) {
	cases := []struct{ command, mode, want string }{
		{"claude -p --permission-mode acceptEdits", "bypassPermissions", "claude -p --permission-mode bypassPermissions"},
		{"claude -p --permission-mode=plan", "bypassPermissions", "claude -p --permission-mode bypassPermissions"},
		{"codex exec --skip-git-repo-check -", "bypassPermissions", "codex exec --skip-git-repo-check -"},
		{"claude -p --permission-mode acceptEdits", "", "claude -p --permission-mode acceptEdits"},
	}
	for _, c := range cases {
		if got := ApplyPermissionMode(c.command, c.mode); got != c.want {
			t.Errorf("ApplyPermissionMode(%q, %q) = %q, want %q", c.command, c.mode, got, c.want)
		}
	}
}

func TestDefaultCommand(t *testing.T) {
	if DefaultCommand("claude") != "claude -p --permission-mode acceptEdits" {
		t.Error("claude default changed")
	}
	if DefaultCommand("codex") != "codex exec --skip-git-repo-check -" {
		t.Error("codex default changed")
	}
	if DefaultCommand("deepseek") != "dsh headless -" {
		t.Error("deepseek default changed")
	}
}