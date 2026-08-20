package network

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"
)

type PortCheck struct {
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

func CheckPort(host string, port int) PortCheck {
	if host == "" {
		host = "0.0.0.0"
	}
	result := PortCheck{Host: host, Port: port}
	if port < 1 || port > 65535 {
		result.Reason = "port_out_of_range"
		return result
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		result.Reason = classifyListenError(err)
		return result
	}
	_ = listener.Close()
	result.Available = true
	return result
}

func ServerIPs() []string {
	unique := make(map[string]struct{})
	if address := routeProbeIP(); address != "" {
		unique[address] = struct{}{}
	}
	interfaces, _ := net.Interfaces()
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, _ := iface.Addrs()
		for _, address := range addresses {
			host, _, err := net.ParseCIDR(address.String())
			if err != nil {
				host = net.ParseIP(strings.Split(address.String(), "%")[0])
			}
			if host != nil && host.IsGlobalUnicast() && !host.IsLoopback() && !host.IsLinkLocalUnicast() {
				unique[host.String()] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(unique))
	for address := range unique {
		result = append(result, address)
	}
	sort.Strings(result)
	return result
}

func DialUpstream(ctx context.Context, hostname string, port int, timeout time.Duration, allowPrivate bool, allowedDomains []string) (net.Conn, error) {
	hostname = NormalizeHostname(hostname)
	if !ValidHostname(hostname) || net.ParseIP(hostname) != nil {
		return nil, errors.New("upstream must be a valid DNS hostname")
	}
	if !DomainAllowed(hostname, allowedDomains) {
		return nil, errors.New("upstream hostname is not allowed")
	}
	if timeout <= 0 {
		return nil, errors.New("upstream timeout must be positive")
	}
	budget, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupNetIP(budget, "ip", hostname)
	if err != nil {
		return nil, fmt.Errorf("resolve upstream: %w", err)
	}
	eligible := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if isDialableAddress(address) && (allowPrivate || IsPublicAddress(address)) {
			eligible = append(eligible, address)
		}
	}
	if len(eligible) == 0 {
		return nil, errors.New("upstream returned no policy-eligible addresses")
	}
	return dialHappyEyeballs(budget, eligible, port)
}

func NormalizeHostname(hostname string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(hostname)), ".")
}

// DomainAllowed is deliberately deny-by-default. A suffix entry permits the
// exact domain and its subdomains, but never a merely similar string.
func DomainAllowed(hostname string, allowed []string) bool {
	hostname = NormalizeHostname(hostname)
	if len(allowed) == 0 {
		return false
	}
	for _, candidate := range allowed {
		candidate = strings.TrimPrefix(NormalizeHostname(candidate), ".")
		if candidate != "" && (hostname == candidate || strings.HasSuffix(hostname, "."+candidate)) {
			return true
		}
	}
	return false
}

func ValidHostname(hostname string) bool {
	if len(hostname) < 1 || len(hostname) > 253 || strings.Contains(hostname, "..") {
		return false
	}
	for _, label := range strings.Split(hostname, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

var specialUsePrefixes = mustPrefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
	"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
	"192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
	"203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
	"::/128", "::1/128", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64",
	"2001::/23", "2001:db8::/32", "fc00::/7", "fe80::/10", "ff00::/8",
)

func IsPublicAddress(address netip.Addr) bool {
	if !isDialableAddress(address) {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range specialUsePrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func isDialableAddress(address netip.Addr) bool {
	return address.IsValid() && address.IsGlobalUnicast() && !address.IsUnspecified() && !address.IsMulticast()
}

func mustPrefixes(values ...string) []netip.Prefix {
	result := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		result = append(result, netip.MustParsePrefix(value))
	}
	return result
}

func dialHappyEyeballs(ctx context.Context, addresses []netip.Addr, port int) (net.Conn, error) {
	ordered := interleaveFamilies(addresses)
	if len(ordered) > 8 {
		ordered = ordered[:8]
	}
	type result struct {
		connection net.Conn
		err        error
	}
	results := make(chan result, len(ordered))
	dialContext, cancel := context.WithCancel(ctx)
	defer cancel()
	for index, address := range ordered {
		index, address := index, address
		go func() {
			if index > 0 {
				timer := time.NewTimer(time.Duration(index) * 200 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-dialContext.Done():
					results <- result{err: dialContext.Err()}
					return
				case <-timer.C:
				}
			}
			dialer := net.Dialer{KeepAlive: 30 * time.Second}
			connection, err := dialer.DialContext(dialContext, "tcp", net.JoinHostPort(address.String(), strconv.Itoa(port)))
			results <- result{connection: connection, err: err}
		}()
	}
	var lastError error
	for received := 0; received < len(ordered); received++ {
		attempt := <-results
		if attempt.err == nil {
			cancel()
			remaining := len(ordered) - received - 1
			go func() {
				for index := 0; index < remaining; index++ {
					late := <-results
					if late.connection != nil {
						late.connection.Close()
					}
				}
			}()
			return attempt.connection, nil
		}
		if !errors.Is(attempt.err, context.Canceled) {
			lastError = attempt.err
		}
	}
	if lastError == nil {
		lastError = ctx.Err()
	}
	if lastError == nil {
		lastError = errors.New("no eligible upstream address could be reached")
	}
	return nil, lastError
}

func interleaveFamilies(addresses []netip.Addr) []netip.Addr {
	v6, v4 := make([]netip.Addr, 0), make([]netip.Addr, 0)
	for _, address := range addresses {
		if address.Unmap().Is4() {
			v4 = append(v4, address.Unmap())
		} else {
			v6 = append(v6, address)
		}
	}
	result := make([]netip.Addr, 0, len(addresses))
	for len(v6) > 0 || len(v4) > 0 {
		if len(v6) > 0 {
			result, v6 = append(result, v6[0]), v6[1:]
		}
		if len(v4) > 0 {
			result, v4 = append(result, v4[0]), v4[1:]
		}
	}
	return result
}

func routeProbeIP() string {
	connection, err := net.DialTimeout("udp", "1.1.1.1:53", time.Second)
	if err != nil {
		return ""
	}
	defer connection.Close()
	address, ok := connection.LocalAddr().(*net.UDPAddr)
	if !ok || address.IP == nil || address.IP.IsLoopback() || address.IP.IsUnspecified() {
		return ""
	}
	return address.IP.String()
}

func classifyListenError(err error) string {
	value := strings.ToLower(err.Error())
	switch {
	case strings.Contains(value, "permission"):
		return "permission_denied"
	case strings.Contains(value, "address already in use") || strings.Contains(value, "only one usage"):
		return "address_in_use"
	default:
		return "listen_failed"
	}
}
