package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CareyChi/sni-proxy/internal/config"
)

func TestVerifySignedManifest(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	archivePath := filepath.Join(root, "sni-proxy-linux-amd64.tar.gz")
	archiveContent := []byte("signed archive fixture")
	if err := os.WriteFile(archivePath, archiveContent, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(archiveContent)
	manifest := []byte(`{"version":"v1.2.3","architecture":"amd64","archive":"sni-proxy-linux-amd64.tar.gz","sha256":"` + hex.EncodeToString(digest[:]) + `"}`)
	manifestPath := filepath.Join(root, "manifest.json")
	signaturePath := filepath.Join(root, "manifest.json.sig")
	if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	signature := base64.RawStdEncoding.EncodeToString(ed25519.Sign(privateKey, manifest))
	if err := os.WriteFile(signaturePath, []byte(signature), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifySignedManifest(manifestPath, signaturePath, archivePath, filepath.Base(archivePath), "v1.2.3", publicKey); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(signaturePath, []byte(base64.RawStdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifySignedManifest(manifestPath, signaturePath, archivePath, filepath.Base(archivePath), "v1.2.3", publicKey); err == nil {
		t.Fatal("invalid manifest signature was accepted")
	}
	if err := os.WriteFile(signaturePath, []byte(signature), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifySignedManifest(manifestPath, signaturePath, archivePath, filepath.Base(archivePath), "v1.2.3", publicKey); err == nil {
		t.Fatal("tampered archive passed signed-manifest verification")
	}
}

func TestExtractArchiveRejectsTraversalAndLinks(t *testing.T) {
	for _, test := range []struct {
		name     string
		typeflag byte
	}{
		{"../escape", tar.TypeReg},
		{"/absolute", tar.TypeReg},
		{"..\\escape", tar.TypeReg},
		{"link", tar.TypeSymlink},
		{"hardlink", tar.TypeLink},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			archivePath := filepath.Join(root, "fixture.tar.gz")
			file, err := os.Create(archivePath)
			if err != nil {
				t.Fatal(err)
			}
			gzipWriter := gzip.NewWriter(file)
			tarWriter := tar.NewWriter(gzipWriter)
			header := &tar.Header{Name: test.name, Typeflag: test.typeflag, Mode: 0o644}
			if test.typeflag == tar.TypeReg {
				header.Size = 1
			}
			if err := tarWriter.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if test.typeflag == tar.TypeReg {
				_, _ = tarWriter.Write([]byte("x"))
			}
			_ = tarWriter.Close()
			_ = gzipWriter.Close()
			_ = file.Close()
			if err := extractArchive(archivePath, filepath.Join(root, "out")); err == nil {
				t.Fatal("unsafe archive was accepted")
			}
		})
	}
}

func TestExtractArchiveRejectsOversizedFile(t *testing.T) {
	root := t.TempDir()
	archivePath := filepath.Join(root, "oversized.tar.gz")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "large", Typeflag: tar.TypeReg, Mode: 0o644, Size: maximumArchiveSize + 1}); err != nil {
		t.Fatal(err)
	}
	_ = tarWriter.Close() // The deliberately absent body leaves a valid oversized header in a truncated archive.
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractArchive(archivePath, filepath.Join(root, "out")); err == nil {
		t.Fatal("oversized archive member was accepted")
	}
}

func TestExtractArchiveAcceptsTarRootDirectoryEntry(t *testing.T) {
	root := t.TempDir()
	archivePath := filepath.Join(root, "fixture.tar.gz")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, header := range []*tar.Header{
		{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "./VERSION", Typeflag: tar.TypeReg, Mode: 0o644, Size: 2},
	} {
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := tarWriter.Write([]byte("v1")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "out")
	if err := extractArchive(archivePath, destination); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(destination, "VERSION"))
	if err != nil || string(content) != "v1" {
		t.Fatalf("extracted VERSION = %q, %v", content, err)
	}
}

func TestRestorePreviousVersion(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "active")
	rollback := filepath.Join(root, "rollback")
	staging := filepath.Join(root, "staging")
	for _, directory := range []string{active, rollback, staging} {
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(active, "VERSION"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rollback, "VERSION"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := &fakeManager{}
	paths := config.Paths{InstallDir: active}
	health := func(_ context.Context, paths config.Paths) error {
		content, err := os.ReadFile(filepath.Join(paths.InstallDir, "VERSION"))
		if err != nil {
			return err
		}
		if string(content) != "old" {
			return errors.New("old version not restored")
		}
		return nil
	}
	if err := restorePrevious(context.Background(), manager, paths, rollback, staging, health); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(active, "VERSION"))
	if err != nil || string(content) != "old" {
		t.Fatalf("active version = %q, %v", content, err)
	}
	if manager.stop != 1 || manager.install != 1 || manager.restart != 1 {
		t.Fatalf("unexpected manager calls: %#v", manager)
	}
}

type fakeManager struct {
	stop, install, restart int
}

func (*fakeManager) Name() string                            { return "fake" }
func (*fakeManager) Start(context.Context) error             { return nil }
func (manager *fakeManager) Stop(context.Context) error      { manager.stop++; return nil }
func (manager *fakeManager) Restart(context.Context) error   { manager.restart++; return nil }
func (*fakeManager) IsRunning(context.Context) (bool, error) { return true, nil }
func (*fakeManager) Enable(context.Context) error            { return nil }
func (*fakeManager) Disable(context.Context) error           { return nil }
func (*fakeManager) IsEnabled(context.Context) (bool, error) { return true, nil }
func (manager *fakeManager) Install(context.Context) error   { manager.install++; return nil }
func (*fakeManager) Uninstall(context.Context) error         { return nil }
