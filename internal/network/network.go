package network

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
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
	listener, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprintf("%d", port)))
	if err != nil {
		result.Reason = classifyListenError(err)
		return result
	}
	listener.Close()
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
	hostname = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(hostname)), ".")
	if !validHostname(hostname) || net.ParseIP(hostname) != nil {
		return nil, errors.New("upstream must be a valid DNS hostname")
	}
	if !domainAllowed(hostname, allowedDomains) {
		return nil, errors.New("upstream hostname is not allowed")
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, hostname)
	if err != nil {
		return nil, fmt.Errorf("resolve upstream: %w", err)
	}
	if len(addresses) == 0 {
		return nil, errors.New("upstream hostname returned no addresses")
	}
	dialer := net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	var lastError error
	for _, address := range addresses {
		if !allowPrivate && !isPublicAddress(address.IP) {
			lastError = errors.New("upstream resolved to a non-public address")
			continue
		}
		connection, dialErr := dialer.DialContext(ctx, "tcp", net.JoinHostPort(address.IP.String(), fmt.Sprintf("%d", port)))
		if dialErr == nil {
			return connection, nil
		}
		lastError = dialErr
	}
	if lastError == nil {
		lastError = errors.New("no eligible upstream address")
	}
	return nil, lastError
}

func domainAllowed(hostname string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, candidate := range allowed {
		candidate = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(candidate)), ".")
		if hostname == candidate || strings.HasSuffix(hostname, "."+candidate) {
			return true
		}
	}
	return false
}

func validHostname(hostname string) bool {
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

func isPublicAddress(address net.IP) bool {
	return address.IsGlobalUnicast() && !address.IsPrivate() && !address.IsLoopback() && !address.IsUnspecified() && !address.IsLinkLocalUnicast() && !address.IsLinkLocalMulticast() && !address.IsMulticast()
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
