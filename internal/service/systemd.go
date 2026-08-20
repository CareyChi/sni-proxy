package service

import (
	"context"
	"path/filepath"
)

type SystemdManager struct{ baseManager }

func (manager *SystemdManager) Name() string { return "systemd" }

func (manager *SystemdManager) run(ctx context.Context, action string, arguments ...string) error {
	result, err := manager.runner.Run(ctx, "systemctl", arguments...)
	return commandSucceeded(result, err, action)
}

func (manager *SystemdManager) Start(ctx context.Context) error {
	return manager.run(ctx, "start systemd service", "start", manager.serviceName)
}

func (manager *SystemdManager) Stop(ctx context.Context) error {
	return manager.run(ctx, "stop systemd service", "stop", manager.serviceName)
}

func (manager *SystemdManager) Restart(ctx context.Context) error {
	return manager.run(ctx, "restart systemd service", "restart", manager.serviceName)
}

func (manager *SystemdManager) IsRunning(ctx context.Context) (bool, error) {
	result, err := manager.runner.Run(ctx, "systemctl", "is-active", "--quiet", manager.serviceName)
	if err != nil {
		return false, err
	}
	if result.ExitCode == 0 {
		return true, nil
	}
	if result.ExitCode == 3 {
		return false, nil
	}
	return false, commandSucceeded(result, nil, "query systemd service state")
}

func (manager *SystemdManager) Enable(ctx context.Context) error {
	return manager.run(ctx, "enable systemd service", "enable", manager.serviceName)
}

func (manager *SystemdManager) Disable(ctx context.Context) error {
	return manager.run(ctx, "disable systemd service", "disable", manager.serviceName)
}

func (manager *SystemdManager) IsEnabled(ctx context.Context) (bool, error) {
	result, err := manager.runner.Run(ctx, "systemctl", "is-enabled", "--quiet", manager.serviceName)
	if err != nil {
		return false, err
	}
	if result.ExitCode == 0 {
		return true, nil
	}
	if result.ExitCode == 1 {
		return false, nil
	}
	return false, commandSucceeded(result, nil, "query systemd autostart state")
}

func (manager *SystemdManager) Install(ctx context.Context) error {
	source := filepath.Join(manager.paths.InstallDir, "service", "systemd", "sni-proxy.service")
	target := manager.rooted(filepath.Join("/etc/systemd/system", manager.serviceName+".service"))
	if err := manager.installLink(source, target, 0o644); err != nil {
		return err
	}
	return manager.run(ctx, "reload systemd configuration", "daemon-reload")
}

func (manager *SystemdManager) Uninstall(ctx context.Context) error {
	target := manager.rooted(filepath.Join("/etc/systemd/system", manager.serviceName+".service"))
	if err := removeRegistration(target); err != nil {
		return err
	}
	return manager.run(ctx, "reload systemd configuration", "daemon-reload")
}
