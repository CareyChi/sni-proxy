package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	HTTPPort         int      `json:"http_port"`
	HTTPSPort        int      `json:"https_port"`
	WebPort          int      `json:"web_port"`
	ListenAddress    string   `json:"listen_address"`
	AllowedDomains   []string `json:"allowed_domains,omitempty"`
	AllowPrivate     bool     `json:"allow_private_upstreams"`
	DialTimeout      string   `json:"dial_timeout"`
	IdleTimeout      string   `json:"idle_timeout"`
	UpdateRepository string   `json:"update_repository"`
}

func DefaultConfig() Config {
	return Config{
		HTTPPort:         80,
		HTTPSPort:        443,
		WebPort:          6866,
		ListenAddress:    "0.0.0.0",
		DialTimeout:      "10s",
		IdleTimeout:      "5m",
		UpdateRepository: "CareyChi/sni-proxy",
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
	if c.ListenAddress == "" || strings.ContainsAny(c.ListenAddress, "\r\n\x00") {
		return errors.New("listen_address is invalid")
	}
	for _, value := range []string{c.DialTimeout, c.IdleTimeout} {
		duration, err := time.ParseDuration(value)
		if err != nil || duration <= 0 {
			return fmt.Errorf("invalid duration %q", value)
		}
	}
	parts := strings.Split(c.UpdateRepository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(c.UpdateRepository, " \t\r\n") {
		return errors.New("update_repository must use owner/name form")
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
	if err := configuration.Validate(); err != nil {
		return Config{}, err
	}
	return configuration, nil
}

func Save(path string, configuration Config) error {
	if err := configuration.Validate(); err != nil {
		return err
	}
	return writeJSONAtomic(path, configuration, 0o600)
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
	return nil
}
