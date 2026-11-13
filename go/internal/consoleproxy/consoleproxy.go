// Package consoleproxy serves the direct-peer command console inside the lanbus
// desktop window. It proxies a chosen local peer's loopback console page and API
// calls, injecting the approval secret server-side so the embedded page needs no
// URL fragment. Loopback-only; peer names are single path segments (no traversal).
package consoleproxy

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Handler resolves local peers under Home/.lanbus/peers and serves the console.
type Handler struct{ Home string }

type target struct{ url, secret string }

func (h *Handler) resolve(name string) (target, error) {
	if name == "" || name == "." || name == ".." || filepath.IsAbs(name) || strings.ContainsAny(name, `/\`) {
		return target{}, fmt.Errorf("invalid peer name")
	}
	dir := filepath.Join(h.Home, ".lanbus", "peers", name)
	raw, err := os.ReadFile(filepath.Join(dir, "peer.json"))
	if err != nil {
		return target{}, err
	}
	var info struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return target{}, err
	}
	u, err := url.Parse(strings.TrimSpace(info.URL))
	if err != nil || u.Scheme != "http" || !isLoopback(u.Hostname()) || u.User != nil {
		return target{}, fmt.Errorf("peer url must be loopback http")
	}
	secRaw, err := os.ReadFile(filepath.Join(dir, "approval.json"))
	if err != nil {
		return target{}, err
	}
	var sec struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(secRaw, &sec); err != nil || sec.Secret == "" {
		return target{}, fmt.Errorf("invalid approval secret")
	}
	return target{url: strings.TrimRight(strings.TrimSpace(info.URL), "/"), secret: sec.Secret}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/console" || path == "/console/" {
		h.launcher(w)
		return
	}
	rest := strings.TrimPrefix(path, "/console/")
	name, sub, _ := strings.Cut(rest, "/")
	tgt, err := h.resolve(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if strings.Contains(sub, "..") {
		http.NotFound(w, r)
		return
	}
	if sub == "" {
		h.consolePage(w, name, tgt)
		return
	}
	h.proxy(w, r, tgt, sub)
}

// consolePage fetches the peer's console HTML and injects the API base plus a
// Wails navigation listener so the desktop window can switch peers in-place.
func (h *Handler) consolePage(w http.ResponseWriter, name string, tgt target) {
	resp, err := http.Get(tgt.url + "/peer/console")
	if err != nil {
		http.Error(w, "peer offline", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	baseJS, _ := json.Marshal("/console/" + name)
	inject := "<script>window.__LANBUS_BASE__=" + string(baseJS) +
		";(function(){function reg(){if(window.runtime&&runtime.EventsOn){runtime.EventsOn('lanbus-navigate',function(u){location.href=u})}else{setTimeout(reg,50)}}reg();})();</script>"
	page := strings.Replace(string(raw), "</head>", inject+"</head>", 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(page))
}

// proxy forwards /console/<name>/<sub> to the peer's loopback server, adding the
// approval secret so the embedded page is authorized without a URL fragment.
func (h *Handler) proxy(w http.ResponseWriter, r *http.Request, tgt target, sub string) {
	u, err := url.Parse(tgt.url)
	if err != nil {
		http.Error(w, "bad peer url", http.StatusBadGateway)
		return
	}
	p := httputil.NewSingleHostReverseProxy(u)
	orig := p.Director
	p.Director = func(req *http.Request) {
		orig(req)
		req.URL.Path = "/" + sub
		req.URL.RawQuery = r.URL.RawQuery
		req.Host = u.Host
		req.Header.Set("X-Lanbus-Approval", tgt.secret)
	}
	p.ServeHTTP(w, r)
}

func (h *Handler) launcher(w http.ResponseWriter) {
	root := filepath.Join(h.Home, ".lanbus", "peers")
	items, _ := os.ReadDir(root)
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width, initial-scale=1"><title>lanbus · 指挥台</title>` +
		`<style>body{margin:0;background:#0d1117;color:#e6edf3;font:15px/1.6 ui-sans-serif,system-ui,"Segoe UI",sans-serif;padding:32px}` +
		`h1{font-size:20px}a{color:#2f81f7;text-decoration:none}li{margin:8px 0}.muted{color:#8b949e}</style></head><body>` +
		`<h1>lanbus 指挥台</h1><p class="muted">选择本机直连 peer 打开指挥台：</p><ul>`)
	n := 0
	for _, it := range items {
		if !it.IsDir() {
			continue
		}
		if _, err := h.resolve(it.Name()); err != nil {
			continue
		}
		esc := html.EscapeString(it.Name())
		b.WriteString(`<li><a href="/console/` + esc + `">` + esc + `</a></li>`)
		n++
	}
	if n == 0 {
		b.WriteString(`<li class="muted">暂无本机直连 peer（先运行 lanbus-worker --direct --name &lt;名称&gt;）。</li>`)
	}
	b.WriteString(`</ul></body></html>`)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(b.String()))
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
