package daemon

import (
	"strings"
	"testing"
)

func TestRenderSystemd(t *testing.T) {
	s := Spec{Name: "lanbus-hub", ExePath: "/opt/lanbus/lanbusd", DataDir: "/home/u/.lanbus", Port: 8788}
	out := RenderSystemd(s)
	for _, want := range []string{"[Unit]", "[Service]", "ExecStart=/opt/lanbus/lanbusd", "--data-dir /home/u/.lanbus", "--port 8788", "Restart=on-failure", "[Install]"} {
		if !strings.Contains(out, want) {
			t.Fatalf("systemd unit missing %q:\n%s", want, out)
		}
	}
}

func TestRenderSystemdQuotesSpaces(t *testing.T) {
	s := Spec{ExePath: "/opt/my lanbus/lanbusd", DataDir: "/home/u/.lanbus"}
	out := RenderSystemd(s)
	if !strings.Contains(out, `ExecStart="/opt/my lanbus/lanbusd"`) {
		t.Fatalf("expected quoted ExecStart:\n%s", out)
	}
}

func TestRenderLaunchd(t *testing.T) {
	s := Spec{Name: "lanbus-hub", ExePath: "/Users/u/bin/lanbusd", DataDir: "/Users/u/.lanbus", Port: 8788}
	out := RenderLaunchd(s)
	for _, want := range []string{"<key>Label</key><string>com.lanbus.lanbus-hub</string>", "<string>/Users/u/bin/lanbusd</string>", "<key>KeepAlive</key><true/>", "<string>--port</string>", "<string>8788</string>"} {
		if !strings.Contains(out, want) {
			t.Fatalf("launchd plist missing %q:\n%s", want, out)
		}
	}
}

func TestRenderWindowsInstallPS(t *testing.T) {
	s := Spec{Name: "lanbus-hub", ExePath: `C:\lanbus\lanbusd.exe`, DataDir: `C:\Users\u\.lanbus`, Port: 8788, TokenFile: `C:\Users\u\.lanbus\token.txt`}
	out := RenderWindowsInstallPS(s)
	for _, want := range []string{"New-ScheduledTaskAction", "New-ScheduledTaskTrigger -AtLogOn", "Register-ScheduledTask -TaskName 'lanbus-hub'", `C:\lanbus\lanbusd.exe`, "--port 8788", "--token-file", "HKCU:", "Set-ItemProperty"} {
		if !strings.Contains(out, want) {
			t.Fatalf("windows install PS missing %q:\n%s", want, out)
		}
	}
}

func TestRenderWindowsUninstallPS(t *testing.T) {
	out := RenderWindowsUninstallPS("lanbus-hub")
	if !strings.Contains(out, "Unregister-ScheduledTask -TaskName 'lanbus-hub'") || !strings.Contains(out, "Remove-ItemProperty") {
		t.Fatalf("windows uninstall PS wrong:\n%s", out)
	}
}

func TestHubArgStringQuotesSpaces(t *testing.T) {
	s := Spec{ExePath: "x", DataDir: `C:\Users\my user\.lanbus`, Port: 8788}
	got := s.hubArgString()
	if !strings.Contains(got, `--data-dir "C:\Users\my user\.lanbus"`) {
		t.Fatalf("expected quoted data-dir: %s", got)
	}
}

func TestDefaults(t *testing.T) {
	s := Spec{}.withDefaults()
	if s.Name != "lanbus-hub" || s.Port != 8788 {
		t.Fatalf("defaults wrong: %+v", s)
	}
	if s.Label() != "com.lanbus.lanbus-hub" {
		t.Fatalf("label wrong: %s", s.Label())
	}
}
