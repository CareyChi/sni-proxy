//go:build linux

package credentials

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestRootInitializationPreservesDataDirectoryOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root is required to exercise cross-owner initialization")
	}
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dataDir, 0o750); err != nil {
		t.Fatal(err)
	}
	const serviceUID, serviceGID = 65534, 65534
	if err := os.Chown(dataDir, serviceUID, serviceGID); err != nil {
		t.Fatal(err)
	}
	store := NewStore(dataDir)
	if _, err := store.Initialize("admin", "correct-horse-battery-staple"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != serviceUID || stat.Gid != serviceGID {
		t.Fatalf("credential owner = %v, want %d:%d", info.Sys(), serviceUID, serviceGID)
	}
}
