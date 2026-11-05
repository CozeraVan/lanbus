package scanner

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

var basePorts = []int{
	22, 80, 443, 1234, 3000, 3001, 3080, 4000, 5000, 5001, 5173, 5678,
	7000, 7860, 7861, 8000, 8001, 8008, 8080, 8081, 8082, 8083, 8084,
	8088, 8090, 8095, 8188, 8501, 8888, 9000, 9090, 11434, 12345,
	18080, 18789, 30000,
}

const (
	maxHosts = 1024
	maxPorts = 2048
)

type Iface struct {
	IP      string `json:"ip"`
	Prefix  int    `json:"prefix"`
	Network string `json:"network"`
	Iface   string `json:"iface"`
	Mask    string `json:"mask"`
}

type Proc struct {
	PID     int
	PPID    int
	Name    string
	Command string
	Path    string
	Scope   string
}

type Listener struct {
	Port    int
	Addr    string
	PID     int
	Process string
}

func logf(format string, args ...any) {
	if os.Getenv("LANBUS_SCAN_LOG") != "" {
		fmt.Fprintf(os.Stderr, "[scan] "+format+"\n", args...)
	}
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func asFloat(v any, def float64) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		if f != 0 {
			return f
		}
	}
	return def
}

func asInt(v any, def int) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(x)
		return n
	}
	return def
}

func asBool(v any, def bool) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "1" || strings.EqualFold(x, "true") || strings.EqualFold(x, "yes")
	case float64:
		return x != 0
	case int:
		return x != 0
	}
	return def
}

func parsePorts(v any, deep bool) []int {
	set := map[int]bool{}
	for _, p := range basePorts {
		set[p] = true
	}
	if deep {
		for p := 1; p <= 1024; p++ {
			set[p] = true
		}
	}
	var parts []string
	switch x := v.(type) {
	case string:
		parts = strings.FieldsFunc(x, func(r rune) bool {
			return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t'
		})
	case []any:
		for _, y := range x {
			parts = append(parts, fmt.Sprint(y))
		}
	case []string:
		parts = x
	}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			ab := strings.SplitN(part, "-", 2)
			a, _ := strconv.Atoi(strings.TrimSpace(ab[0]))
			b, _ := strconv.Atoi(strings.TrimSpace(ab[1]))
			if a > 0 && b >= a && b < 65536 {
				if b-a > maxPorts {
					b = a + maxPorts
				}
				for p := a; p <= b; p++ {
					set[p] = true
				}
			}
			continue
		}
		if p, err := strconv.Atoi(part); err == nil && p > 0 && p < 65536 {
			set[p] = true
		}
	}
	out := make([]int, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Ints(out)
	if len(out) > maxPorts {
		out = out[:maxPorts]
	}
	return out
}

func localInterfaces() []Iface {
	var out []Iface
	seen := map[string]bool{}
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			var ip net.IP
			var mask net.IPMask
			switch x := a.(type) {
			case *net.IPNet:
				ip, mask = x.IP, x.Mask
			case *net.IPAddr:
				ip = x.IP
			}
			ip4 := ip.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			if mask == nil {
				mask = net.CIDRMask(24, 32)
			}
			ones, bits := mask.Size()
			if bits != 32 {
				ones = 24
			}
			netIP := ip4.Mask(mask)
			key := ip4.String() + "/" + strconv.Itoa(ones) + "/" + ifc.Name
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, Iface{
				IP:      ip4.String(),
				Prefix:  ones,
				Network: netIP.String() + "/" + strconv.Itoa(ones),
				Iface:   ifc.Name,
				Mask:    net.IP(mask).String(),
			})
		}
	}
	if len(out) == 0 {
		if ip := primaryIP(); ip != "" {
			ip4 := net.ParseIP(ip).To4()
			out = append(out, Iface{IP: ip, Prefix: 24,
				Network: ip4.Mask(net.CIDRMask(24, 32)).String() + "/24",
				Iface:   "default", Mask: "255.255.255.0"})
		}
	}
	return out
}

func primaryIP() string {
	c, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return ""
}

func ipToUint(ip net.IP) uint32 {
	ip4 := ip.To4()
	if ip4 == nil {
		return 0
	}
	return uint32(ip4[0])<<24 | uint32(ip4[1])<<16 | uint32(ip4[2])<<8 | uint32(ip4[3])
}

func uintToIP(v uint32) string {
	return fmt.Sprintf("%d.%d.%d.%d", byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func targets(cidr string, ifaces []Iface, arp map[string]string, max int) ([]string, []string, bool) {
	if max <= 0 {
		max = maxHosts
	}
	var nets []*net.IPNet
	var netStrs []string
	if strings.TrimSpace(cidr) != "" {
		for _, part := range strings.FieldsFunc(cidr, func(r rune) bool {
			return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t'
		}) {
			if _, n, err := net.ParseCIDR(part); err == nil {
				nets = append(nets, n)
				netStrs = append(netStrs, n.String())
			}
		}
	} else {
		for _, i := range ifaces {
			if isVirtualIface(i.Iface) {
				continue
			}
			_, n, err := net.ParseCIDR(i.Network)
			if err != nil {
				continue
			}
			ones, _ := n.Mask.Size()
			if ones < 24 {
				ip := net.ParseIP(i.IP).To4()
				n = &net.IPNet{IP: ip.Mask(net.CIDRMask(24, 32)), Mask: net.CIDRMask(24, 32)}
			}
			nets = append(nets, n)
			netStrs = append(netStrs, n.String())
		}
	}
	ips := map[uint32]bool{}
	for _, n := range nets {
		start := ipToUint(n.IP)
		ones, bits := n.Mask.Size()
		if bits != 32 {
			continue
		}
		size := uint32(1) << uint(32-ones)
		if size <= 1 {
			ips[start] = true
			continue
		}
		for v := start + 1; v < start+size-1; v++ {
			ips[v] = true
			if len(ips) >= max+1 {
				break
			}
		}
	}
	if strings.TrimSpace(cidr) == "" {
		for ip := range arp {
			if p := net.ParseIP(ip).To4(); p != nil {
				ips[ipToUint(p)] = true
			}
		}
	}
	vals := make([]uint32, 0, len(ips))
	for v := range ips {
		vals = append(vals, v)
	}
	sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
	truncated := false
	if len(vals) > max {
		vals = vals[:max]
		truncated = true
	}
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		out = append(out, uintToIP(v))
	}
	return out, netStrs, truncated
}

func isVirtualIface(name string) bool {
	n := strings.ToLower(name)
	for _, p := range []string{"docker", "veth", "br-", "virbr", "vmnet", "vboxnet", "vethernet", "lo"} {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

func guessOS(ports []int) string {
	set := map[int]bool{}
	for _, p := range ports {
		set[p] = true
	}
	for _, p := range []int{5000, 7000, 62078, 548, 3689} {
		if set[p] {
			return "macos"
		}
	}
	for _, p := range []int{135, 139, 445, 3389, 5985, 5986} {
		if set[p] {
			return "windows"
		}
	}
	if set[22] {
		return "unix"
	}
	return ""
}

func Discover(spec map[string]any) (map[string]any, error) {
	start := time.Now()
	if spec == nil {
		spec = map[string]any{}
	}
	timeout := asFloat(spec["timeout"], 0.22)
	if timeout <= 0 {
		timeout = 0.22
	}
	httpTimeout := asFloat(spec["http_timeout"], 1.5)
	if httpTimeout <= 0 {
		httpTimeout = 1.5
	}
	deep := asBool(spec["deep"], false)
	ports := parsePorts(spec["ports"], deep)
	max := asInt(spec["max_hosts"], maxHosts)
	if max <= 0 {
		max = maxHosts
	}
	includeLocal := asBool(spec["include_local"], true)
	includeProcesses := asBool(spec["include_processes"], true)
	includeWSL := asBool(spec["include_wsl"], true)

	logf("interfaces / processes / listeners")
	ifaces := localInterfaces()
	baseProcs := []Proc{}
	if includeLocal || includeProcesses {
		baseProcs = localProcesses()
	}
	wslProcs := []Proc{}
	if includeProcesses && includeWSL && runtime.GOOS == "windows" {
		wslProcs = wslProcesses()
	}
	localAgents := []map[string]any{}
	if includeProcesses {
		localAgents = detectLocalAgents(append(baseProcs, wslProcs...))
	}
	listeners := []Listener{}
	if includeLocal {
		listeners = listeningPorts(baseProcs)
	}
	localServices := []map[string]any{}
	if includeLocal {
		localServices = localServicesFor(listeners, ports, httpTimeout)
	}
	arp := arpTable()
	targetList, nets, truncated := targets(asString(spec["cidr"]), ifaces, arp, max)
	logf("targets=%d nets=%v ports=%d timeout=%.2f", len(targetList), nets, len(ports), timeout)

	hosts := scanHosts(targetList, ports, timeout, httpTimeout, arp)
	sort.Slice(hosts, func(i, j int) bool {
		return ipToUint(net.ParseIP(asString(hosts[i]["ip"]))) < ipToUint(net.ParseIP(asString(hosts[j]["ip"])))
	})

	agents := []map[string]any{}
	hostWithAgent := map[string]bool{}
	for _, h := range hosts {
		hAgents, _ := h["agents"].([]map[string]any)
		for _, a := range hAgents {
			x := map[string]any{}
			for k, v := range a {
				x[k] = v
			}
			x["host"] = h["ip"]
			x["os_guess"] = h["os_guess"]
			agents = append(agents, x)
			hostWithAgent[asString(h["ip"])] = true
		}
	}
	for _, a := range localAgents {
		x := map[string]any{}
		for k, v := range a {
			x[k] = v
		}
		x["host"] = "local"
		x["models"] = []string{}
		agents = append(agents, x)
	}
	alive := 0
	for _, h := range hosts {
		if asBool(h["alive"], false) {
			alive++
		}
	}
	kinds := map[string]bool{}
	for _, a := range agents {
		if k := asString(a["kind"]); k != "" {
			kinds[k] = true
		}
	}
	kindList := make([]string, 0, len(kinds))
	for k := range kinds {
		kindList = append(kindList, k)
	}
	sort.Strings(kindList)
	localInstances := 0
	for _, a := range localAgents {
		localInstances += asInt(a["instances"], 1)
	}
	summary := map[string]any{
		"hosts_in_scope":        len(targetList),
		"hosts_alive":           alive,
		"hosts_with_agents":     len(hostWithAgent),
		"agent_endpoints":       len(agents) - len(localAgents),
		"local_agents":          len(localAgents),
		"local_agent_instances": localInstances,
		"agent_kinds":           kindList,
		"ports_scanned":         len(ports),
		"truncated":             truncated,
		"deep":                  deep,
	}
	rep := map[string]any{
		"ok":             true,
		"engine":         "go",
		"generated_at":   time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		"duration_s":     float64(time.Since(start).Milliseconds()) / 1000.0,
		"cancelled":      false,
		"host":           hostname(),
		"platform":       runtime.GOOS,
		"scope":          map[string]any{"networks": nets, "cidr": asString(spec["cidr"]), "ports": ports, "max_hosts": max},
		"interfaces":     ifaces,
		"local_agents":   localAgents,
		"local_services": localServices,
		"hosts":          hosts,
		"agents":         agents,
		"summary":        summary,
		"notes": []string{
			"进程证据只在本机有效；远端主机只能通过开放端口/HTTP 指纹发现。",
			"GUI-only agent 没有监听端口时，需要在该主机运行 lanbus worker 或 lanbus discover。",
		},
	}
	logf("done %v", summary)
	return rep, nil
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}
