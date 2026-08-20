package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const OfficialUpdateRepository = "CareyChi/sni-proxy"

type Config struct {
	HTTPPort           int      `json:"http_port"`
	HTTPSPort          int      `json:"https_port"`
	WebPort            int      `json:"web_port"`
	ProxyListenAddress string   `json:"proxy_listen_address"`
	AdminListenAddress string   `json:"admin_listen_address"`
	AdminPublicURL     string   `json:"admin_public_url,omitempty"`
	CookieSecure       bool     `json:"cookie_secure"`
	AllowedDomains     []string `json:"allowed_domains"`
	AllowPrivate       bool     `json:"allow_private_upstreams"`
	DialTimeout        string   `json:"dial_timeout"`
	IdleTimeout        string   `json:"idle_timeout"`
	MaxConnections     int      `json:"max_connections"`
	MaxConnectionsIP   int      `json:"max_connections_per_ip"`
	UpdateRepository   string   `json:"update_repository"`

	// ListenAddress is accepted only to migrate configurations written before
	// the proxy and administration listeners were separated. It never controls
	// the administration listener.
	ListenAddress string `json:"listen_address,omitempty"`
}

func DefaultConfig() Config {
	return Config{
		HTTPPort:           80,
		HTTPSPort:          443,
		WebPort:            6866,
		ProxyListenAddress: "0.0.0.0",
		AdminListenAddress: "127.0.0.1",
		DialTimeout:        "10s",
		IdleTimeout:        "5m",
		MaxConnections:     1024,
		MaxConnectionsIP:   32,
		UpdateRepository:   OfficialUpdateRepository,
	}
}

func (c *Config) migrateLegacy() {
	if c.ListenAddress != "" {
		c.ProxyListenAddress = c.ListenAddress
		c.ListenAddress = ""
	}
}

func (c Config) Validate() error {
	ports := []int{c.HTTPPort, c.HTTPSPort, c.WebPort}
	seen := make(map[int]struct{}, len(ports))
	for _, port := range ports {
		if port < 1 || port > 65535 {
			return fmt.Errorf("port %d is outside 1-65535", port)
		}
		if _, exists := seen[port]; exists {
			return fmt.Errorf("ports must be distinct: %d is repeated", port)
		}
		seen[port] = struct{}{}
	}
	for name, address := range map[string]string{
		"proxy_listen_address": c.ProxyListenAddress,
		"admin_listen_address": c.AdminListenAddress,
	} {
		if address == "" || strings.ContainsAny(address, "\r\n\x00") || net.ParseIP(address) == nil {
			return errors.New(name + " must be an IP address")
		}
	}
	if c.MaxConnections < 1 || c.MaxConnections > 1_000_000 {
		return errors.New("max_connections must be between 1 and 1000000")
	}
	if c.MaxConnectionsIP < 1 || c.MaxConnectionsIP > c.MaxConnections {
		return errors.New("max_connections_per_ip must be positive and no larger than max_connections")
	}
	if c.AdminPublicURL != "" {
		publicURL, err := url.Parse(c.AdminPublicURL)
		if err != nil || !publicURL.IsAbs() || publicURL.Host == "" || (publicURL.Scheme != "http" && publicURL.Scheme != "https") || publicURL.User != nil || publicURL.RawQuery != "" || publicURL.Fragment != "" {
			return errors.New("admin_public_url must be an absolute http(s) URL without credentials, query, or fragment")
		}
		if publicURL.Path != "" && publicURL.Path != "/" {
			return errors.New("admin_public_url must not contain a path")
		}
		if publicURL.Scheme == "https" && !c.CookieSecure {
			return errors.New("cookie_secure must be true when admin_public_url uses HTTPS")
		}
	}
	for _, value := range []string{c.DialTimeout, c.IdleTimeout} {
		duration, err := time.ParseDuration(value)
		if err != nil || duration <= 0 {
			return fmt.Errorf("invalid duration %q", value)
		}
	}
	if c.UpdateRepository != OfficialUpdateRepository {
		return fmt.Errorf("update_repository is pinned to %s", OfficialUpdateRepository)
	}
	for _, domain := range c.AllowedDomains {
		if strings.TrimSpace(domain) == "" || strings.ContainsAny(domain, "/:@\r\n\x00") {
			return fmt.Errorf("invalid allowed domain %q", domain)
		}
	}
	return nil
}

func Load(path string) (Config, error) {
	configuration := DefaultConfig()
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return configuration, nil
	}
	if err != nil {
		return Config{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&configuration); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	configuration.migrateLegacy()
	if err := configuration.Validate(); err != nil {
		return Config{}, err
	}
	return configuration, nil
}

func Save(path string, configuration Config) error {
	configuration.migrateLegacy()
	if err := configuration.Validate(); err != nil {
		return err
	}
	// Runtime configuration contains no secrets and is root-managed. World
	// readability lets the unprivileged daemon load it without making the
	// containing directory writable or trusting a service-owned file.
	return writeJSONAtomic(path, configuration, 0o644)
}

func writeJSONAtomic(path string, value any, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".sni-proxy-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	remove := true
	defer func() {
		file.Close()
		if remove {
			os.Remove(temporary)
		}
	}()
	if err := file.Chmod(mode); err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	remove = false
	return syncDirectory(filepath.Dir(path))
}
