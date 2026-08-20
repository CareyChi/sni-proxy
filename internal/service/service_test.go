package service

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/CareyChi/sni-proxy/internal/config"
	"github.com/CareyChi/sni-proxy/internal/platform"
)

type recordedCommand struct {
	name string
	args []string
}

type fakeRunner struct {
	commands []recordedCommand
	result   Result
}

func (runner *fakeRunner) Run(_ context.Context, name string, args ...string) (Result, error) {
	runner.commands = append(runner.commands, recordedCommand{name: name, args: append([]string(nil), args...)})
	return runner.result, nil
}

func TestServiceAdapterCommands(t *testing.T) {
	tests := []struct {
		init platform.InitSystem
		want recordedCommand
	}{
		{platform.InitSystemd, recordedCommand{"systemctl", []string{"restart", "sni-proxy"}}},
		{platform.InitOpenRC, recordedCommand{"rc-service", []string{"sni-proxy", "restart"}}},
		{platform.InitSysV, recordedCommand{"service", []string{"sni-proxy", "restart"}}},
		{platform.InitRunit, recordedCommand{"sv", []string{"restart", "/var/service/sni-proxy"}}},
	}
	for _, test := range tests {
		runner := &fakeRunner{}
		manager, err := New(Options{
			Platform: platform.Info{InitSystem: test.init}, Paths: config.DefaultPaths(), Runner: runner,
			LookPath: func(string) (string, error) { return "", errors.New("missing") }, RunitDir: "/var/service",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := manager.Restart(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(runner.commands) != 1 || !reflect.DeepEqual(runner.commands[0], test.want) {
			t.Fatalf("%s command = %#v, want %#v", test.init, runner.commands, test.want)
		}
	}
}

func TestUnknownInitIsRejected(t *testing.T) {
	_, err := New(Options{Platform: platform.Info{InitSystem: platform.InitUnknown}, Paths: config.DefaultPaths()})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}
