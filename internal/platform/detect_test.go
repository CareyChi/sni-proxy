package platform

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CareyChi/sni-proxy/internal/config"
)

func TestDetectAlpineOpenRC(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "os-release"), []byte("ID=alpine\nNAME=\"Alpine Linux\"\nVERSION_ID=3.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commands := map[string]bool{"rc-service": true, "rc-update": true, "apk": true}
	detector := Detector{
		Root: root, Machine: "x86_64", Kernel: "6.12", PID1: "init",
		LookPath: func(name string) (string, error) {
			if commands[name] {
				return "/sbin/" + name, nil
			}
			return "", errors.New("missing")
		},
	}
	info, err := detector.Detect(config.DefaultPaths())
	if err != nil {
		t.Fatal(err)
	}
	if info.DistributionID != "alpine" || info.InitSystem != InitOpenRC || info.PackageManager != "apk" {
		t.Fatalf("unexpected info: %#v", info)
	}
}

func TestCompatibilityMatrixIDs(t *testing.T) {
	for _, id := range []string{"debian", "ubuntu", "rocky", "almalinux", "fedora", "opensuse-leap", "arch", "alpine", "gentoo", "void"} {
		if compatibilityFor(id) != "verified" {
			t.Fatalf("expected %s to be verified", id)
		}
	}
	if compatibilityFor("example-linux") != "generic" {
		t.Fatal("unknown distribution must use generic compatibility")
	}
}

func TestDistributionFixtures(t *testing.T) {
	tests := []struct {
		id, name, version string
	}{
		{"debian", "Debian GNU/Linux", "13"},
		{"ubuntu", "Ubuntu", "26.04"},
		{"rocky", "Rocky Linux", "10"},
		{"arch", "Arch Linux", "rolling"},
		{"opensuse-leap", "openSUSE Leap", "16.0"},
	}
	for _, test := range tests {
		t.Run(test.id, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
				t.Fatal(err)
			}
			content := "ID=" + test.id + "\nNAME=\"" + test.name + "\"\nVERSION_ID=\"" + test.version + "\"\n"
			if err := os.WriteFile(filepath.Join(root, "etc", "os-release"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			detector := Detector{Root: root, Machine: "amd64", Kernel: "6.12", LookPath: missingCommand}
			info, err := detector.Detect(config.DefaultPaths())
			if err != nil {
				t.Fatal(err)
			}
			if info.DistributionID != test.id || info.Distribution != test.name || info.DistributionVersion != test.version {
				t.Fatalf("unexpected info: %#v", info)
			}
		})
	}
}

func TestInitDetectionByCapabilities(t *testing.T) {
	tests := []struct {
		name     string
		dirs     []string
		commands []string
		want     InitSystem
	}{
		{"systemd", []string{"run/systemd/system"}, []string{"systemctl"}, InitSystemd},
		{"openrc", nil, []string{"rc-service", "rc-update"}, InitOpenRC},
		{"runit", []string{"etc/sv"}, []string{"sv", "runsvdir"}, InitRunit},
		{"sysv", []string{"etc/init.d"}, []string{"service"}, InitSysV},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			for _, directory := range test.dirs {
				if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			available := make(map[string]bool)
			for _, command := range test.commands {
				available[command] = true
			}
			detector := Detector{Root: root, PID1: "fixture", LookPath: func(name string) (string, error) {
				if available[name] {
					return "/bin/" + name, nil
				}
				return "", errors.New("missing")
			}}
			if got := detector.detectInit(); got != test.want {
				t.Fatalf("detectInit = %s, want %s", got, test.want)
			}
		})
	}
}

func TestPackageManagerCapabilities(t *testing.T) {
	tests := map[string]string{"apt-get": "apt", "dnf": "dnf", "yum": "yum", "apk": "apk", "zypper": "zypper", "pacman": "pacman", "emerge": "emerge", "xbps-install": "xbps"}
	for command, want := range tests {
		detector := Detector{LookPath: func(name string) (string, error) {
			if name == command {
				return "/bin/" + name, nil
			}
			return "", errors.New("missing")
		}}
		if got := detector.detectPackageManager("unknown", ""); got != want {
			t.Fatalf("command %s detected as %s, want %s", command, got, want)
		}
	}
}

func missingCommand(string) (string, error) { return "", errors.New("missing") }
