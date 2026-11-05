package hub

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func (h *Hub) download(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", 405)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/download/")
	name = filepath.Base(name)
	allowed := strings.HasPrefix(name, "lanbus-worker-") || strings.HasPrefix(name, "lanbus-scan-") ||
		name == "lanbus-mac-bootstrap.sh"
	if !allowed {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(h.cfg.DownloadDir, name)
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename="+name)
	http.ServeFile(w, r, path)
}
