package network

import (
	"net/netip"
	"reflect"
	"testing"
)

func TestDomainAllowedDenyByDefault(t *testing.T) {
	tests := []struct {
		host    string
		allowed []string
		want    bool
	}{
		{"example.com", nil, false},
		{"example.com", []string{"example.com"}, true},
		{"www.example.com", []string{"example.com"}, true},
		{"notexample.com", []string{"example.com"}, false},
		{"example.com.attacker.invalid", []string{"example.com"}, false},
		{"EXAMPLE.COM.", []string{".example.com"}, true},
	}
	for _, test := range tests {
		if got := DomainAllowed(test.host, test.allowed); got != test.want {
			t.Errorf("DomainAllowed(%q, %v) = %v, want %v", test.host, test.allowed, got, test.want)
		}
	}
}

func TestSpecialUseAddressPolicy(t *testing.T) {
	blocked := []string{
		"0.0.0.1", "10.0.0.1", "100.64.0.1", "127.0.0.1", "169.254.1.1",
		"172.16.0.1", "192.0.2.1", "192.168.1.1", "198.18.0.1",
		"198.51.100.1", "203.0.113.1", "240.0.0.1", "::1", "2001:db8::1",
		"fc00::1", "fe80::1", "ff02::1", "::ffff:192.168.1.1",
	}
	for _, value := range blocked {
		if IsPublicAddress(netip.MustParseAddr(value)) {
			t.Errorf("special-use address %s was accepted", value)
		}
	}
	for _, value := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !IsPublicAddress(netip.MustParseAddr(value)) {
			t.Errorf("public address %s was rejected", value)
		}
	}
}

func TestInterleaveFamilies(t *testing.T) {
	input := []netip.Addr{
		netip.MustParseAddr("2001:4860:4860::8888"),
		netip.MustParseAddr("2606:4700:4700::1111"),
		netip.MustParseAddr("8.8.8.8"),
		netip.MustParseAddr("1.1.1.1"),
	}
	want := []netip.Addr{input[0], input[2], input[1], input[3]}
	if got := interleaveFamilies(input); !reflect.DeepEqual(got, want) {
		t.Fatalf("interleaveFamilies = %v, want %v", got, want)
	}
}
