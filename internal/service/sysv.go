package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

type SysVManager struct{ baseManager }

func (manager *SysVManager) Name() string { return "sysvinit" }

func (manager *SysVManager) service(ctx context.Context, action string) (Result, error) {
	return manager.runner.Run(ctx, "service", manager.serviceName, action)
}

func (manager *SysVManager) Start(ctx context.Context) error {
	result, err := manager.service(ctx, "start")
	return commandSucceeded(result, err, "start SysV service")
}

func (manager *SysVManager) Stop(ctx context.Context) error {
	result, err := manager.service(ctx, "stop")
	return commandSucceeded(result, err, "stop SysV service")
}

func (manager *SysVManager) Restart(ctx context.Context) error {
	result, err := manager.service(ctx, "restart")
	return commandSucceeded(result, err, "restart SysV service")
}

func (manager *SysVManager) IsRunning(ctx context.Context) (bool, error) {
	result, err := manager.service(ctx, "status")
	if err != nil {
		return false, err
	}
	if result.ExitCode == 0 {
		return true, nil
	}
	if result.ExitCode == 1 || result.ExitCode == 3 {
		return false, nil
	}
	return false, commandSucceeded(result, nil, "query SysV service state")
}

func (manager *SysVManager) Enable(ctx context.Context) error {
	if _, err := manager.lookPath("update-rc.d"); err == nil {
		result, runErr := manager.runner.Run(ctx, "update-rc.d", manager.serviceName, "defaults")
		return commandSucceeded(result, runErr, "enable SysV service with update-rc.d")
	}
	if _, err := manager.lookPath("chkconfig"); err == nil {
		result, runErr := manager.runner.Run(ctx, "chkconfig", manager.serviceName, "on")
		return commandSucceeded(result, runErr, "enable SysV service with chkconfig")
	}
	for _, level := range []string{"2", "3", "4", "5"} {
		directory := manager.rooted("/etc/rc" + level + ".d")
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return err
		}
		target := filepath.Join(directory, "S90"+manager.serviceName)
		source := filepath.Join("../init.d", manager.serviceName)
		if err := os.Symlink(source, target); err != nil && !os.IsExist(err) {
			return err
		}
	}
	return nil
}

func (manager *SysVManager) Disable(ctx context.Context) error {
	if _, err := manager.lookPath("update-rc.d"); err == nil {
		result, runErr := manager.runner.Run(ctx, "update-rc.d", "-f", manager.serviceName, "remove")
		return commandSucceeded(result, runErr, "disable SysV service with update-rc.d")
	}
	if _, err := manager.lookPath("chkconfig"); err == nil {
		result, runErr := manager.runner.Run(ctx, "chkconfig", manager.serviceName, "off")
		return commandSucceeded(result, runErr, "disable SysV service with chkconfig")
	}
	matches, err := filepath.Glob(manager.rooted("/etc/rc[0-6].d/S??" + manager.serviceName))
	if err != nil {
		return err
	}
	for _, match := range matches {
		if err := os.Remove(match); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (manager *SysVManager) IsEnabled(context.Context) (bool, error) {
	matches, err := filepath.Glob(manager.rooted("/etc/rc[2-5].d/S??" + manager.serviceName))
	return len(matches) > 0, err
}

func (manager *SysVManager) Install(context.Context) error {
	source := filepath.Join(manager.paths.InstallDir, "service", "sysvinit", "sni-proxy")
	target := manager.rooted(filepath.Join("/etc/init.d", manager.serviceName))
	if err := manager.installLink(source, target, 0o755); err != nil {
		return fmt.Errorf("install SysV service: %w", err)
	}
	return nil
}

func (manager *SysVManager) Uninstall(context.Context) error {
	return removeRegistration(manager.rooted(filepath.Join("/etc/init.d", manager.serviceName)))
}
