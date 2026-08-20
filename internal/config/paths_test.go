package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInstallDirFromExecutableSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires an elevated Windows token")
	}
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

func TestPathsRejectNonCanonicalInput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed paths are Linux paths")
	}
	paths := DefaultPaths()
	paths.InstallDir = "/opt/example/../../etc"
	if paths.Validate() == nil {
		t.Fatal("path containing dot-dot components was accepted")
	}
	paths = DefaultPaths()
	paths.ConfigDir = "/opt/sni-proxy/config"
	if paths.Validate() == nil {
		t.Fatal("config directory outside /etc was accepted")
	}
}

func TestCanonicalManagedDirRejectsSymlinkedParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed paths are Linux paths")
	}
	root := t.TempDir()
	allowed := filepath.Join(root, "allowed")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(allowed, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalManagedDir(filepath.Join(allowed, "link", "child"), []string{allowed}); err == nil {
		t.Fatal("managed path containing a symlinked parent was accepted")
	}
}
