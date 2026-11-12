// lanbus is the native product CLI (no Python required): version, healthcheck,
// backup/restore of the hub state, and daemon install/uninstall/status.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/CozeraVan/lanbus/go/internal/backup"
	"github.com/CozeraVan/lanbus/go/internal/daemon"
	"github.com/CozeraVan/lanbus/go/internal/peerui"
	"github.com/CozeraVan/lanbus/go/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "version", "--version", "-v":
		fmt.Println("lanbus " + version.String())
	case "healthcheck":
		err = healthcheck(args)
	case "peer-ui":
		err = peerUI(args, openBrowser)
	case "backup":
		err = backupCmd(args)
	case "restore":
		err = restoreCmd(args)
	case "daemon":
		err = daemonCmd(args)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintln(os.Stderr, "lanbus: unknown command", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "lanbus:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`lanbus ` + version.String() + ` — native product CLI

Usage:
  lanbus version                          print version
  lanbus healthcheck [--url URL]          probe hub /health (exit 0=ok)
  lanbus peer-ui --name NAME [--data-dir DIR]
  lanbus backup [--data-dir DIR] [--out FILE]
  lanbus restore [--data-dir DIR] ARCHIVE
  lanbus daemon install|uninstall|status [--name N] [--exe PATH]
                 [--data-dir DIR] [--port N] [--token-file F]
`)
}

func defaultDataDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".lanbus")
}

func peerUI(args []string, opener func(string) error) error {
	fs := flag.NewFlagSet("peer-ui", flag.ContinueOnError)
	name := fs.String("name", "", "direct peer name")
	dataDir := fs.String("data-dir", "", "peer data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || *name == "." || *name == ".." || filepath.IsAbs(*name) || strings.ContainsAny(*name, `/\\`) {
		return fmt.Errorf("peer-ui: --name must be a single peer directory name")
	}
	dir := *dataDir
	if dir == "" {
		dir = filepath.Join(defaultDataDir(), "peers", *name)
	}
	approvalURL, err := peerui.ApprovalURL(dir)
	if err != nil {
		return err
	}
	fmt.Printf("审批页面：%s（密钥经 URL 片段自动带入，不会发往服务器）\n", strings.Split(approvalURL, "#")[0])
	return opener(approvalURL)
}

func openBrowser(rawURL string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", rawURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		cmd = exec.Command("xdg-open", rawURL)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("peer-ui: open browser: %w", err)
	}
	return nil
}

func healthcheck(args []string) error {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	url := fs.String("url", "http://127.0.0.1:8788", "hub base URL")
	timeout := fs.Duration("timeout", 5*time.Second, "probe timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	client := &http.Client{Timeout: *timeout}
	resp, err := client.Get(*url + "/health")
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return fmt.Errorf("healthcheck: status %d", resp.StatusCode)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return fmt.Errorf("healthcheck: invalid JSON response: %w", err)
	}
	if ok, _ := parsed["ok"].(bool); !ok {
		return fmt.Errorf("healthcheck: ok=false")
	}
	fmt.Println(string(body))
	return nil
}

func backupCmd(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "hub data directory")
	out := fs.String("out", "", "output archive (default <data-dir>/backups/<ts>.zip)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path, err := backup.Create(*dataDir, *out)
	if err != nil {
		return err
	}
	fmt.Println("backup written:", path)
	return nil
}

func restoreCmd(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	dataDir := fs.String("data-dir", defaultDataDir(), "hub data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("restore: ARCHIVE argument required")
	}
	if err := backup.Restore(fs.Arg(0), *dataDir); err != nil {
		return err
	}
	fmt.Println("restored into:", *dataDir)
	return nil
}

func daemonCmd(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("daemon: install|uninstall|status required")
	}
	action, rest := args[0], args[1:]
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	name := fs.String("name", "lanbus-hub", "service/task name")
	exe := fs.String("exe", defaultHubExe(), "path to lanbusd binary")
	dataDir := fs.String("data-dir", defaultDataDir(), "hub data directory")
	port := fs.Int("port", 8788, "hub listen port")
	tokenFile := fs.String("token-file", "", "token file for the hub")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	spec := daemon.Spec{Name: *name, ExePath: *exe, DataDir: *dataDir, Port: *port, TokenFile: *tokenFile}
	switch action {
	case "install":
		if err := daemon.Install(spec); err != nil {
			return err
		}
		fmt.Println("daemon installed:", *name)
	case "uninstall":
		if err := daemon.Uninstall(*name); err != nil {
			return err
		}
		fmt.Println("daemon uninstalled:", *name)
	case "status":
		out, err := daemon.Status(*name)
		fmt.Print(out)
		return err
	default:
		return fmt.Errorf("daemon: unknown action %s", action)
	}
	return nil
}

// defaultHubExe guesses the lanbusd binary next to this CLI.
func defaultHubExe() string {
	self, err := os.Executable()
	if err != nil {
		return "lanbusd"
	}
	name := "lanbusd"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(filepath.Dir(self), name)
}
