package service

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/CareyChi/sni-proxy/internal/config"
)

type baseManager struct {
	serviceName string
	paths       config.Paths
	runner      Runner
	lookPath    func(string) (string, error)
	root        string
}

func (manager baseManager) rooted(path string) string {
	if manager.root == "/" {
		return path
	}
	return filepath.Join(manager.root, strings.TrimLeft(filepath.Clean(path), "/\\"))
}

func (manager baseManager) installLink(source, target string, mode os.FileMode) error {
	if err := os.Chmod(source, mode); err != nil {
		return fmt.Errorf("prepare service source: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if current, err := os.Readlink(target); err == nil && current == source {
		return nil
	}
	if _, err := os.Lstat(target); err == nil {
		if err := os.Remove(target); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Symlink(source, target); err == nil {
		return nil
	}
	return copyRegistration(source, target, mode)
}

func copyRegistration(source, target string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		output.Close()
		if !ok {
			os.Remove(target)
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	ok = true
	return nil
}

func removeRegistration(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
