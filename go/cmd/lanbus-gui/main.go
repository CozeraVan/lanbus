//go:build windows

// lanbus-gui is the Windows Wails/WebView2 desktop shell with a tray icon.
// It serves the same dashboard HTML and proxies API calls to the running hub.
package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/getlantern/systray"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/CozeraVan/lanbus/go/internal/hub"
)

//go:embed icon.ico
var icon []byte

var _ embed.FS

type proxyHandler struct {
	proxy *httputil.ReverseProxy
	html  []byte
	token string
}

func (h *proxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" || r.URL.Path == "/ui" || r.URL.Path == "/ui/" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		page := string(h.html)
		if h.token != "" {
			inject := "<script>localStorage.setItem('lanbus_token'," + fmt.Sprintf("%q", h.token) + ");</script>"
			page = strings.Replace(page, "</head>", inject+"</head>", 1)
		}
		_, _ = w.Write([]byte(page))
		return
	}
	h.proxy.ServeHTTP(w, r)
}

type app struct {
	ctx      context.Context
	hubURL   string
	quitting bool
}

func (a *app) openBrowser() {
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", a.hubURL+"/ui").Start()
}

func (a *app) show() {
	if a.ctx != nil {
		runtime.WindowShow(a.ctx)
	}
}

func (a *app) quit() {
	a.quitting = true
	systray.Quit()
	if a.ctx != nil {
		runtime.Quit(a.ctx)
	}
}

func main() {
	var hubURL, token string
	var width, height int
	flag.StringVar(&hubURL, "hub", "http://127.0.0.1:8788", "running hub URL")
	flag.StringVar(&token, "token", "", "agent token (default ~/.lanbus/token.txt)")
	flag.IntVar(&width, "width", 1280, "window width")
	flag.IntVar(&height, "height", 860, "window height")
	flag.Parse()
	if _, err := url.Parse(hubURL); err != nil {
		fmt.Fprintln(os.Stderr, "lanbus-gui:", err)
		os.Exit(2)
	}
	if token == "" {
		home, _ := os.UserHomeDir()
		for _, name := range []string{"operator-token.txt", "token.txt"} {
			if b, err := os.ReadFile(filepath.Join(home, ".lanbus", name)); err == nil {
				token = strings.TrimSpace(string(b))
				break
			}
		}
	}
	a := &app{hubURL: hubURL}
	target, _ := url.Parse(hubURL)
	proxy := httputil.NewSingleHostReverseProxy(target)

	// Tray icon + menu. Wails owns the main window; systray owns the tray loop.
	go systray.Run(func() {
		systray.SetIcon(icon)
		systray.SetTitle("lanbus")
		systray.SetTooltip("lanbus local agent bus")
		open := systray.AddMenuItem("打开面板", "显示 lanbus 窗口")
		browser := systray.AddMenuItem("浏览器打开", "在默认浏览器打开 /ui")
		systray.AddSeparator()
		quit := systray.AddMenuItem("退出", "关闭 lanbus")
		go func() {
			for {
				select {
				case <-open.ClickedCh:
					a.show()
				case <-browser.ClickedCh:
					a.openBrowser()
				case <-quit.ClickedCh:
					a.quit()
					return
				}
			}
		}()
	}, func() {})

	// Wait briefly for the hub to come up; the UI itself handles errors.
	time.Sleep(100 * time.Millisecond)

	handler := &proxyHandler{proxy: proxy, html: []byte(hub.UIHTML()), token: token}
	err := wails.Run(&options.App{
		Title:       "lanbus · local agent bus",
		Width:       width,
		Height:      height,
		MinWidth:    900,
		MinHeight:   600,
		StartHidden: false,
		AssetServer: &assetserver.Options{Handler: handler},
		OnStartup: func(ctx context.Context) {
			a.ctx = ctx
			runtime.WindowShow(ctx)
		},
		OnBeforeClose: func(ctx context.Context) bool {
			if a.quitting {
				return false
			}
			runtime.WindowHide(ctx)
			return true
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "lanbus-gui:", err)
		os.Exit(1)
	}
}
