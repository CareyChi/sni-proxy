package service

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
)

type OpenRCManager struct{ baseManager }

func (manager *OpenRCManager) Name() string { return "openrc" }

func (manager *OpenRCManager) service(ctx context.Context, action string) error {
	result, err := manager.runner.Run(ctx, "rc-service", manager.serviceName, action)
	return commandSucceeded(result, err, action+" OpenRC service")
}

func (manager *OpenRCManager) Start(ctx context.Context) error   { return manager.service(ctx, "start") }
func (manager *OpenRCManager) Stop(ctx context.Context) error    { return manager.service(ctx, "stop") }
func (manager *OpenRCManager) Restart(ctx context.Context) error { return manager.service(ctx, "restart") }

func (manager *OpenRCManager) IsRunning(ctx context.Context) (bool, error) {
	result, err := manager.runner.Run(ctx, "rc-service", manager.serviceName, "status")
	if err != nil {
		return false, err
	}
	if result.ExitCode == 0 {
		return true, nil
	}
	if result.ExitCode == 3 || result.ExitCode == 1 {
		return false, nil
	}
	return false, commandSucceeded(result, nil, "query OpenRC service state")
}

func (manager *OpenRCManager) Enable(ctx context.Context) error {
	result, err := manager.runner.Run(ctx, "rc-update", "add", manager.serviceName, "default")
	return commandSucceeded(result, err, "enable OpenRC service")
}

func (manager *OpenRCManager) Disable(ctx context.Context) error {
	result, err := manager.runner.Run(ctx, "rc-update", "del", manager.serviceName, "default")
	return commandSucceeded(result, err, "disable OpenRC service")
}

func (manager *OpenRCManager) IsEnabled(ctx context.Context) (bool, error) {
	result, err := manager.runner.Run(ctx, "rc-update", "show", "default")
	if err != nil {
		return false, err
	}
	if result.ExitCode != 0 {
		return false, commandSucceeded(result, nil, "query OpenRC autostart state")
	}
	for _, line := range bytes.Split(result.Output, []byte{'\n'}) {
		fields := strings.Fields(string(line))
		if len(fields) > 0 && fields[0] == manager.serviceName {
			return true, nil
		}
	}
	return false, nil
}

func (manager *OpenRCManager) Install(context.Context) error {
	source := filepath.Join(manager.paths.InstallDir, "service", "openrc", "sni-proxy")
	target := manager.rooted(filepath.Join("/etc/init.d", manager.serviceName))
	return manager.installLink(source, target, 0o755)
}

func (manager *OpenRCManager) Uninstall(context.Context) error {
	return removeRegistration(manager.rooted(filepath.Join("/etc/init.d", manager.serviceName)))
}
