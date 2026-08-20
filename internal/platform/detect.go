package platform

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/CareyChi/sni-proxy/internal/config"
)

type Detector struct {
	Root     string
	LookPath func(string) (string, error)
	Machine  string
	Kernel   string
	PID1     string
}

func NewDetector() Detector {
	return Detector{Root: "/", LookPath: exec.LookPath}
}

func (detector Detector) Detect(paths config.Paths) (Info, error) {
	if detector.Root == "" {
		detector.Root = "/"
	}
	if detector.LookPath == nil {
		detector.LookPath = exec.LookPath
	}
	values, err := detector.detectDistribution()
	if err != nil {
		return Info{}, err
	}
	machine := detector.Machine
	if machine == "" {
		machine = runtime.GOARCH
		if output, commandErr := exec.Command("uname", "-m").Output(); commandErr == nil {
			machine = strings.TrimSpace(string(output))
		}
	}
	architecture, err := NormalizeArchitecture(machine)
	if err != nil {
		return Info{}, err
	}
	kernel := detector.Kernel
	if kernel == "" {
		kernel = "unknown"
		if output, commandErr := exec.Command("uname", "-r").Output(); commandErr == nil {
			kernel = strings.TrimSpace(string(output))
		}
	}
	initSystem := detector.detectInit()
	id := strings.ToLower(values["ID"])
	distribution := values["PRETTY_NAME"]
	if distribution == "" {
		distribution = values["NAME"]
	}
	if distribution == "" {
		distribution = "Unknown Linux"
	}
	info := Info{
		OS:                  "linux",
		Distribution:        distribution,
		DistributionID:      id,
		DistributionVersion: firstNonEmpty(values["VERSION_ID"], values["VERSION"], "unknown"),
		IDLike:              strings.Fields(strings.ToLower(values["ID_LIKE"])),
		Architecture:        architecture,
		Kernel:              kernel,
		InitSystem:          initSystem,
		ServiceManager:      string(initSystem),
		PackageManager:      detector.detectPackageManager(id, values["ID_LIKE"]),
		InstallDir:          paths.InstallDir,
		Compatibility:       compatibilityFor(id),
		Container:           detector.detectContainer(),
	}
	return info, nil
}

func NormalizeArchitecture(machine string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(machine))
	switch normalized {
	case "amd64", "x86_64", "x64":
		return "amd64", nil
	case "arm64", "aarch64", "armv7", "armv7l", "386", "i386", "i686":
		return "", fmt.Errorf("current version only supports Linux amd64/x86_64; detected architecture: %s", machine)
	default:
		return "", fmt.Errorf("unsupported architecture: %s", machine)
	}
}

func (detector Detector) detectDistribution() (map[string]string, error) {
	for _, candidate := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		file, err := os.Open(detector.rootPath(candidate))
		if err == nil {
			values, parseErr := ParseOSRelease(file)
			file.Close()
			if parseErr != nil {
				return nil, parseErr
			}
			return values, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if detector.Root == "/" && detector.hasCommand("lsb_release") {
		nameOutput, nameErr := exec.Command("lsb_release", "-si").Output()
		versionOutput, versionErr := exec.Command("lsb_release", "-sr").Output()
		if nameErr == nil && versionErr == nil {
			name := strings.TrimSpace(string(nameOutput))
			id := strings.ToLower(strings.ReplaceAll(name, " ", "-"))
			return map[string]string{"ID": id, "NAME": name, "VERSION_ID": strings.TrimSpace(string(versionOutput))}, nil
		}
	}
	values := make(map[string]string)
	for path, id := range map[string]string{
		"/etc/alpine-release": "alpine",
		"/etc/arch-release":   "arch",
		"/etc/debian_version": "debian",
		"/etc/redhat-release": "rhel",
		"/etc/SuSE-release":   "suse",
	} {
		content, err := os.ReadFile(detector.rootPath(path))
		if err == nil {
			values["ID"] = id
			values["NAME"] = fallbackName(id)
			values["VERSION_ID"] = strings.TrimSpace(string(content))
			return values, nil
		}
	}
	return map[string]string{"ID": "unknown", "NAME": "Unknown Linux", "VERSION_ID": "unknown"}, nil
}

func (detector Detector) detectInit() InitSystem {
	pid1 := detector.PID1
	if pid1 == "" {
		if content, err := os.ReadFile(detector.rootPath("/proc/1/comm")); err == nil {
			pid1 = strings.ToLower(strings.TrimSpace(string(content)))
		}
	}
	if detector.isDir("/run/systemd/system") && detector.hasCommand("systemctl") {
		return InitSystemd
	}
	if detector.hasCommand("rc-service") && detector.hasCommand("rc-update") {
		return InitOpenRC
	}
	if detector.hasCommand("sv") && (detector.hasCommand("runsvdir") || detector.isDir("/etc/sv") || detector.isDir("/var/service")) {
		return InitRunit
	}
	if (detector.hasCommand("service") || strings.Contains(pid1, "init")) && detector.isDir("/etc/init.d") {
		return InitSysV
	}
	if strings.Contains(pid1, "systemd") && detector.hasCommand("systemctl") {
		return InitSystemd
	}
	return InitUnknown
}

func (detector Detector) detectPackageManager(id, idLike string) string {
	order := []struct {
		command string
		name    string
	}{
		{"apt-get", "apt"}, {"dnf", "dnf"}, {"yum", "yum"}, {"apk", "apk"},
		{"zypper", "zypper"}, {"pacman", "pacman"}, {"emerge", "emerge"}, {"xbps-install", "xbps"},
	}
	family := strings.ToLower(id + " " + idLike)
	preferred := map[string]string{}
	switch {
	case strings.Contains(family, "alpine"):
		preferred["apk"] = "apk"
	case strings.Contains(family, "debian") || strings.Contains(family, "ubuntu"):
		preferred["apt-get"] = "apt"
	case strings.Contains(family, "rhel") || strings.Contains(family, "fedora") || strings.Contains(family, "centos"):
		preferred["dnf"] = "dnf"
	case strings.Contains(family, "suse"):
		preferred["zypper"] = "zypper"
	case strings.Contains(family, "arch"):
		preferred["pacman"] = "pacman"
	}
	for command, name := range preferred {
		if detector.hasCommand(command) {
			return name
		}
	}
	for _, candidate := range order {
		if detector.hasCommand(candidate.command) {
			return candidate.name
		}
	}
	return "unknown"
}

func (detector Detector) detectContainer() ContainerInfo {
	for path, kind := range map[string]string{"/.dockerenv": "docker", "/run/.containerenv": "podman"} {
		if detector.exists(path) {
			return ContainerInfo{Detected: true, Type: kind}
		}
	}
	content, _ := os.ReadFile(detector.rootPath("/proc/1/cgroup"))
	value := strings.ToLower(string(content))
	for _, kind := range []string{"docker", "podman", "lxc", "openvz", "containerd"} {
		if strings.Contains(value, kind) {
			return ContainerInfo{Detected: true, Type: kind}
		}
	}
	return ContainerInfo{}
}

func (detector Detector) rootPath(path string) string {
	if detector.Root == "/" {
		return path
	}
	return filepath.Join(detector.Root, strings.TrimLeft(filepath.Clean(path), "/\\"))
}

func (detector Detector) exists(path string) bool {
	_, err := os.Stat(detector.rootPath(path))
	return err == nil
}

func (detector Detector) isDir(path string) bool {
	info, err := os.Stat(detector.rootPath(path))
	return err == nil && info.IsDir()
}

func (detector Detector) hasCommand(command string) bool {
	_, err := detector.LookPath(command)
	return err == nil
}

func compatibilityFor(id string) string {
	switch strings.ToLower(id) {
	case "debian", "ubuntu", "linuxmint", "pop", "kali", "rhel", "rocky", "almalinux", "centos", "ol", "fedora", "opensuse-leap", "opensuse-tumbleweed", "sles", "arch", "manjaro", "endeavouros", "alpine", "gentoo", "devuan", "void":
		return "verified"
	default:
		return "generic"
	}
}

func fallbackName(id string) string {
	return map[string]string{"alpine": "Alpine Linux", "arch": "Arch Linux", "debian": "Debian", "rhel": "Red Hat Linux", "suse": "SUSE Linux"}[id]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
