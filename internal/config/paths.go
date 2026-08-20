package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultInstallDir = "/opt/sni-proxy"
	DefaultConfigDir  = "/etc/sni-proxy"
	DefaultDataDir    = "/var/lib/sni-proxy"
	DefaultLogDir     = "/var/log/sni-proxy"
	ManagedMarkerName = ".sni-proxy-managed"
)

type ManagedMarker struct {
	InstallationID string `json:"installation_id"`
	Role           string `json:"role"`
}

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
func (p Paths) UpdateTrustFile() string { return filepath.Join(p.ConfigDir, "update-trust.json") }

func (p Paths) ManagedDirs() map[string]string {
	return map[string]string{"install": p.InstallDir, "config": p.ConfigDir, "data": p.DataDir, "log": p.LogDir}
}

func ManagedMarkerFile(directory string) string { return filepath.Join(directory, ManagedMarkerName) }

func (p Paths) Validate() error {
	allowed := map[string][]string{
		"install": {"/opt", "/srv", "/usr/local/lib"},
		"config":  {"/etc"},
		"data":    {"/var/lib"},
		"log":     {"/var/log"},
	}
	canonical := make(map[string]string, 4)
	for role, value := range p.ManagedDirs() {
		resolved, err := canonicalManagedDir(value, allowed[role])
		if err != nil {
			return fmt.Errorf("%s_dir: %w", role, err)
		}
		canonical[role] = resolved
	}
	roles := []string{"install", "config", "data", "log"}
	for left := 0; left < len(roles); left++ {
		for right := left + 1; right < len(roles); right++ {
			a, b := canonical[roles[left]], canonical[roles[right]]
			if sameOrContains(a, b) || sameOrContains(b, a) {
				return fmt.Errorf("%s_dir and %s_dir must not overlap", roles[left], roles[right])
			}
		}
	}
	return nil
}

func canonicalManagedDir(value string, allowedRoots []string) (string, error) {
	if value == "" || !filepath.IsAbs(value) {
		return "", errors.New("must be an absolute path")
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '/' && character != '.' && character != '_' && character != '-' {
			return "", errors.New("contains a character that is unsafe in service templates")
		}
	}
	cleaned := filepath.Clean(value)
	if cleaned != value || cleaned == string(filepath.Separator) || hasDotComponent(value) {
		return "", errors.New("must be a canonical path without dot components")
	}
	resolved, err := evalExistingParents(cleaned)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	if resolved != cleaned {
		return "", errors.New("must use the resolved canonical path rather than a symlink")
	}
	allowed := false
	for _, root := range allowedRoots {
		if resolved != root && sameOrContains(root, resolved) {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", errors.New("is outside the allowed managed directory space")
	}
	return resolved, nil
}

func hasDotComponent(value string) bool {
	for _, component := range strings.FieldsFunc(value, func(r rune) bool { return r == '/' || r == '\\' }) {
		if component == "." || component == ".." {
			return true
		}
	}
	return false
}

func evalExistingParents(value string) (string, error) {
	missing := make([]string, 0, 4)
	candidate := value
	for {
		_, err := os.Lstat(candidate)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", err
		}
		missing = append(missing, filepath.Base(candidate))
		candidate = parent
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	for index := len(missing) - 1; index >= 0; index-- {
		resolved = filepath.Join(resolved, missing[index])
	}
	return filepath.Clean(resolved), nil
}

func sameOrContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
}

func VerifyManagedMarkers(paths Paths, installationID string) error {
	if installationID == "" {
		return errors.New("installation metadata has no installation_id")
	}
	if err := paths.Validate(); err != nil {
		return err
	}
	for role, directory := range paths.ManagedDirs() {
		file, err := os.Open(ManagedMarkerFile(directory))
		if err != nil {
			return fmt.Errorf("%s marker: %w", role, err)
		}
		var marker ManagedMarker
		decodeErr := json.NewDecoder(io.LimitReader(file, 4096)).Decode(&marker)
		closeErr := file.Close()
		if decodeErr != nil {
			return fmt.Errorf("%s marker: %w", role, decodeErr)
		}
		if closeErr != nil {
			return closeErr
		}
		if marker.InstallationID != installationID || marker.Role != role {
			return fmt.Errorf("%s marker does not match installation metadata", role)
		}
		info, err := os.Lstat(ManagedMarkerFile(directory))
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 || !ownedByRoot(info) {
			return fmt.Errorf("%s marker ownership or mode is unsafe", role)
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
