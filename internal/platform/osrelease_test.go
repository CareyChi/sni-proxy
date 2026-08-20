package platform

import (
	"strings"
	"testing"
)

func TestParseOSRelease(t *testing.T) {
	input := `NAME="Alpine Linux"
ID=alpine
VERSION_ID=3.22.1
PRETTY_NAME="Alpine Linux 3.22"
IGNORED=$(touch /tmp/never-run)
ID_LIKE='busybox linux'
`
	values, err := ParseOSRelease(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if values["ID"] != "alpine" || values["VERSION_ID"] != "3.22.1" || values["ID_LIKE"] != "busybox linux" {
		t.Fatalf("unexpected values: %#v", values)
	}
	if _, exists := values["IGNORED"]; exists {
		t.Fatal("unknown key must not be retained")
	}
}

func TestNormalizeArchitecture(t *testing.T) {
	for _, value := range []string{"amd64", "x86_64"} {
		architecture, err := NormalizeArchitecture(value)
		if err != nil || architecture != "amd64" {
			t.Fatalf("NormalizeArchitecture(%q) = %q, %v", value, architecture, err)
		}
	}
	if _, err := NormalizeArchitecture("aarch64"); err == nil {
		t.Fatal("aarch64 must be rejected during the amd64-only stage")
	}
}
