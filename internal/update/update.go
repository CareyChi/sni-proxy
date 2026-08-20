package update

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/CareyChi/sni-proxy/internal/config"
	"github.com/CareyChi/sni-proxy/internal/service"
)

const maximumArchiveSize = 256 << 20

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
	return Client{
		HTTP:       &http.Client{Timeout: 20 * time.Second},
		Repository: repository,
	}
}

func (client Client) Check(ctx context.Context, current, channel string) (CheckResult, error) {
	if channel == "" {
		channel = "stable"
	}
	if channel != "stable" && channel != "beta" {
		return CheckResult{}, errors.New("update channel must be stable or beta")
	}
	if client.HTTP == nil {
		client.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
	parts := strings.Split(client.Repository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return CheckResult{}, errors.New("repository must use owner/name form")
	}
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
	for _, release := range releases {
		if release.Draft || (channel == "stable" && release.Prerelease) {
			continue
		}
		return CheckResult{
			Current:   current,
			Latest:    release.TagName,
			Available: compareVersions(release.TagName, current) > 0,
			Channel:   channel,
			Release:   release,
		}, nil
	}
	return CheckResult{}, errors.New("no release is available for the selected channel")
}

type ApplyOptions struct {
	Paths   config.Paths
	Release Release
	Manager service.Manager
}

func (client Client) Apply(ctx context.Context, options ApplyOptions) error {
	if err := options.Paths.Validate(); err != nil {
		return fmt.Errorf("unsafe update paths: %w", err)
	}
	for _, marker := range []string{options.Paths.MetadataFile(), options.Paths.VersionFile(), options.Paths.BinaryFile()} {
		if info, err := os.Stat(marker); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("current installation marker is missing: %s", marker)
		}
	}
	if client.HTTP == nil {
		client.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
	archiveAsset, checksumAsset, err := releaseAssets(options.Release)
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
	checksumPath := filepath.Join(staging, checksumAsset.Name)
	if err := client.download(ctx, archiveAsset.DownloadURL, archivePath, maximumArchiveSize); err != nil {
		return err
	}
	if err := client.download(ctx, checksumAsset.DownloadURL, checksumPath, 1<<20); err != nil {
		return err
	}
	if err := verifyChecksum(archivePath, checksumPath); err != nil {
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
	if err := validatePayload(extracted); err != nil {
		return err
	}
	rollback := filepath.Join(parent, ".sni-proxy-rollback-"+strconv.FormatInt(time.Now().Unix(), 10))
	if _, err := os.Lstat(rollback); !errors.Is(err, os.ErrNotExist) {
		return errors.New("rollback directory already exists")
	}
	if err := os.Rename(options.Paths.InstallDir, rollback); err != nil {
		return fmt.Errorf("prepare rollback: %w", err)
	}
	restore := true
	defer func() {
		if restore {
			os.RemoveAll(options.Paths.InstallDir)
			os.Rename(rollback, options.Paths.InstallDir)
		}
	}()
	if err := os.Rename(extracted, options.Paths.InstallDir); err != nil {
		return fmt.Errorf("activate update: %w", err)
	}
	if options.Manager != nil {
		if err := options.Manager.Install(ctx); err != nil {
			return err
		}
	}
	restore = false
	backupTarget := filepath.Join(options.Paths.BackupDir(), filepath.Base(rollback))
	if err := os.MkdirAll(options.Paths.BackupDir(), 0o750); err != nil {
		return fmt.Errorf("update succeeded but creating backup directory failed: %w", err)
	}
	if err := os.Rename(rollback, backupTarget); err != nil {
		if copyErr := copyTree(rollback, backupTarget); copyErr != nil {
			return fmt.Errorf("update succeeded but preserving rollback backup failed: %w", copyErr)
		}
		if removeErr := os.RemoveAll(rollback); removeErr != nil {
			return fmt.Errorf("update succeeded but cleaning local rollback failed: %w", removeErr)
		}
	}
	return nil
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

func renderPayload(root string, paths config.Paths) error {
	replacements := map[string]string{
		"@INSTALL_DIR@":  paths.InstallDir,
		"@CONFIG_DIR@":   paths.ConfigDir,
		"@DATA_DIR@":     paths.DataDir,
		"@LOG_DIR@":      paths.LogDir,
		"@SERVICE_USER@": "sni-proxy",
		"@SERVICE_GROUP@": "sni-proxy",
	}
	files := map[string]os.FileMode{
		"service/systemd/sni-proxy.service": 0o644,
		"service/openrc/sni-proxy":           0o755,
		"service/sysvinit/sni-proxy":         0o755,
		"service/runit/sni-proxy/run":        0o755,
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

func releaseAssets(release Release) (Asset, Asset, error) {
	var archive, checksum Asset
	for _, asset := range release.Assets {
		switch asset.Name {
		case "sni-proxy-linux-amd64.tar.gz":
			archive = asset
		case "sni-proxy-linux-amd64.tar.gz.sha256":
			checksum = asset
		}
	}
	if archive.DownloadURL == "" || checksum.DownloadURL == "" {
		return Asset{}, Asset{}, errors.New("release does not contain the complete linux-amd64 package and checksum")
	}
	return archive, checksum, nil
}

func verifyChecksum(archivePath, checksumPath string) error {
	checksumFile, err := os.Open(checksumPath)
	if err != nil {
		return err
	}
	defer checksumFile.Close()
	scanner := bufio.NewScanner(io.LimitReader(checksumFile, 1<<20))
	if !scanner.Scan() {
		return errors.New("checksum file is empty")
	}
	fields := strings.Fields(scanner.Text())
	if len(fields) < 1 || len(fields[0]) != sha256.Size*2 {
		return errors.New("checksum file is invalid")
	}
	want, err := hex.DecodeString(fields[0])
	if err != nil {
		return err
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
		return errors.New("release archive SHA-256 mismatch")
	}
	return nil
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
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	for {
		header, nextErr := tarReader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nextErr
		}
		name := filepath.Clean(filepath.FromSlash(header.Name))
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return errors.New("release archive contains an unsafe path")
		}
		target := filepath.Join(destination, name)
		if !strings.HasPrefix(target+string(filepath.Separator), destination+string(filepath.Separator)) {
			return errors.New("release archive path escaped staging directory")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				return err
			}
		case tar.TypeReg:
			if header.Size < 0 || header.Size > maximumArchiveSize {
				return errors.New("release file exceeds size limit")
			}
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
		"service/systemd/sni-proxy.service", "service/openrc/sni-proxy",
		"service/sysvinit/sni-proxy", "service/runit/sni-proxy/run", "VERSION",
	}
	for _, name := range required {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("release payload is missing %s", name)
		}
	}
	return nil
}

func compareVersions(left, right string) int {
	parse := func(value string) []int {
		value = strings.TrimPrefix(strings.TrimSpace(value), "v")
		value = strings.SplitN(value, "-", 2)[0]
		parts := strings.Split(value, ".")
		result := make([]int, len(parts))
		for index, part := range parts {
			result[index], _ = strconv.Atoi(part)
		}
		return result
	}
	a, b := parse(left), parse(right)
	maximum := len(a)
	if len(b) > maximum {
		maximum = len(b)
	}
	for index := 0; index < maximum; index++ {
		var av, bv int
		if index < len(a) {
			av = a[index]
		}
		if index < len(b) {
			bv = b[index]
		}
		if av > bv {
			return 1
		}
		if av < bv {
			return -1
		}
	}
	return 0
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
