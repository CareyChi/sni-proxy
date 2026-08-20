package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"debug/elf"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/CareyChi/sni-proxy/internal/config"
	"github.com/CareyChi/sni-proxy/internal/privilege"
	"github.com/CareyChi/sni-proxy/internal/service"
)

const (
	maximumArchiveSize   = 256 << 20
	maximumExtractedSize = 512 << 20
	updateHealthTimeout  = 30 * time.Second
)

type Asset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
}

type Release struct {
	TagName    string  `json:"tag_name"`
	Prerelease bool    `json:"prerelease"`
	Draft      bool    `json:"draft"`
	Assets     []Asset `json:"assets"`
}

type CheckResult struct {
	Current   string  `json:"current"`
	Latest    string  `json:"latest"`
	Available bool    `json:"available"`
	Channel   string  `json:"channel"`
	Release   Release `json:"-"`
}

type Client struct {
	HTTP       *http.Client
	Repository string
}

func NewClient(repository string) Client {
	return Client{HTTP: &http.Client{Timeout: 20 * time.Second}, Repository: repository}
}

func (client Client) Check(ctx context.Context, current, channel string) (CheckResult, error) {
	if channel == "" {
		channel = "stable"
	}
	if channel != "stable" && channel != "beta" {
		return CheckResult{}, errors.New("update channel must be stable or beta")
	}
	if client.Repository != config.OfficialUpdateRepository {
		return CheckResult{}, errors.New("update repository does not match the compiled trust root")
	}
	if client.HTTP == nil {
		client.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
	parts := strings.Split(client.Repository, "/")
	endpoint := "https://api.github.com/repos/" + parts[0] + "/" + parts[1] + "/releases"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return CheckResult{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "sni-proxy-updater")
	response, err := client.HTTP.Do(request)
	if err != nil {
		return CheckResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return CheckResult{}, fmt.Errorf("GitHub releases API returned %s", response.Status)
	}
	var releases []Release
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<20))
	if err := decoder.Decode(&releases); err != nil {
		return CheckResult{}, err
	}
	var selected Release
	selectedFound := false
	for _, release := range releases {
		if release.Draft {
			continue
		}
		parsed, err := parseSemVersion(release.TagName)
		if err != nil {
			return CheckResult{}, fmt.Errorf("release has an invalid SemVer tag %q: %w", release.TagName, err)
		}
		if channel == "stable" && (release.Prerelease || len(parsed.prerelease) > 0) {
			continue
		}
		if !selectedFound {
			selected, selectedFound = release, true
			continue
		}
		comparison, _ := compareVersions(release.TagName, selected.TagName)
		if comparison > 0 {
			selected = release
		}
	}
	if !selectedFound {
		return CheckResult{}, errors.New("no release is available for the selected channel")
	}
	available := true
	if current != "dev" {
		comparison, err := compareVersions(selected.TagName, current)
		if err != nil {
			return CheckResult{}, fmt.Errorf("compare release versions: %w", err)
		}
		available = comparison > 0
	}
	return CheckResult{Current: current, Latest: selected.TagName, Available: available, Channel: channel, Release: selected}, nil
}

type ApplyOptions struct {
	Paths       config.Paths
	Release     Release
	Manager     service.Manager
	PublicKey   ed25519.PublicKey
	Prepare     func(context.Context, string, config.Paths) error
	HealthCheck func(context.Context, config.Paths) error
}

type TransactionError struct {
	Cause       error
	RollbackErr error
}

func (err TransactionError) Error() string {
	if err.RollbackErr != nil {
		return fmt.Sprintf("update failed and rollback also failed: update=%v; rollback=%v", err.Cause, err.RollbackErr)
	}
	return fmt.Sprintf("update failed; previous version was restored: %v", err.Cause)
}

func (err TransactionError) Unwrap() error { return err.Cause }

func (client Client) Apply(ctx context.Context, options ApplyOptions) error {
	if err := options.Paths.Validate(); err != nil {
		return fmt.Errorf("unsafe update paths: %w", err)
	}
	if options.Manager == nil {
		return errors.New("a supported service manager is required for transactional updates")
	}
	if len(options.PublicKey) != ed25519.PublicKeySize {
		return errors.New("a trusted Ed25519 release public key is required")
	}
	if options.Prepare == nil {
		if err := privilege.RequireRoot(); err != nil {
			return err
		}
	}
	_, err := validateCurrentInstallation(options.Paths)
	if err != nil {
		return err
	}
	if client.Repository != config.OfficialUpdateRepository {
		return errors.New("update repository does not match the compiled trust root")
	}
	if client.HTTP == nil {
		client.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
	archiveAsset, manifestAsset, signatureAsset, err := releaseAssets(options.Release)
	if err != nil {
		return err
	}
	parent := filepath.Dir(options.Paths.InstallDir)
	staging, err := os.MkdirTemp(parent, ".sni-proxy-update-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	archivePath := filepath.Join(staging, archiveAsset.Name)
	manifestPath := filepath.Join(staging, manifestAsset.Name)
	signaturePath := filepath.Join(staging, signatureAsset.Name)
	if err := client.download(ctx, archiveAsset.DownloadURL, archivePath, maximumArchiveSize); err != nil {
		return err
	}
	if err := client.download(ctx, manifestAsset.DownloadURL, manifestPath, 64<<10); err != nil {
		return err
	}
	if err := client.download(ctx, signatureAsset.DownloadURL, signaturePath, 4<<10); err != nil {
		return err
	}
	if err := verifySignedManifest(manifestPath, signaturePath, archivePath, archiveAsset.Name, options.Release.TagName, options.PublicKey); err != nil {
		return err
	}
	extracted := filepath.Join(staging, "payload")
	if err := extractArchive(archivePath, extracted); err != nil {
		return err
	}
	if err := renderPayload(extracted, options.Paths); err != nil {
		return err
	}
	if err := preserveInstallMetadata(options.Paths.MetadataFile(), filepath.Join(extracted, "install-meta.json")); err != nil {
		return err
	}
	if err := copyMarker(config.ManagedMarkerFile(options.Paths.InstallDir), config.ManagedMarkerFile(extracted)); err != nil {
		return err
	}
	if err := validatePayload(extracted); err != nil {
		return err
	}
	prepare := options.Prepare
	if prepare == nil {
		prepare = preparePayload
	}
	if err := prepare(ctx, extracted, options.Paths); err != nil {
		return fmt.Errorf("prepare privileged update payload: %w", err)
	}

	rollback := filepath.Join(parent, ".sni-proxy-rollback-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	if _, err := os.Lstat(rollback); !errors.Is(err, os.ErrNotExist) {
		return errors.New("rollback directory already exists")
	}
	if err := os.Rename(options.Paths.InstallDir, rollback); err != nil {
		return fmt.Errorf("prepare rollback: %w", err)
	}
	if err := os.Rename(extracted, options.Paths.InstallDir); err != nil {
		_ = os.Rename(rollback, options.Paths.InstallDir)
		return fmt.Errorf("activate update: %w", err)
	}

	healthCheck := options.HealthCheck
	if healthCheck == nil {
		healthCheck = waitForHealth
	}
	activationErr := activateAndCheck(ctx, options.Manager, options.Paths, healthCheck)
	if activationErr != nil {
		rollbackErr := restorePrevious(ctx, options.Manager, options.Paths, rollback, staging, healthCheck)
		return TransactionError{Cause: activationErr, RollbackErr: rollbackErr}
	}

	backupTarget := filepath.Join(options.Paths.BackupDir(), filepath.Base(rollback))
	if err := os.MkdirAll(options.Paths.BackupDir(), 0o750); err != nil {
		return fmt.Errorf("update is healthy but preserving rollback backup failed: %w", err)
	}
	if err := os.Rename(rollback, backupTarget); err != nil {
		if copyErr := copyTree(rollback, backupTarget); copyErr != nil {
			return fmt.Errorf("update is healthy but preserving rollback backup failed: %w", copyErr)
		}
		if capabilityErr := setBinaryCapability(ctx, filepath.Join(backupTarget, "bin", "sni-proxy")); capabilityErr != nil {
			return fmt.Errorf("update is healthy but preserving rollback capability failed: %w", capabilityErr)
		}
		if removeErr := os.RemoveAll(rollback); removeErr != nil {
			return fmt.Errorf("update is healthy but cleaning rollback staging failed: %w", removeErr)
		}
	}
	return nil
}

func validateCurrentInstallation(paths config.Paths) (config.InstallMetadata, error) {
	metadata, err := config.LoadInstallMetadata(paths.MetadataFile())
	if err != nil {
		return config.InstallMetadata{}, fmt.Errorf("read installation metadata: %w", err)
	}
	if metadata.InstallDir != paths.InstallDir || metadata.ConfigDir != paths.ConfigDir || metadata.DataDir != paths.DataDir || metadata.LogDir != paths.LogDir {
		return config.InstallMetadata{}, errors.New("installation metadata paths do not match the active installation")
	}
	if err := config.VerifyManagedMarkers(paths, metadata.InstallationID); err != nil {
		return config.InstallMetadata{}, fmt.Errorf("verify managed directory markers: %w", err)
	}
	for _, marker := range []string{paths.MetadataFile(), paths.VersionFile(), paths.BinaryFile()} {
		if info, err := os.Stat(marker); err != nil || !info.Mode().IsRegular() {
			return config.InstallMetadata{}, fmt.Errorf("current installation marker is missing: %s", marker)
		}
	}
	return metadata, nil
}

func activateAndCheck(ctx context.Context, manager service.Manager, paths config.Paths, healthCheck func(context.Context, config.Paths) error) error {
	operationContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := manager.Install(operationContext); err != nil {
		return fmt.Errorf("register updated service: %w", err)
	}
	if err := manager.Restart(operationContext); err != nil {
		return fmt.Errorf("restart updated service: %w", err)
	}
	if err := healthCheck(operationContext, paths); err != nil {
		return fmt.Errorf("updated service health check: %w", err)
	}
	return nil
}

func restorePrevious(ctx context.Context, manager service.Manager, paths config.Paths, rollback, staging string, healthCheck func(context.Context, config.Paths) error) error {
	rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
	defer cancel()
	var rollbackErrors []error
	if err := manager.Stop(rollbackContext); err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("stop failed version: %w", err))
	}
	failedTree := filepath.Join(staging, "failed-active")
	if err := os.Rename(paths.InstallDir, failedTree); err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("move failed version aside: %w", err))
	}
	if err := os.Rename(rollback, paths.InstallDir); err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("restore previous files: %w", err))
		return errors.Join(rollbackErrors...)
	}
	if err := manager.Install(rollbackContext); err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("restore previous service registration: %w", err))
	}
	if err := manager.Restart(rollbackContext); err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("restart previous service: %w", err))
	} else if err := healthCheck(rollbackContext, paths); err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("previous service health check: %w", err))
	}
	return errors.Join(rollbackErrors...)
}

func waitForHealth(ctx context.Context, paths config.Paths) error {
	configuration, err := config.Load(paths.ConfigFile())
	if err != nil {
		return err
	}
	host := configuration.AdminListenAddress
	if host == "0.0.0.0" {
		host = "127.0.0.1"
	} else if host == "::" {
		host = "::1"
	}
	endpoint := "http://" + net.JoinHostPort(host, strconv.Itoa(configuration.WebPort)) + "/healthz"
	deadline := time.Now().Add(updateHealthTimeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		response, requestErr := client.Do(request)
		if requestErr == nil && response.StatusCode == http.StatusOK {
			io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
			response.Body.Close()
			proxyHost := host
			if configuration.ProxyListenAddress != "" {
				proxyHost = configuration.ProxyListenAddress
			}
			if proxyHost == "0.0.0.0" {
				proxyHost = "127.0.0.1"
			} else if proxyHost == "::" {
				proxyHost = "::1"
			}
			ready := true
			for _, port := range []int{configuration.HTTPPort, configuration.HTTPSPort} {
				connection, dialErr := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(proxyHost, strconv.Itoa(port)))
				if dialErr != nil {
					ready = false
					break
				}
				connection.Close()
			}
			if ready {
				return nil
			}
		}
		if response != nil {
			response.Body.Close()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("health endpoint did not become ready before the deadline")
}

func preparePayload(ctx context.Context, root string, paths config.Paths) error {
	binaryPath := filepath.Join(root, "bin", "sni-proxy")
	binary, err := elf.Open(binaryPath)
	if err != nil {
		return fmt.Errorf("updated binary is not ELF: %w", err)
	}
	if binary.Class != elf.ELFCLASS64 || binary.Machine != elf.EM_X86_64 {
		binary.Close()
		return errors.New("updated binary is not linux/amd64 ELF")
	}
	if err := binary.Close(); err != nil {
		return err
	}
	if err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("prepared payload contains a symbolic link")
		}
		if err := os.Chown(path, 0, 0); err != nil {
			return err
		}
		if info.IsDir() {
			return os.Chmod(path, 0o755)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("set payload owner and mode: %w", err)
	}
	if err := os.Chmod(binaryPath, 0o755); err != nil {
		return err
	}
	return setBinaryCapability(ctx, binaryPath)
}

func setBinaryCapability(ctx context.Context, binaryPath string) error {
	setcapPath, err := capabilityTool("setcap")
	if err != nil {
		return err
	}
	getcapPath, err := capabilityTool("getcap")
	if err != nil {
		return err
	}
	setcap := exec.CommandContext(ctx, setcapPath, "cap_net_bind_service=+ep", binaryPath)
	if output, err := setcap.CombinedOutput(); err != nil {
		return fmt.Errorf("set CAP_NET_BIND_SERVICE: %w: %s", err, strings.TrimSpace(string(output)))
	}
	getcap := exec.CommandContext(ctx, getcapPath, binaryPath)
	output, err := getcap.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "cap_net_bind_service") {
		return fmt.Errorf("verify CAP_NET_BIND_SERVICE: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func capabilityTool(name string) (string, error) {
	for _, directory := range []string{"/usr/sbin", "/sbin", "/usr/bin", "/bin"} {
		candidate := filepath.Join(directory, name)
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o022 == 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("trusted %s executable was not found", name)
}

type signedManifest struct {
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
	Archive      string `json:"archive"`
	SHA256       string `json:"sha256"`
}

func verifySignedManifest(manifestPath, signaturePath, archivePath, archiveName, releaseVersion string, publicKey ed25519.PublicKey) error {
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	if len(manifestBytes) == 0 || len(manifestBytes) > 64<<10 {
		return errors.New("release manifest size is invalid")
	}
	signatureText, err := os.ReadFile(signaturePath)
	if err != nil {
		return err
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signatureText)))
	if err != nil {
		signature, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(string(signatureText)))
	}
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(publicKey, manifestBytes, signature) {
		return errors.New("release manifest signature is invalid")
	}
	var manifest signedManifest
	decoder := json.NewDecoder(strings.NewReader(string(manifestBytes)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return fmt.Errorf("decode signed manifest: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("signed manifest must contain exactly one JSON object")
	}
	if manifest.Version != releaseVersion || manifest.Architecture != "amd64" || manifest.Archive != archiveName {
		return errors.New("signed manifest does not match the selected release asset")
	}
	want, err := hex.DecodeString(manifest.SHA256)
	if err != nil || len(want) != sha256.Size {
		return errors.New("signed manifest SHA-256 is invalid")
	}
	archive, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer archive.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, archive); err != nil {
		return err
	}
	if !equalBytes(want, digest.Sum(nil)) {
		return errors.New("release archive SHA-256 does not match the signed manifest")
	}
	return nil
}

func releaseAssets(release Release) (Asset, Asset, Asset, error) {
	var archive, manifest, signature Asset
	for _, asset := range release.Assets {
		switch asset.Name {
		case "sni-proxy-linux-amd64.tar.gz":
			archive = asset
		case "sni-proxy-linux-amd64.manifest.json":
			manifest = asset
		case "sni-proxy-linux-amd64.manifest.json.sig":
			signature = asset
		}
	}
	if archive.DownloadURL == "" || manifest.DownloadURL == "" || signature.DownloadURL == "" {
		return Asset{}, Asset{}, Asset{}, errors.New("release does not contain the archive, signed manifest, and signature")
	}
	return archive, manifest, signature, nil
}

func (client Client) download(ctx context.Context, source, destination string, limit int64) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}
	response, err := client.HTTP.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %s", response.Status)
	}
	if response.ContentLength > limit {
		return errors.New("download exceeds size limit")
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	written, err := io.Copy(file, io.LimitReader(response.Body, limit+1))
	if err != nil {
		return err
	}
	if written > limit {
		return errors.New("download exceeds size limit")
	}
	return file.Sync()
}

func extractArchive(archivePath, destination string) error {
	if err := os.Mkdir(destination, 0o750); err != nil {
		return err
	}
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(io.LimitReader(file, maximumArchiveSize+1))
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	var extractedSize int64
	for {
		header, nextErr := tarReader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nextErr
		}
		name := filepath.Clean(filepath.FromSlash(header.Name))
		if name == "." {
			if header.Typeflag == tar.TypeDir {
				continue
			}
			return errors.New("release archive contains an invalid root entry")
		}
		if strings.HasPrefix(header.Name, "/") || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) || strings.Contains(header.Name, "\\") {
			return errors.New("release archive contains an unsafe path")
		}
		target := filepath.Join(destination, name)
		relative, err := filepath.Rel(destination, target)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("release archive path escaped staging directory")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				return err
			}
		case tar.TypeReg:
			if header.Size < 0 || header.Size > maximumArchiveSize || extractedSize+header.Size > maximumExtractedSize {
				return errors.New("release extracted size exceeds limit")
			}
			extractedSize += header.Size
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if header.FileInfo().Mode()&0o111 != 0 {
				mode = 0o755
			}
			output, openErr := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if openErr != nil {
				return openErr
			}
			_, copyErr := io.CopyN(output, tarReader, header.Size)
			closeErr := output.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return errors.New("release archive contains a link or unsupported entry")
		}
	}
	return nil
}

func validatePayload(root string) error {
	required := []string{
		"bin/sni-proxy", "bin/sni-proxyctl", "libexec/install.sh", "libexec/common.sh", "libexec/distro.sh", "libexec/service.sh",
		"libexec/systemd.sh", "libexec/openrc.sh", "libexec/sysv.sh", "libexec/runit.sh", "libexec/network.sh", "libexec/update.sh", "libexec/uninstall.sh",
		"service/systemd/sni-proxy.service", "service/openrc/sni-proxy", "service/sysvinit/sni-proxy", "service/runit/sni-proxy/run", "VERSION", "install-meta.json", config.ManagedMarkerName,
	}
	for _, name := range required {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("release payload is missing %s", name)
		}
	}
	return nil
}

func renderPayload(root string, paths config.Paths) error {
	replacements := map[string]string{
		"@INSTALL_DIR@": paths.InstallDir, "@CONFIG_DIR@": paths.ConfigDir, "@DATA_DIR@": paths.DataDir, "@LOG_DIR@": paths.LogDir,
		"@SERVICE_USER@": "sni-proxy", "@SERVICE_GROUP@": "sni-proxy",
	}
	files := map[string]os.FileMode{
		"service/systemd/sni-proxy.service": 0o644, "service/openrc/sni-proxy": 0o755,
		"service/sysvinit/sni-proxy": 0o755, "service/runit/sni-proxy/run": 0o755,
	}
	for name, mode := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read service template %s: %w", name, err)
		}
		value := string(content)
		for placeholder, replacement := range replacements {
			value = strings.ReplaceAll(value, placeholder, replacement)
		}
		if strings.Contains(value, "@INSTALL_DIR@") || strings.Contains(value, "@CONFIG_DIR@") {
			return fmt.Errorf("service template %s contains unresolved placeholders", name)
		}
		if err := os.WriteFile(path, []byte(value), mode); err != nil {
			return err
		}
	}
	return nil
}

func preserveInstallMetadata(source, destination string) error {
	content, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read current install metadata: %w", err)
	}
	if len(content) > 256<<10 {
		return errors.New("current install metadata exceeds size limit")
	}
	return os.WriteFile(destination, content, 0o644)
}

func copyMarker(source, destination string) error {
	content, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read installation marker: %w", err)
	}
	if len(content) > 4096 {
		return errors.New("installation marker exceeds size limit")
	}
	return os.WriteFile(destination, content, 0o644)
}

func copyTree(source, destination string) error {
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return errors.New("backup target already exists")
	}
	return filepath.Walk(source, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("rollback tree contains an unexpected symbolic link")
		}
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return errors.New("rollback tree contains an unsupported file type")
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

type semVersion struct {
	major, minor, patch uint64
	prerelease          []string
}

func parseSemVersion(value string) (semVersion, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "v")
	if value == "" {
		return semVersion{}, errors.New("version is empty")
	}
	coreAndPre := strings.SplitN(value, "+", 2)
	if len(coreAndPre) == 2 && !validIdentifiers(coreAndPre[1], false) {
		return semVersion{}, errors.New("build metadata is invalid")
	}
	versionAndPre := strings.SplitN(coreAndPre[0], "-", 2)
	core := strings.Split(versionAndPre[0], ".")
	if len(core) != 3 {
		return semVersion{}, errors.New("major, minor, and patch are required")
	}
	parsed := semVersion{}
	values := []*uint64{&parsed.major, &parsed.minor, &parsed.patch}
	for index, component := range core {
		if !validNumericIdentifier(component) {
			return semVersion{}, errors.New("core version contains an invalid integer")
		}
		number, err := strconv.ParseUint(component, 10, 64)
		if err != nil {
			return semVersion{}, errors.New("core version integer is out of range")
		}
		*values[index] = number
	}
	if len(versionAndPre) == 2 {
		if !validIdentifiers(versionAndPre[1], true) {
			return semVersion{}, errors.New("prerelease is invalid")
		}
		parsed.prerelease = strings.Split(versionAndPre[1], ".")
	}
	return parsed, nil
}

func validIdentifiers(value string, rejectNumericLeadingZeros bool) bool {
	if value == "" {
		return false
	}
	for _, identifier := range strings.Split(value, ".") {
		if identifier == "" {
			return false
		}
		numeric := true
		for _, character := range identifier {
			if (character < '0' || character > '9') && (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') && character != '-' {
				return false
			}
			if character < '0' || character > '9' {
				numeric = false
			}
		}
		if rejectNumericLeadingZeros && numeric && len(identifier) > 1 && identifier[0] == '0' {
			return false
		}
	}
	return true
}

func validNumericIdentifier(value string) bool {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func compareVersions(left, right string) (int, error) {
	a, err := parseSemVersion(left)
	if err != nil {
		return 0, err
	}
	b, err := parseSemVersion(right)
	if err != nil {
		return 0, err
	}
	for _, pair := range [][2]uint64{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if pair[0] > pair[1] {
			return 1, nil
		}
		if pair[0] < pair[1] {
			return -1, nil
		}
	}
	if len(a.prerelease) == 0 && len(b.prerelease) == 0 {
		return 0, nil
	}
	if len(a.prerelease) == 0 {
		return 1, nil
	}
	if len(b.prerelease) == 0 {
		return -1, nil
	}
	for index := 0; index < len(a.prerelease) && index < len(b.prerelease); index++ {
		leftID, rightID := a.prerelease[index], b.prerelease[index]
		leftNumber, leftErr := strconv.ParseUint(leftID, 10, 64)
		rightNumber, rightErr := strconv.ParseUint(rightID, 10, 64)
		switch {
		case leftErr == nil && rightErr == nil:
			if leftNumber > rightNumber {
				return 1, nil
			}
			if leftNumber < rightNumber {
				return -1, nil
			}
		case leftErr == nil:
			return -1, nil
		case rightErr == nil:
			return 1, nil
		default:
			if leftID > rightID {
				return 1, nil
			}
			if leftID < rightID {
				return -1, nil
			}
		}
	}
	if len(a.prerelease) > len(b.prerelease) {
		return 1, nil
	}
	if len(a.prerelease) < len(b.prerelease) {
		return -1, nil
	}
	return 0, nil
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
}
