package service

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/CareyChi/sni-proxy/internal/config"
	"github.com/CareyChi/sni-proxy/internal/platform"
)

var ErrUnsupported = errors.New("service operation is unsupported on the detected init system")

type Manager interface {
	Name() string
	Start(context.Context) error
	Stop(context.Context) error
	Restart(context.Context) error
	IsRunning(context.Context) (bool, error)
	Enable(context.Context) error
	Disable(context.Context) error
	IsEnabled(context.Context) (bool, error)
	Install(context.Context) error
	Uninstall(context.Context) error
}

type Result struct {
	Output   []byte
	ExitCode int
}

type Runner interface {
	Run(context.Context, string, ...string) (Result, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, arguments ...string) (Result, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	output, err := command.CombinedOutput()
	if err == nil {
		return Result{Output: output, ExitCode: 0}, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return Result{Output: output, ExitCode: exitError.ExitCode()}, nil
	}
	return Result{Output: output, ExitCode: -1}, err
}

type Options struct {
	Platform    platform.Info
	Paths       config.Paths
	ServiceName string
	Runner      Runner
	LookPath    func(string) (string, error)
	Root        string
	RunitDir    string
}

func New(options Options) (Manager, error) {
	if options.ServiceName == "" {
		options.ServiceName = "sni-proxy"
	}
	if options.Runner == nil {
		options.Runner = ExecRunner{}
	}
	if options.LookPath == nil {
		options.LookPath = exec.LookPath
	}
	if options.Root == "" {
		options.Root = "/"
	}
	base := baseManager{
		serviceName: options.ServiceName,
		paths:       options.Paths,
		runner:      options.Runner,
		lookPath:    options.LookPath,
		root:        options.Root,
	}
	switch options.Platform.InitSystem {
	case platform.InitSystemd:
		return &SystemdManager{baseManager: base}, nil
	case platform.InitOpenRC:
		return &OpenRCManager{baseManager: base}, nil
	case platform.InitSysV:
		return &SysVManager{baseManager: base}, nil
	case platform.InitRunit:
		return &RunitManager{baseManager: base, serviceDir: options.RunitDir}, nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, options.Platform.InitSystem)
	}
}

func commandSucceeded(result Result, err error, action string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("%s failed with exit code %d: %s", action, result.ExitCode, sanitizeOutput(result.Output))
	}
	return nil
}

func sanitizeOutput(output []byte) string {
	const maximum = 512
	if len(output) > maximum {
		output = output[:maximum]
	}
	return string(output)
}
