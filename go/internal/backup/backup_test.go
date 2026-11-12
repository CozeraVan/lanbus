package backup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateRestoreRoundtrip(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "bus.db"), []byte("db-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "bus.db-wal"), []byte("wal"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "operator-token.txt"), []byte("optok"), 0o600); err != nil {
		t.Fatal(err)
	}
	// unrelated file must NOT be backed up
	if err := os.WriteFile(filepath.Join(src, "secret-other.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	archive, err := Create(src, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("archive missing: %v", err)
	}

	dst := t.TempDir()
	if err := Restore(archive, dst); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"bus.db": "db-content", "bus.db-wal": "wal", "operator-token.txt": "optok"} {
		got, err := os.ReadFile(filepath.Join(dst, name))
		if err != nil {
			t.Fatalf("restore %s: %v", name, err)
		}
		if string(got) != want {
			t.Fatalf("restore %s = %q, want %q", name, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "secret-other.txt")); !os.IsNotExist(err) {
		t.Fatal("unrelated file should not be restored")
	}
}

func TestCreateEmptyDirFails(t *testing.T) {
	if _, err := Create(t.TempDir(), ""); err == nil {
		t.Fatal("expected error for empty data dir")
	}
}

func TestList(t *testing.T) {
	dir := t.TempDir()
	if _, err := Create(dir, filepath.Join(dir, "backups", "a.zip")); err != nil {
		// no state -> error; seed a db first
		_ = os.WriteFile(filepath.Join(dir, "bus.db"), []byte("x"), 0o600)
		if _, err := Create(dir, ""); err != nil {
			t.Fatal(err)
		}
	}
	if len(List(dir)) == 0 {
		t.Fatal("expected at least one backup listed")
	}
}