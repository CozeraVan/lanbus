package scanner

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

type agentPattern struct {
	Kind string
	Name string
	Re   *regexp.Regexp
}

var agentPatterns = []agentPattern{
	{"claude", "Claude Code / Claude Desktop", regexp.MustCompile(`(?i)claude`)},
	{"codex", "Codex CLI / Codex desktop", regexp.MustCompile(`(?i)codex`)},
	{"cursor", "Cursor", regexp.MustCompile(`(?i)cursor`)},
	{"windsurf", "Windsurf", regexp.MustCompile(`(?i)windsurf`)},
	{"chatgpt", "ChatGPT desktop", regexp.MustCompile(`(?i)chatgpt|openai desktop`)},
	{"deepseek", "DeepSeek", regexp.MustCompile(`(?i)deepseek`)},
	{"copilot", "GitHub Copilot", regexp.MustCompile(`(?i)copilot|github copilot`)},
	{"aider", "Aider", regexp.MustCompile(`(?i)\baider\b`)},
	{"cline", "Cline", regexp.MustCompile(`(?i)\bcline\b`)},
	{"roo", "Roo Code", regexp.MustCompile(`(?i)roo[- ]?code|roocode`)},
	{"goose", "Goose", regexp.MustCompile(`(?i)\bgoose\b`)},
	{"opencode", "OpenCode", regexp.MustCompile(`(?i)opencode`)},
	{"gemini", "Gemini CLI", regexp.MustCompile(`(?i)gemini`)},
	{"qwen", "Qwen agent", regexp.MustCompile(`(?i)qwen`)},
	{"kimi", "Kimi", regexp.MustCompile(`(?i)kimi`)},
	{"perplexity", "Perplexity", regexp.MustCompile(`(?i)perplexity`)},
	{"manus", "Manus", regexp.MustCompile(`(?i)\bmanus\b`)},
	{"poe", "Poe", regexp.MustCompile(`(?i)\bpoe\b`)},
	{"monica", "Monica", regexp.MustCompile(`(?i)\bmonica\b`)},
	{"sider", "Sider", regexp.MustCompile(`(?i)\bsider\b`)},
	{"trae", "Trae", regexp.MustCompile(`(?i)\btrae\b`)},
	{"continue", "Continue", regexp.MustCompile(`(?i)continue\.dev|continueai|continue-ai`)},
	{"vscode", "VS Code + AI extension", regexp.MustCompile(`(?i)visual studio code|code\.exe|\bcode\b`)},
	{"zed", "Zed", regexp.MustCompile(`(?i)\bzed\b`)},
	{"ollama", "Ollama", regexp.MustCompile(`(?i)ollama`)},
	{"lmstudio", "LM Studio", regexp.MustCompile(`(?i)lm ?studio|lmstudio`)},
	{"localai", "LocalAI", regexp.MustCompile(`(?i)localai`)},
	{"llamacpp", "llama.cpp", regexp.MustCompile(`(?i)llama[-_. ]?(cpp|server)`)},
	{"vllm", "vLLM", regexp.MustCompile(`(?i)vllm`)},
	{"textgen", "text-generation-webui", regexp.MustCompile(`(?i)text-generation-webui|oobabooga`)},
	{"openwebui", "Open WebUI", regexp.MustCompile(`(?i)open[- ]?webui`)},
	{"comfyui", "ComfyUI", regexp.MustCompile(`(?i)comfyui|comfy`)},
	{"jan", "Jan", regexp.MustCompile(`(?i)^jan(\.exe)?$|\bjan\.ai\b`)},
	{"gpt4all", "GPT4All", regexp.MustCompile(`(?i)gpt4all`)},
	{"msty", "Msty", regexp.MustCompile(`(?i)msty`)},
	{"anythingllm", "AnythingLLM", regexp.MustCompile(`(?i)anythingllm`)},
	{"dify", "Dify", regexp.MustCompile(`(?i)\bdify\b`)},
	{"n8n", "n8n", regexp.MustCompile(`(?i)\bn8n\b`)},
	{"langflow", "Langflow", regexp.MustCompile(`(?i)langflow`)},
	{"sillytavern", "SillyTavern", regexp.MustCompile(`(?i)sillytavern`)},
	{"koboldcpp", "KoboldCpp", regexp.MustCompile(`(?i)koboldcpp`)},
	{"browser-use", "browser-use", regexp.MustCompile(`(?i)browser[-_]use`)},
	{"playwright", "Playwright", regexp.MustCompile(`(?i)playwright`)},
	{"selenium", "Selenium", regexp.MustCompile(`(?i)selenium`)},
	{"jupyter", "Jupyter", regexp.MustCompile(`(?i)jupyter|ipykernel`)},
}

var genericRuntime = regexp.MustCompile(`(?i)^(node|node\.exe|python\d*(\.exe)?|pythonw\.exe|uv|uv\.exe|bun|bun\.exe|deno|deno\.exe|java|java\.exe|ruby|go|go\.exe|npx|npx\.cmd|npm|npm\.cmd|pnpm|yarn)$`)
var helperName = regexp.MustCompile(`(?i)(service|helper|crashpad|renderer|gpu-process|utility|updater|update|sandbox|bridge|daemon)`)

var cliNames = map[string][]string{
	"claude": {"claude"}, "codex": {"codex"}, "kimi": {"kimi"},
	"deepseek": {"deepseek"}, "ollama": {"ollama"}, "lmstudio": {"lms", "lm-studio"},
	"cursor": {"cursor"}, "windsurf": {"windsurf"}, "aider": {"aider"},
	"goose": {"goose"}, "opencode": {"opencode"}, "gemini": {"gemini"},
	"qwen": {"qwen"}, "trae": {"trae"}, "jan": {"jan"}, "gpt4all": {"gpt4all"},
	"comfyui": {"comfy"}, "jupyter": {"jupyter"},
}

func runCmd(name string, args ...string) string {
	cmd := exec.Command(name, args...)
	hideWindow(cmd)
	out, _ := cmd.Output()
	return string(out)
}

func localProcesses() []Proc {
	switch runtime.GOOS {
	case "windows":
		return windowsProcesses()
	case "darwin":
		return psProcesses()
	default:
		return procProcesses()
	}
}

func windowsProcesses() []Proc {
	ps := `Get-CimInstance Win32_Process | Select-Object ProcessId,ParentProcessId,Name,CommandLine,ExecutablePath | ConvertTo-Json -Compress`
	text := runCmd("powershell", "-NoProfile", "-Command", ps)
	var raw []map[string]any
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		var one map[string]any
		if json.Unmarshal([]byte(text), &one) == nil {
			raw = []map[string]any{one}
		}
	}
	var out []Proc
	missing := false
	for _, p := range raw {
		pid := asInt(p["ProcessId"], 0)
		if pid == 0 {
			continue
		}
		cmdline := asString(p["CommandLine"])
		path := asString(p["ExecutablePath"])
		if path == "" {
			path = pathFromCommand(cmdline)
			missing = true
		}
		out = append(out, Proc{PID: pid, PPID: asInt(p["ParentProcessId"], 0),
			Name: asString(p["Name"]), Command: cmdline, Path: path, Scope: "local"})
	}
	if len(out) == 0 {
		text = runCmd("tasklist", "/FO", "CSV", "/NH")
		for _, line := range strings.Split(text, "\n") {
			parts := strings.Split(strings.TrimSpace(line), `","`)
			if len(parts) >= 2 {
				pid, _ := strconv.Atoi(strings.Trim(parts[1], `"`))
				name := strings.Trim(parts[0], `"`)
				out = append(out, Proc{PID: pid, Name: name, Command: name, Scope: "local"})
			}
		}
	}
	if missing {
		svc := `Get-CimInstance Win32_Service | Select-Object ProcessId,PathName | ConvertTo-Json -Compress`
		text := runCmd("powershell", "-NoProfile", "-Command", svc)
		var list []map[string]any
		if err := json.Unmarshal([]byte(text), &list); err != nil {
			var one map[string]any
			if json.Unmarshal([]byte(text), &one) == nil {
				list = []map[string]any{one}
			}
		}
		smap := map[int]string{}
		for _, s := range list {
			pid := asInt(s["ProcessId"], 0)
			raw := asString(s["PathName"])
			if pid > 0 && raw != "" {
				smap[pid] = firstPath(raw)
			}
		}
		for i := range out {
			if out[i].Path == "" && smap[out[i].PID] != "" {
				out[i].Path = smap[out[i].PID]
			}
		}
	}
	return out
}

func psProcesses() []Proc {
	text := runCmd("ps", "-axo", "pid=,ppid=,comm=,args=")
	var out []Proc
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, _ := strconv.Atoi(fields[0])
		ppid, _ := strconv.Atoi(fields[1])
		comm := fields[2]
		cmd := strings.Join(fields[3:], " ")
		path := ""
		if filepath.IsAbs(comm) {
			path = comm
		}
		out = append(out, Proc{PID: pid, PPID: ppid, Name: filepath.Base(comm),
			Command: cmd, Path: path, Scope: "local"})
	}
	return out
}

func procProcesses() []Proc {
	var out []Proc
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		comm, _ := os.ReadFile("/proc/" + e.Name() + "/comm")
		cmdline, _ := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		path, _ := os.Readlink("/proc/" + e.Name() + "/exe")
		cmd := strings.ReplaceAll(string(cmdline), "\x00", " ")
		out = append(out, Proc{PID: pid, Name: strings.TrimSpace(string(comm)),
			Command: strings.TrimSpace(cmd), Path: path, Scope: "local"})
	}
	return out
}

func wslProcesses() []Proc {
	if _, err := exec.LookPath("wsl.exe"); err != nil {
		return nil
	}
	raw, _ := exec.Command("wsl.exe", "-l", "-v").Output()
	text := decodeWSL(raw)
	var distros []string
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		for i := 0; i+1 < len(fields); i++ {
			if strings.EqualFold(fields[i+1], "Running") {
				distros = append(distros, strings.Trim(fields[i], "*"))
				break
			}
		}
	}
	var out []Proc
	script := `ps -eo pid=,ppid=,comm=,args= | while read pid ppid comm args; do exe=$(readlink -f /proc/$pid/exe 2>/dev/null); printf '%s\t%s\t%s\t%s\t%s\n' "$pid" "$ppid" "$comm" "$exe" "$args"; done`
	for _, d := range distros {
		raw, err := exec.Command("wsl.exe", "-d", d, "--exec", "sh", "-lc", script).Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			p := strings.SplitN(line, "\t", 5)
			if len(p) < 4 {
				continue
			}
			pid, _ := strconv.Atoi(p[0])
			ppid, _ := strconv.Atoi(p[1])
			out = append(out, Proc{PID: pid, PPID: ppid, Name: p[2],
				Path: p[3], Command: strings.TrimSpace(p[4]), Scope: "wsl:" + d})
		}
	}
	return out
}

func decodeWSL(b []byte) string {
	if len(b) >= 2 && b[0] == 0xff && b[1] == 0xfe {
		u := make([]rune, 0, len(b)/2)
		for i := 2; i+1 < len(b); i += 2 {
			u = append(u, rune(uint16(b[i])|uint16(b[i+1])<<8))
		}
		return string(u)
	}
	return strings.ReplaceAll(string(b), "\x00", "")
}

func pathFromCommand(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}
	var p string
	if strings.HasPrefix(cmd, `"`) {
		if i := strings.Index(cmd[1:], `"`); i >= 0 {
			p = cmd[1 : 1+i]
		}
	} else {
		p = strings.Fields(cmd)[0]
	}
	if p != "" && (filepath.IsAbs(p) || strings.HasSuffix(strings.ToLower(p), ".exe")) {
		return p
	}
	return ""
}

func firstPath(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, `"`) {
		if i := strings.Index(s[1:], `"`); i >= 0 {
			return s[1 : 1+i]
		}
	}
	f := strings.Fields(s)
	if len(f) > 0 {
		return f[0]
	}
	return ""
}

type agentGroup struct {
	kind, name, scope string
	pids              []int
	processes         []string
	helpers           []string
	direct            []string
	paths             []string
	helperPaths       []string
	command           string
}

func detectLocalAgents(procs []Proc) []map[string]any {
	groups := map[string]*agentGroup{}
	for _, p := range procs {
		kind, direct := kindForProcess(p.Name, p.Command)
		if kind == "" {
			continue
		}
		scope := p.Scope
		if scope == "" {
			scope = "local"
		}
		key := kind + "|" + scope
		g := groups[key]
		if g == nil {
			label := kind
			for _, ap := range agentPatterns {
				if ap.Kind == kind {
					label = ap.Name
					break
				}
			}
			g = &agentGroup{kind: kind, name: label, scope: scope}
			groups[key] = g
		}
		found := false
		for _, x := range g.pids {
			if x == p.PID {
				found = true
			}
		}
		if !found && p.PID > 0 {
			g.pids = append(g.pids, p.PID)
		}
		isHelper := helperName.MatchString(p.Name)
		if direct && !isHelper {
			if !containsFold(g.processes, p.Name) {
				g.processes = append(g.processes, p.Name)
			}
			if !containsFold(g.direct, p.Name) {
				g.direct = append(g.direct, p.Name)
			}
			if p.Path != "" && !contains(g.paths, p.Path) {
				g.paths = append(g.paths, p.Path)
			}
		} else {
			if !containsFold(g.helpers, p.Name) {
				g.helpers = append(g.helpers, p.Name)
			}
			if p.Path != "" && !contains(g.helperPaths, p.Path) {
				g.helperPaths = append(g.helperPaths, p.Path)
			}
		}
		if g.command == "" && p.Command != "" {
			g.command = redact(p.Command)
		}
	}
	out := []map[string]any{}
	for _, g := range groups {
		if len(g.paths) == 0 {
			for _, name := range cliNames[g.kind] {
				if p, err := exec.LookPath(name); err == nil {
					g.paths = append(g.paths, p)
					break
				}
			}
		}
		path := ""
		if len(g.paths) > 0 {
			path = g.paths[0]
		} else if len(g.helperPaths) > 0 {
			path = g.helperPaths[0]
		}
		instances := len(g.direct)
		if instances < 1 {
			instances = 1
		}
		process := strings.Join(g.processes, ", ")
		if process == "" {
			process = strings.Join(g.helpers, ", ")
		}
		out = append(out, map[string]any{
			"kind": g.kind, "name": g.name, "scope": g.scope,
			"pids": g.pids, "processes": g.processes, "helpers": g.helpers,
			"direct": g.direct, "paths": g.paths, "helper_paths": g.helperPaths,
			"path": path, "command": g.command, "instances": instances,
			"process": process, "source": "process",
		})
	}
	sort.Slice(out, func(i, j int) bool {
		a := asString(out[i]["name"])
		b := asString(out[j]["name"])
		if a == b {
			return asString(out[i]["scope"]) < asString(out[j]["scope"])
		}
		return a < b
	})
	return out
}

func kindForProcess(name, command string) (string, bool) {
	n := strings.ToLower(filepath.Base(name))
	c := strings.ToLower(command)
	for _, ap := range agentPatterns {
		if ap.Re.MatchString(n) {
			if ap.Kind == "vscode" && !regexp.MustCompile(`(?i)copilot|continue|cline|roo|codeium|tabnine|ai`).MatchString(c) {
				continue
			}
			return ap.Kind, true
		}
	}
	if !genericRuntime.MatchString(n) {
		return "", false
	}
	rules := []struct {
		Kind string
		Re   *regexp.Regexp
	}{
		{"claude", regexp.MustCompile(`(?i)claude-code|@anthropic-ai/claude|/claude-code/|\bclaude\.js\b`)},
		{"codex", regexp.MustCompile(`(?i)@openai/codex|/codex(-cli)?/|\bcodex\.js\b|\bcodex\b.*(exec|--)`)},
		{"aider", regexp.MustCompile(`(?i)(^|[/\ ])aider(\.py)?([/\ ]|$)`)},
		{"goose", regexp.MustCompile(`(?i)\bgoose\b`)},
		{"open-interpreter", regexp.MustCompile(`(?i)open[-_]interpreter|\binterpreter\b`)},
		{"opencode", regexp.MustCompile(`(?i)\bopencode\b`)},
		{"gemini", regexp.MustCompile(`(?i)gemini-cli|@google/gemini`)},
		{"qwen", regexp.MustCompile(`(?i)qwen[-_]?agent|qwen-code`)},
		{"kimi", regexp.MustCompile(`(?i)kimi[-_]?(cli|bridge|desktop)`)},
		{"deepseek", regexp.MustCompile(`(?i)deepseek`)},
		{"continue", regexp.MustCompile(`(?i)continue\.dev|continueai|continue-ai`)},
		{"browser-use", regexp.MustCompile(`(?i)browser[-_]use`)},
		{"playwright", regexp.MustCompile(`(?i)playwright`)},
		{"selenium", regexp.MustCompile(`(?i)selenium`)},
		{"jupyter", regexp.MustCompile(`(?i)jupyter|ipykernel`)},
		{"vllm", regexp.MustCompile(`(?i)\bvllm\b`)},
		{"textgen", regexp.MustCompile(`(?i)text[-_]generation[-_]webui|oobabooga`)},
		{"comfyui", regexp.MustCompile(`(?i)comfyui|comfy`)},
		{"openwebui", regexp.MustCompile(`(?i)open[-_]?webui`)},
	}
	for _, r := range rules {
		if r.Re.MatchString(c) {
			return r.Kind, false
		}
	}
	return "", false
}

func redact(s string) string {
	re := regexp.MustCompile(`(?i)((token|api[_-]?key|secret|password|passwd)\s*[=:]\s*)\S+`)
	s = re.ReplaceAllString(s, "$1***")
	if len(s) > 400 {
		s = s[:400]
	}
	return s
}

func listeningPorts(procs []Proc) []Listener {
	names := map[int]string{}
	for _, p := range procs {
		names[p.PID] = p.Name
	}
	var out []Listener
	seen := map[string]bool{}
	switch runtime.GOOS {
	case "windows":
		text := runCmd("netstat", "-ano", "-p", "tcp")
		for _, line := range strings.Split(text, "\n") {
			f := strings.Fields(line)
			if len(f) >= 5 && strings.EqualFold(f[0], "TCP") && strings.EqualFold(f[3], "LISTENING") {
				port := lastPort(f[1])
				pid, _ := strconv.Atoi(f[4])
				if port > 0 {
					k := f[1] + "|" + strconv.Itoa(pid)
					if !seen[k] {
						seen[k] = true
						out = append(out, Listener{Port: port, Addr: f[1], PID: pid, Process: names[pid]})
					}
				}
			}
		}
	case "darwin":
		text := runCmd("lsof", "-nP", "-iTCP", "-sTCP:LISTEN")
		for _, line := range strings.Split(text, "\n") {
			f := strings.Fields(line)
			if len(f) < 9 {
				continue
			}
			port := lastPort(f[8])
			pid, _ := strconv.Atoi(f[1])
			if port > 0 {
				out = append(out, Listener{Port: port, Addr: f[8], PID: pid, Process: f[0]})
			}
		}
	default:
		text := runCmd("ss", "-ltnp")
		if strings.TrimSpace(text) != "" {
			for _, line := range strings.Split(text, "\n")[1:] {
				f := strings.Fields(line)
				if len(f) < 4 {
					continue
				}
				port := lastPort(f[3])
				pid := 0
				if m := regexp.MustCompile(`pid=(\d+)`).FindStringSubmatch(line); m != nil {
					pid, _ = strconv.Atoi(m[1])
				}
				proc := names[pid]
				if m := regexp.MustCompile(`users:\(\("([^"]+)"`).FindStringSubmatch(line); m != nil {
					proc = m[1]
				}
				if port > 0 {
					out = append(out, Listener{Port: port, Addr: f[3], PID: pid, Process: proc})
				}
			}
		}
	}
	return out
}

func lastPort(addr string) int {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return 0
	}
	p, _ := strconv.Atoi(addr[i+1:])
	return p
}

func localServicesFor(listeners []Listener, interesting []int, timeout float64) []map[string]any {
	allow := map[int]bool{}
	for _, p := range interesting {
		allow[p] = true
	}
	byPort := map[int][]Listener{}
	for _, l := range listeners {
		if allow[l.Port] {
			byPort[l.Port] = append(byPort[l.Port], l)
		}
	}
	var out []map[string]any
	for port, ls := range byPort {
		var svcs []map[string]any
		if port == 22 {
			svcs = []map[string]any{{"kind": "ssh", "name": "ssh", "port": 22, "transport": "tcp", "ai": false}}
		} else {
			svcs = probeHTTP("127.0.0.1", port, port == 443, timeout)
			if len(svcs) == 0 {
				kind := "tcp"
				if port == 443 {
					kind = "tls"
				}
				svcs = []map[string]any{{"kind": kind, "name": kind, "port": port, "transport": "tcp", "ai": false}}
			}
		}
		for _, s := range svcs {
			s["pid"] = ls[0].PID
			s["process"] = ls[0].Process
			out = append(out, s)
		}
	}
	return out
}

func arpTable() map[string]string {
	out := map[string]string{}
	if runtime.GOOS == "linux" {
		if b, err := os.ReadFile("/proc/net/arp"); err == nil {
			for _, line := range strings.Split(string(b), "\n")[1:] {
				f := strings.Fields(line)
				if len(f) >= 4 && netParseIP(f[0]) {
					out[f[0]] = f[3]
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	text := runCmd("arp", "-a")
	reIP := regexp.MustCompile(`(\d{1,3}(?:\.\d{1,3}){3})`)
	reMAC := regexp.MustCompile(`(?i)([0-9a-f]{1,2}[:-]){5}[0-9a-f]{1,2}`)
	for _, line := range strings.Split(text, "\n") {
		im := reIP.FindStringSubmatch(line)
		if im == nil {
			continue
		}
		mm := reMAC.FindString(line)
		if mm == "" || strings.HasPrefix(strings.ToLower(mm), "00:00") {
			continue
		}
		out[im[1]] = mm
	}
	return out
}

func netParseIP(s string) bool {
	return len(strings.Split(s, ".")) == 4
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func containsFold(xs []string, s string) bool {
	for _, x := range xs {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

func readLines(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}
