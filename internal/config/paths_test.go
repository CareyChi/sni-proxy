package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallDirFromExecutableSymlink(t *testing.T) {
	root := t.TempDir()
	installDir := filepath.Join(root, "opt", "sni-proxy")
	binDir := filepath.Join(installDir, "bin")
	entryDir := filepath.Join(root, "usr", "local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(entryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(binDir, "sni-proxy")
	if err := os.WriteFile(binary, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(entryDir, "sni-proxy")
	relative, err := filepath.Rel(entryDir, binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(relative, link); err != nil {
		t.Fatal(err)
	}
	got, ok := installDirFromExecutable(link)
	if !ok || got != installDir {
		t.Fatalf("installDirFromExecutable = %q, %v; want %q, true", got, ok, installDir)
	}
}

func TestPathsRejectDangerousRoots(t *testing.T) {
	paths := DefaultPaths()
	paths.InstallDir = "/"
	if paths.Validate() == nil {
		t.Fatal("root install directory must be rejected")
	}
}
