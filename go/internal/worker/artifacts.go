package worker

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var defaultArtifacts = []string{
	".lanbus-shot.*", "screenshot*.png", "build.log",
	"artifacts/**/*.png", "playwright-report/**/*.png",
}

type artifact struct {
	Name   string `json:"name"`
	Mime   string `json:"mime"`
	Size   int    `json:"size"`
	Sha256 string `json:"sha256"`
	Data   string `json:"data"`
}

func artifactFromBytes(name string, data []byte) artifact {
	sum := sha256.Sum256(data)
	return artifact{
		Name: name, Mime: mime.TypeByExtension(filepath.Ext(name)),
		Size: len(data), Sha256: hex.EncodeToString(sum[:]),
		Data: base64.StdEncoding.EncodeToString(data),
	}
}

func (w *Worker) collectArtifacts(cwd string, t *Task, since time.Time) []artifact {
	if cwd == "" {
		return nil
	}
	patterns := []string{}
	if t.ArtifactGlobs != "" {
		var list []string
		if json.Unmarshal([]byte(t.ArtifactGlobs), &list) == nil {
			patterns = append(patterns, list...)
		}
	}
	patterns = append(patterns, w.artifactGlobs...)
	patterns = append(patterns, defaultArtifacts...)
	seen := map[string]bool{}
	out := []artifact{}
	var total int64
	filepath.Walk(cwd, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(cwd, path)
		rel = filepath.ToSlash(rel)
		if !matchAny(rel, patterns) {
			return nil
		}
		real, _ := filepath.EvalSymlinks(path)
		if real == "" {
			real = path
		}
		if seen[real] {
			return nil
		}
		seen[real] = true
		if info.ModTime().Before(since.Add(-5 * time.Second)) {
			return nil
		}
		if info.Size() > w.maxArtifact || total+info.Size() > w.maxArtifact*2 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		sum := sha256.Sum256(data)
		m := mime.TypeByExtension(filepath.Ext(path))
		if m == "" {
			m = detectMime(path)
		}
		name := rel
		if strings.HasPrefix(name, "../") {
			name = filepath.Base(path)
		}
		out = append(out, artifact{
			Name: name, Mime: m, Size: len(data),
			Sha256: hex.EncodeToString(sum[:]),
			Data:   base64.StdEncoding.EncodeToString(data),
		})
		total += int64(len(data))
		if len(out) >= 50 {
			return filepath.SkipAll
		}
		return nil
	})
	return out
}

func detectMime(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".log", ".txt", ".diff", ".patch":
		return "text/plain"
	case ".json":
		return "application/json"
	case ".md":
		return "text/markdown"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	}
	return "application/octet-stream"
}

func matchAny(rel string, patterns []string) bool {
	for _, p := range patterns {
		if p == "" {
			continue
		}
		if matchGlob(strings.TrimPrefix(filepath.ToSlash(p), "./"), rel) {
			return true
		}
	}
	return false
}

func matchGlob(pattern, name string) bool {
	p := strings.Split(strings.ReplaceAll(pattern, "\\", "/"), "/")
	n := strings.Split(strings.ReplaceAll(name, "\\", "/"), "/")
	return matchSegments(p, n)
}

func matchSegments(p, n []string) bool {
	if len(p) == 0 {
		return len(n) == 0
	}
	if p[0] == "**" {
		if matchSegments(p[1:], n) {
			return true
		}
		if len(n) > 0 {
			return matchSegments(p, n[1:])
		}
		return false
	}
	if len(n) == 0 {
		return false
	}
	ok, _ := filepath.Match(p[0], n[0])
	if !ok {
		return false
	}
	return matchSegments(p[1:], n[1:])
}
