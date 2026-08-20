package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecureDefaults(t *testing.T) {
	configuration := DefaultConfig()
	if configuration.ProxyListenAddress != "0.0.0.0" || configuration.AdminListenAddress != "127.0.0.1" {
		t.Fatalf("unexpected listener defaults: %#v", configuration)
	}
	if len(configuration.AllowedDomains) != 0 {
		t.Fatal("default allowlist must be empty and therefore deny all")
	}
	if configuration.MaxConnections < 1 || configuration.MaxConnectionsIP < 1 {
		t.Fatal("connection limits must be enabled by default")
	}
}

func TestLegacyListenAddressMigratesProxyOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	content := `{"http_port":80,"https_port":443,"web_port":6866,"listen_address":"192.0.2.2","allowed_domains":["example.com"],"allow_private_upstreams":false,"dial_timeout":"10s","idle_timeout":"5m","max_connections":1024,"max_connections_per_ip":32,"update_repository":"CareyChi/sni-proxy"}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	configuration, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.ProxyListenAddress != "192.0.2.2" || configuration.AdminListenAddress != "127.0.0.1" {
		t.Fatalf("legacy listener migration exposed admin: %#v", configuration)
	}
}

func TestUpdateRepositoryIsPinned(t *testing.T) {
	configuration := DefaultConfig()
	configuration.UpdateRepository = "attacker/repository"
	if configuration.Validate() == nil {
		t.Fatal("untrusted update repository was accepted")
	}
}
