package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultInstallDir = "/opt/sni-proxy"
	DefaultConfigDir  = "/etc/sni-proxy"
	DefaultDataDir    = "/var/lib/sni-proxy"
	DefaultLogDir     = "/var/log/sni-proxy"
)

// Paths is the only source of path defaults used by the Go application.
type Paths struct {
	InstallDir string `json:"install_dir"`
	ConfigDir  string `json:"config_dir"`
	DataDir    string `json:"data_dir"`
	LogDir     string `json:"log_dir"`
}

func DefaultPaths() Paths {
	return Paths{
		InstallDir: DefaultInstallDir,
		ConfigDir:  DefaultConfigDir,
		DataDir:    DefaultDataDir,
		LogDir:     DefaultLogDir,
	}
}

func (p Paths) ConfigFile() string      { return filepath.Join(p.ConfigDir, "config.json") }
func (p Paths) CredentialsFile() string { return filepath.Join(p.DataDir, "credentials.json") }
func (p Paths) MetadataFile() string    { return filepath.Join(p.InstallDir, "install-meta.json") }
func (p Paths) VersionFile() string     { return filepath.Join(p.InstallDir, "VERSION") }
func (p Paths) BinaryFile() string      { return filepath.Join(p.InstallDir, "bin", "sni-proxy") }
func (p Paths) ControlFile() string     { return filepath.Join(p.InstallDir, "bin", "sni-proxyctl") }
func (p Paths) BackupDir() string       { return filepath.Join(p.DataDir, "backups") }

func (p Paths) Validate() error {
	for name, value := range map[string]string{
		"install_dir": p.InstallDir,
		"config_dir":  p.ConfigDir,
		"data_dir":    p.DataDir,
		"log_dir":     p.LogDir,
	} {
		if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) == string(filepath.Separator) {
			return errors.New(name + " must be a non-root absolute path")
		}
	}
	return nil
}

// DiscoverPaths resolves the running executable, including symlinks, before
// deriving the installation root. The environment override is intended for
// packaging and controlled test fixtures, not untrusted request data.
func DiscoverPaths() Paths {
	paths := DefaultPaths()
	if value := strings.TrimSpace(os.Getenv("SNI_PROXY_INSTALL_DIR")); value != "" && filepath.IsAbs(value) && filepath.Clean(value) != string(filepath.Separator) {
		paths.InstallDir = filepath.Clean(value)
		return mergeMetadataPaths(paths)
	}

	executable, err := os.Executable()
	if err == nil {
		if candidate, ok := installDirFromExecutable(executable); ok {
			paths.InstallDir = candidate
		}
	}
	return mergeMetadataPaths(paths)
}

func installDirFromExecutable(executable string) (string, bool) {
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	} else {
		return "", false
	}
	binDir := filepath.Dir(executable)
	if filepath.Base(binDir) != "bin" {
		return "", false
	}
	candidate := filepath.Clean(filepath.Dir(binDir))
	if candidate == string(filepath.Separator) || !filepath.IsAbs(candidate) {
		return "", false
	}
	return candidate, true
}

func mergeMetadataPaths(paths Paths) Paths {
	metadata, err := LoadInstallMetadata(paths.MetadataFile())
	if err != nil || metadata.InstallDir != paths.InstallDir {
		return paths
	}
	if metadata.ConfigDir != "" {
		paths.ConfigDir = metadata.ConfigDir
	}
	if metadata.DataDir != "" {
		paths.DataDir = metadata.DataDir
	}
	if metadata.LogDir != "" {
		paths.LogDir = metadata.LogDir
	}
	if paths.Validate() != nil {
		return Paths{InstallDir: paths.InstallDir, ConfigDir: DefaultConfigDir, DataDir: DefaultDataDir, LogDir: DefaultLogDir}
	}
	return paths
}
