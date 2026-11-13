package peer

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"time"
)

// Notification describes a local-only approval prompt. Secret must never be
// included in Title, Message or process output.
type Notification struct {
	RequestID string
	Kind      string
	Message   string
	URL       string
	Secret    string
	Target    map[string]any
	OpenPage  bool
}

// DesktopNotifier returns a best-effort macOS/Windows notifier. Requests stay
// pending in the inbox if desktop notifications are unavailable.
func DesktopNotifier(openPage bool) func(Notification) {
	return func(n Notification) {
		label := asString(n.Target["instance_label"])
		if label == "" {
			label = asString(n.Target["display_name"])
		}
		action := "请求配对"
		if n.Kind == "execution" {
			action = "请求指挥"
		}
		title := fmt.Sprintf("Lanbus：%s %s", action, label)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var cmd *exec.Cmd
		if runtime.GOOS == "darwin" {
			script := fmt.Sprintf("display notification %q with title %q sound name \"Glass\"", n.Message, title)
			cmd = exec.CommandContext(ctx, "osascript", "-e", script)
		} else if runtime.GOOS == "windows" {
			// Opening the local approval page is always available; systems with
			// notification permissions can additionally surface this PowerShell alert.
			script := fmt.Sprintf("[reflection.assembly]::LoadWithPartialName('System.Windows.Forms') | Out-Null; [System.Windows.Forms.MessageBox]::Show(%q, %q)", n.Message, title)
			cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script)
		}
		if cmd != nil {
			if cmd.Start() == nil {
				go func() { _ = cmd.Wait() }()
			}
		}
		if n.OpenPage && openPage {
			page := n.URL + "#secret=" + url.QueryEscape(n.Secret)
			switch runtime.GOOS {
			case "darwin":
				cmd = exec.Command("open", page)
			case "windows":
				cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", page)
			default:
				cmd = exec.Command("xdg-open", page)
			}
			_ = cmd.Start()
		}
	}
}
