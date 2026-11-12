// Package daemon installs the lanbus hub as a per-user startup service:
// Windows Scheduled Task (at logon), macOS launchd (KeepAlive), Linux systemd
// user unit. Render* helpers are pure and unit-tested; Install/Uninstall/Status
// shell out to the platform tool.
package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Spec describes the hub service to install.
type Spec struct {
	Name      string // service/task name, default lanbus-hub
	ExePath   string // absolute path to lanbusd
	DataDir   string // hub data directory
	Port      int    // listen port, default 8788
	TokenFile string // optional token file
}

func (s Spec) withDefaults() Spec {
	if s.Name == "" {
		s.Name = "lanbus-hub"
	}
	if s.Port == 0 {
		s.Port = 8788
	}
	return s
}

// Label is the macOS launchd label / Linux unit base name.
func (s Spec) Label() string { return "com.lanbus." + s.withDefaults().Name }

func (s Spec) hubArgs() []string {
	s = s.withDefaults()
	args := []string{"--data-dir", s.DataDir, "--port", strconv.Itoa(s.Port)}
	if s.TokenFile != "" {
		args = append(args, "--token-file", s.TokenFile, "--worker-token-file", s.TokenFile)
	}
	return args
}

// RenderSystemd returns a systemd user unit for the hub.
func RenderSystemd(s Spec) string {
	s = s.withDefaults()
	var b strings.Builder
	b.WriteString("[Unit]\nDescription=lanbus hub (" + s.Name + ")\nAfter=network-online.target\n\n[Service]\n")
	b.WriteString("ExecStart=" + quoteSystemd(s.ExePath))
	for _, a := range s.hubArgs() {
		b.WriteString(" " + quoteSystemd(a))
	}
	b.WriteString("\nRestart=on-failure\nRestartSec=3\n\n[Install]\nWantedBy=default.target\n")
	return b.String()
}

func quoteSystemd(v string) string {
	if strings.ContainsAny(v, " \t\"") {
		return "\"" + strings.ReplaceAll(v, "\"", "\\\"") + "\""
	}
	return v
}

// RenderLaunchd returns a macOS launchd plist for the hub.
func RenderLaunchd(s Spec) string {
	s = s.withDefaults()
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>` + s.Label() + `</string>
  <key>ProgramArguments</key><array>
    <string>` + s.ExePath + `</string>
`)
	for _, a := range s.hubArgs() {
		b.WriteString("    <string>" + a + "</string>\n")
	}
	b.WriteString(`  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/tmp/` + s.Name + `.out</string>
  <key>StandardErrorPath</key><string>/tmp/` + s.Name + `.err</string>
</dict></plist>
`)
	return b.String()
}

// Install registers and starts the hub service for the current platform.
func Install(s Spec) error {
	s = s.withDefaults()
	if s.ExePath == "" {
		return fmt.Errorf("daemon: ExePath is required")
	}
	if s.DataDir == "" {
		home, _ := os.UserHomeDir()
		s.DataDir = filepath.Join(home, ".lanbus")
	}
	switch runtime.GOOS {
	case "windows":
		return run("powershell.exe", "-NoProfile", "-Command", RenderWindowsInstallPS(s))
	case "darwin":
		home, _ := os.UserHomeDir()
		dir := filepath.Join(home, "Library", "LaunchAgents")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		plist := filepath.Join(dir, s.Label()+".plist")
		if err := os.WriteFile(plist, []byte(RenderLaunchd(s)), 0o644); err != nil {
			return err
		}
		uid := strconv.Itoa(os.Getuid())
		_ = run("launchctl", "bootout", "gui/"+uid+"/"+s.Label())
		return run("launchctl", "bootstrap", "gui/"+uid, plist)
	case "linux":
		home, _ := os.UserHomeDir()
		dir := filepath.Join(home, ".config", "systemd", "user")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		unit := filepath.Join(dir, s.Name+".service")
		if err := os.WriteFile(unit, []byte(RenderSystemd(s)), 0o644); err != nil {
			return err
		}
		if err := run("systemctl", "--user", "daemon-reload"); err != nil {
			return err
		}
		return run("systemctl", "--user", "enable", "--now", s.Name+".service")
	default:
		return fmt.Errorf("daemon: unsupported platform %s", runtime.GOOS)
	}
}

// Uninstall removes the hub service.
func Uninstall(name string) error {
	if name == "" {
		name = "lanbus-hub"
	}
	s := Spec{Name: name}
	switch runtime.GOOS {
	case "windows":
		return run("powershell.exe", "-NoProfile", "-Command", RenderWindowsUninstallPS(name))
	case "darwin":
		home, _ := os.UserHomeDir()
		uid := strconv.Itoa(os.Getuid())
		_ = run("launchctl", "bootout", "gui/"+uid+"/"+s.Label())
		return os.Remove(filepath.Join(home, "Library", "LaunchAgents", s.Label()+".plist"))
	case "linux":
		_ = run("systemctl", "--user", "disable", "--now", name+".service")
		home, _ := os.UserHomeDir()
		return os.Remove(filepath.Join(home, ".config", "systemd", "user", name+".service"))
	default:
		return fmt.Errorf("daemon: unsupported platform %s", runtime.GOOS)
	}
}

// Status returns a human-readable service status.
func Status(name string) (string, error) {
	if name == "" {
		name = "lanbus-hub"
	}
	s := Spec{Name: name}
	switch runtime.GOOS {
	case "windows":
		ps := "$t = Get-ScheduledTask -TaskName " + psQuote(name) + " -ErrorAction SilentlyContinue; " +
			"if ($t) { $t | Get-ScheduledTaskInfo | Format-List TaskName,LastRunTime,LastTaskResult,NextRunTime; Write-Host ('State: ' + $t.State) } " +
			"else { $r = Get-ItemProperty -Path 'HKCU:\\Software\\Microsoft\\Windows\\CurrentVersion\\Run' -Name " + psQuote(name) + " -ErrorAction SilentlyContinue; " +
			"if ($r) { Write-Host 'HKCU Run autostart present' } else { Write-Host 'not installed' } }"
		return output("powershell.exe", "-NoProfile", "-Command", ps)
	case "darwin":
		return output("launchctl", "print", "gui/"+strconv.Itoa(os.Getuid())+"/"+s.Label())
	case "linux":
		return output("systemctl", "--user", "status", name+".service", "--no-pager")
	default:
		return "", fmt.Errorf("daemon: unsupported platform %s", runtime.GOOS)
	}
}

// hubArgString renders the hub flags as a single argument string, quoting
// values that contain spaces (for the scheduled-task action).
func (s Spec) hubArgString() string {
	args := s.hubArgs()
	var parts []string
	for i := 0; i+1 < len(args); i += 2 {
		k, v := args[i], args[i+1]
		if strings.ContainsAny(v, " \t") {
			parts = append(parts, k+" \""+v+"\"")
		} else {
			parts = append(parts, k+" "+v)
		}
	}
	return strings.Join(parts, " ")
}

// RenderWindowsInstallPS registers a per-user logon scheduled task (no admin
// required); if registration fails under a filtered token it falls back to the
// HKCU Run key, which also autostarts at logon without admin.
func RenderWindowsInstallPS(s Spec) string {
	s = s.withDefaults()
	name := psQuote(s.Name)
	return "$exe = " + psQuote(s.ExePath) + "\n" +
		"$argstr = " + psQuote(s.hubArgString()) + "\n" +
		"$action = New-ScheduledTaskAction -Execute $exe -Argument $argstr\n" +
		"$trigger = New-ScheduledTaskTrigger -AtLogOn\n" +
		"$settings = New-ScheduledTaskSettingsSet -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -StartWhenAvailable -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -ExecutionTimeLimit ([TimeSpan]::Zero)\n" +
		"try {\n" +
		"  Register-ScheduledTask -TaskName " + name + " -Action $action -Trigger $trigger -Settings $settings -Description 'lanbus hub' -Force -ErrorAction Stop | Out-Null\n" +
		"  Write-Host 'registered scheduled task'\n" +
		"} catch {\n" +
		"  $rk = 'HKCU:\\Software\\Microsoft\\Windows\\CurrentVersion\\Run'\n" +
		"  Set-ItemProperty -Path $rk -Name " + name + " -Value ('\"' + $exe + '\" ' + $argstr)\n" +
		"  Write-Host 'registered HKCU Run autostart (no admin)'\n" +
		"}\n"
}

// RenderWindowsUninstallPS removes both the scheduled task and the HKCU Run
// fallback entry.
func RenderWindowsUninstallPS(name string) string {
	n := psQuote(name)
	return "Unregister-ScheduledTask -TaskName " + n + " -Confirm:$false -ErrorAction SilentlyContinue\n" +
		"Remove-ItemProperty -Path 'HKCU:\\Software\\Microsoft\\Windows\\CurrentVersion\\Run' -Name " + n + " -ErrorAction SilentlyContinue\n" +
		"Write-Host 'daemon removed'\n"
}

func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func output(name string, args ...string) (string, error) {
	b, err := exec.Command(name, args...).CombinedOutput()
	return string(b), err
}
