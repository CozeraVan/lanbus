// Package backup archives and restores the lanbus data directory (SQLite task
// store in WAL mode plus tokens/config). It is used by `lanbus backup/restore`.
package backup

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// files are the data-dir entries worth backing up. The WAL sidecars are copied
// together with bus.db so a hot copy stays consistent.
var files = []string{"bus.db", "bus.db-wal", "bus.db-shm", "operator-token.txt", "token.txt", "local.json"}

// Create writes a zip of dataDir's state to outPath ("" -> timestamped name in
// dataDir/backups). It returns the archive path.
func Create(dataDir, outPath string) (string, error) {
	if outPath == "" {
		dir := filepath.Join(dataDir, "backups")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		outPath = filepath.Join(dir, "lanbus-"+time.Now().Format("20060102-150405")+".zip")
	}
	f, err := os.Create(outPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	added := 0
	for _, name := range files {
		src := filepath.Join(dataDir, name)
		info, err := os.Stat(src)
		if err != nil || info.IsDir() {
			continue
		}
		if err := addFile(zw, src, name); err != nil {
			zw.Close()
			return "", err
		}
		added++
	}
	if added == 0 {
		zw.Close()
		return "", fmt.Errorf("no backupable state found in %s", dataDir)
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	return outPath, nil
}

func addFile(zw *zip.Writer, src, name string) error {
	r, err := os.Open(src)
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, r)
	return err
}

// Restore extracts a backup archive into dataDir, overwriting existing state.
// The caller should stop the hub first. Only known entry names are extracted
// (zip-slip safe).
func Restore(archivePath, dataDir string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer zr.Close()
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, n := range files {
		allowed[n] = true
	}
	for _, f := range zr.File {
		name := filepath.Base(f.Name)
		if !allowed[name] || f.FileInfo().IsDir() {
			continue
		}
		if err := extract(f, filepath.Join(dataDir, name)); err != nil {
			return err
		}
	}
	return nil
}

func extract(f *zip.File, dst string) error {
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer w.Close()
	_, err = io.Copy(w, r)
	return err
}

// List returns the backup archives in dataDir/backups, newest first.
func List(dataDir string) []string {
	dir := filepath.Join(dataDir, "backups")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".zip") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}
