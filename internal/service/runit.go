package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

type RunitManager struct {
	baseManager
	serviceDir string
}

func (manager *RunitManager) Name() string { return "runit" }

func (manager *RunitManager) target() string {
	if manager.serviceDir != "" {
		return filepath.Join(manager.serviceDir, manager.serviceName)
	}
	for _, directory := range []string{"/var/service", "/run/service", "/etc/service"} {
		if info, err := os.Stat(manager.rooted(directory)); err == nil && info.IsDir() {
			return filepath.Join(manager.rooted(directory), manager.serviceName)
		}
	}
	return filepath.Join(manager.rooted("/var/service"), manager.serviceName)
}

func (manager *RunitManager) run(ctx context.Context, operation, action string) error {
	result, err := manager.runner.Run(ctx, "sv", operation, manager.target())
	return commandSucceeded(result, err, action)
}

func (manager *RunitManager) Start(ctx context.Context) error {
	return manager.run(ctx, "up", "start runit service")
}
func (manager *RunitManager) Stop(ctx context.Context) error {
	return manager.run(ctx, "down", "stop runit service")
}
func (manager *RunitManager) Restart(ctx context.Context) error {
	return manager.run(ctx, "restart", "restart runit service")
}

func (manager *RunitManager) IsRunning(ctx context.Context) (bool, error) {
	result, err := manager.runner.Run(ctx, "sv", "status", manager.target())
	if err != nil {
		return false, err
	}
	if result.ExitCode == 0 {
		return true, nil
	}
	if result.ExitCode == 1 {
		return false, nil
	}
	return false, commandSucceeded(result, nil, "query runit service state")
}

func (manager *RunitManager) Enable(context.Context) error {
	source := filepath.Join(manager.paths.InstallDir, "service", "runit", "sni-proxy")
	target := manager.target()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if _, err := os.Lstat(target); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Symlink(source, target)
}

func (manager *RunitManager) Disable(context.Context) error { return removeRegistration(manager.target()) }

func (manager *RunitManager) IsEnabled(context.Context) (bool, error) {
	_, err := os.Lstat(manager.target())
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func (manager *RunitManager) Install(context.Context) error {
	runFile := filepath.Join(manager.paths.InstallDir, "service", "runit", "sni-proxy", "run")
	return os.Chmod(runFile, 0o755)
}

func (manager *RunitManager) Uninstall(context.Context) error { return removeRegistration(manager.target()) }
