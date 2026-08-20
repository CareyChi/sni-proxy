package proxy

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CareyChi/sni-proxy/internal/config"
)

func TestParseClientHelloSNI(t *testing.T) {
	handshake := clientHello("Example.COM")
	hostname, err := parseClientHelloSNI(handshake)
	if err != nil || hostname != "example.com" {
		t.Fatalf("parseClientHelloSNI = %q, %v", hostname, err)
	}
	for split := 0; split < len(handshake); split++ {
		_, err := parseClientHelloSNI(handshake[:split])
		if err == nil {
			t.Fatalf("truncated ClientHello at %d bytes was accepted", split)
		}
	}
}

func TestReadTLSHostnameAcrossManyRecords(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	handshake := clientHello("example.com")
	go func() {
		for _, value := range handshake {
			record := []byte{22, 3, 3, 0, 1, value}
			if _, err := client.Write(record); err != nil {
				return
			}
		}
	}()
	hostname, raw, err := readTLSHostname(server)
	if err != nil || hostname != "example.com" {
		t.Fatalf("readTLSHostname = %q, %v", hostname, err)
	}
	if len(raw) != len(handshake)*6 {
		t.Fatalf("raw length = %d, want %d", len(raw), len(handshake)*6)
	}
}

func TestParseSNIRejectsDeclaredLengthMismatch(t *testing.T) {
	extension := []byte{0, 5, 0, 0, 1, 'a', 0}
	if _, err := parseSNIExtension(extension); err == nil {
		t.Fatal("mismatched SNI list length was accepted")
	}
	if _, err := parseSNIExtension([]byte{0, 3, 0, 0, 0}); err == nil {
		t.Fatal("zero-length hostname was accepted")
	}
}

func TestParseClientHelloRejectsDuplicateSNIExtension(t *testing.T) {
	extension := sniExtension("example.com")
	handshake := clientHelloWithExtensions(append(append([]byte{}, extension...), extension...))
	if _, err := parseClientHelloSNI(handshake); err == nil {
		t.Fatal("duplicate SNI extensions were accepted")
	}
}

func TestHTTPHandlerUsesPerRequestHostPolicy(t *testing.T) {
	configuration := config.DefaultConfig()
	configuration.AllowedDomains = []string{"example.com"}
	server := Server{Config: configuration}
	handler := server.httpHandler(roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(request.URL.Host))}, nil
	}))

	allowed := httptest.NewRequest(http.MethodGet, "http://example.com/path", nil)
	allowed.Host = "example.com"
	allowedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(allowedRecorder, allowed)
	if allowedRecorder.Code != http.StatusOK || !strings.Contains(allowedRecorder.Body.String(), "example.com:80") {
		t.Fatalf("allowed response = %d %q", allowedRecorder.Code, allowedRecorder.Body.String())
	}

	denied := httptest.NewRequest(http.MethodGet, "http://attacker.invalid/", nil)
	denied.Host = "attacker.invalid"
	deniedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(deniedRecorder, denied)
	if deniedRecorder.Code != http.StatusForbidden {
		t.Fatalf("denied response status = %d", deniedRecorder.Code)
	}
}

func TestTunnelDeadlineAppliesWithBufferedReader(t *testing.T) {
	clientPeer, clientServer := net.Pipe()
	upstreamServer, upstreamPeer := net.Pipe()
	defer clientPeer.Close()
	defer clientServer.Close()
	defer upstreamServer.Close()
	defer upstreamPeer.Close()
	done := make(chan struct{})
	go func() {
		tunnel(clientServer, bufio.NewReader(clientServer), upstreamServer, 50*time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("tunnel did not release buffered-reader relay after idle timeout")
	}
}

func TestConnectionLimiter(t *testing.T) {
	limiter := newConnectionLimiter(2, 1)
	first := testAddr("192.0.2.10:1")
	host, ok := limiter.acquire(first)
	if !ok {
		t.Fatal("first connection was rejected")
	}
	if _, ok := limiter.acquire(first); ok {
		t.Fatal("per-IP connection limit was not enforced")
	}
	if _, ok := limiter.acquire(testAddr("192.0.2.11:1")); !ok {
		t.Fatal("second IP was rejected before the global limit")
	}
	if _, ok := limiter.acquire(testAddr("192.0.2.12:1")); ok {
		t.Fatal("global connection limit was not enforced")
	}
	limiter.release(host)
	if _, ok := limiter.acquire(first); !ok {
		t.Fatal("released connection capacity was not restored")
	}
}

func FuzzParseClientHelloSNI(f *testing.F) {
	f.Add(clientHello("example.com"))
	f.Add([]byte{})
	f.Add([]byte{1, 0, 0, 0})
	f.Fuzz(func(t *testing.T, input []byte) {
		hostname, err := parseClientHelloSNI(input)
		if err == nil && hostname == "" {
			t.Fatal("parser returned an empty successful hostname")
		}
	})
}

func clientHello(hostname string) []byte {
	return clientHelloWithExtensions(sniExtension(hostname))
}

func sniExtension(hostname string) []byte {
	name := []byte(hostname)
	serverNameList := make([]byte, 3+len(name))
	serverNameList[0] = 0
	binary.BigEndian.PutUint16(serverNameList[1:3], uint16(len(name)))
	copy(serverNameList[3:], name)
	sni := make([]byte, 2+len(serverNameList))
	binary.BigEndian.PutUint16(sni[0:2], uint16(len(serverNameList)))
	copy(sni[2:], serverNameList)
	extension := make([]byte, 4+len(sni))
	binary.BigEndian.PutUint16(extension[0:2], 0)
	binary.BigEndian.PutUint16(extension[2:4], uint16(len(sni)))
	copy(extension[4:], sni)
	return extension
}

func clientHelloWithExtensions(extensions []byte) []byte {
	var body bytes.Buffer
	body.Write([]byte{3, 3})
	body.Write(make([]byte, 32))
	body.WriteByte(0)
	body.Write([]byte{0, 2, 0x13, 0x01})
	body.Write([]byte{1, 0})
	_ = binary.Write(&body, binary.BigEndian, uint16(len(extensions)))
	body.Write(extensions)

	handshake := make([]byte, 4+body.Len())
	handshake[0] = 1
	handshake[1] = byte(body.Len() >> 16)
	handshake[2] = byte(body.Len() >> 8)
	handshake[3] = byte(body.Len())
	copy(handshake[4:], body.Bytes())
	return handshake
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type testAddr string

func (address testAddr) Network() string { return "tcp" }
func (address testAddr) String() string  { return string(address) }
