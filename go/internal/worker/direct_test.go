package worker

import (
	"reflect"
	"testing"
)

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

func TestExpandPromptFileQuotesPathsWithSpaces(t *testing.T) {
	path := `C:\Users\Test User\AppData\Local\Temp\lanbus-peer-1.md`
	for _, command := range []string{"agent {prompt_file}", `agent "{prompt_file}"`} {
		want := `agent "` + path + `"`
		if got := expandPromptFile(command, path, true); got != want {
			t.Errorf("expandPromptFile(%q) = %q, want %q", command, got, want)
		}
	}
}

func TestWindowsTempCommandUsesArgumentsInsteadOfShellString(t *testing.T) {
	path := `C:\Users\Test User\AppData\Local\Temp\lanbus-echo-1.txt`
	name, args := windowsTempCommand(path, true)
	if name != "cmd.exe" || !reflect.DeepEqual(args, []string{"/C", "type", path}) {
		t.Fatalf("windowsTempCommand(type) = %q %q", name, args)
	}
	name, args = windowsTempCommand(path, false)
	if name != "cmd.exe" || !reflect.DeepEqual(args, []string{"/C", path}) {
		t.Fatalf("windowsTempCommand(batch) = %q %q", name, args)
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
