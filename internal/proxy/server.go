package proxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CareyChi/sni-proxy/internal/config"
	proxynetwork "github.com/CareyChi/sni-proxy/internal/network"
)

const (
	maximumPreface      = 128 << 10
	maximumTLSRecord    = (16 << 10) + 2048
	maximumHTTPHeaders  = 64 << 10
	responseHeaderLimit = 30 * time.Second
)

var errNeedMoreTLSData = errors.New("more TLS handshake data is required")

type Server struct {
	Config config.Config
}

func (server Server) Serve(ctx context.Context) error {
	if err := server.Config.Validate(); err != nil {
		return err
	}
	limiter := newConnectionLimiter(server.Config.MaxConnections, server.Config.MaxConnectionsIP)
	httpListener, err := net.Listen("tcp", net.JoinHostPort(server.Config.ProxyListenAddress, strconv.Itoa(server.Config.HTTPPort)))
	if err != nil {
		return fmt.Errorf("listen HTTP proxy: %w", err)
	}
	httpsListener, err := net.Listen("tcp", net.JoinHostPort(server.Config.ProxyListenAddress, strconv.Itoa(server.Config.HTTPSPort)))
	if err != nil {
		httpListener.Close()
		return fmt.Errorf("listen HTTPS proxy: %w", err)
	}
	httpListener = &limitedListener{Listener: httpListener, limiter: limiter}
	httpsListener = &limitedListener{Listener: httpsListener, limiter: limiter}

	dialTimeout, _ := time.ParseDuration(server.Config.DialTimeout)
	httpTransport := &http.Transport{
		Proxy:                 nil,
		DisableCompression:    true,
		MaxIdleConns:          server.Config.MaxConnections,
		MaxIdleConnsPerHost:   server.Config.MaxConnectionsIP,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: responseHeaderLimit,
		DialContext: func(dialContext context.Context, _, address string) (net.Conn, error) {
			host, port, err := splitHTTPAuthority(address)
			if err != nil || port != 80 {
				return nil, errors.New("HTTP upstream must use port 80")
			}
			return proxynetwork.DialUpstream(dialContext, host, port, dialTimeout, server.Config.AllowPrivate, server.Config.AllowedDomains)
		},
	}
	defer httpTransport.CloseIdleConnections()
	httpServer := &http.Server{
		Handler:           server.httpHandler(httpTransport),
		ReadHeaderTimeout: dialTimeout,
		ReadTimeout:       mustDuration(server.Config.IdleTimeout),
		WriteTimeout:      mustDuration(server.Config.IdleTimeout),
		IdleTimeout:       mustDuration(server.Config.IdleTimeout),
		MaxHeaderBytes:    maximumHTTPHeaders,
	}

	errorsChannel := make(chan error, 2)
	go func() {
		err := httpServer.Serve(httpListener)
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			err = nil
		}
		errorsChannel <- err
	}()
	go server.acceptTLS(ctx, httpsListener, errorsChannel)
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownContext)
		_ = httpsListener.Close()
	}()

	for completed := 0; completed < 2; completed++ {
		if serveErr := <-errorsChannel; serveErr != nil && ctx.Err() == nil {
			_ = httpServer.Close()
			_ = httpsListener.Close()
			return serveErr
		}
	}
	return ctx.Err()
}

func (server Server) httpHandler(transport http.RoundTripper) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodConnect {
			http.Error(writer, "CONNECT is not supported", http.StatusMethodNotAllowed)
			return
		}
		hostname, port, err := splitHTTPAuthority(request.Host)
		if err != nil || port != 80 || !proxynetwork.DomainAllowed(hostname, server.Config.AllowedDomains) {
			http.Error(writer, "proxy target is not allowed", http.StatusForbidden)
			return
		}
		if request.URL.IsAbs() && !sameAuthority(request.URL.Host, request.Host) {
			http.Error(writer, "absolute request authority does not match Host", http.StatusBadRequest)
			return
		}
		target := &url.URL{Scheme: "http", Host: net.JoinHostPort(hostname, "80")}
		proxy := &httputil.ReverseProxy{
			Transport: transport,
			Rewrite: func(proxyRequest *httputil.ProxyRequest) {
				proxyRequest.SetURL(target)
				proxyRequest.Out.Host = request.Host
				proxyRequest.Out.Header.Del("Forwarded")
				proxyRequest.Out.Header.Del("X-Forwarded-For")
				proxyRequest.Out.Header.Del("X-Forwarded-Host")
				proxyRequest.Out.Header.Del("X-Forwarded-Proto")
			},
			ErrorHandler: func(output http.ResponseWriter, _ *http.Request, proxyErr error) {
				http.Error(output, "upstream unavailable", http.StatusBadGateway)
			},
		}
		proxy.ServeHTTP(writer, request)
	})
}

func splitHTTPAuthority(authority string) (string, int, error) {
	authority = strings.TrimSpace(authority)
	if authority == "" || strings.ContainsAny(authority, "\r\n\x00") {
		return "", 0, errors.New("invalid HTTP authority")
	}
	host, portText, err := net.SplitHostPort(authority)
	if err != nil {
		if strings.Contains(authority, ":") {
			return "", 0, errors.New("invalid HTTP authority")
		}
		host, portText = authority, "80"
	}
	host = proxynetwork.NormalizeHostname(strings.Trim(host, "[]"))
	if !proxynetwork.ValidHostname(host) || net.ParseIP(host) != nil {
		return "", 0, errors.New("HTTP Host must be a DNS hostname")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, errors.New("invalid HTTP authority port")
	}
	return host, port, nil
}

func sameAuthority(left, right string) bool {
	leftHost, leftPort, leftErr := splitHTTPAuthority(left)
	rightHost, rightPort, rightErr := splitHTTPAuthority(right)
	return leftErr == nil && rightErr == nil && leftHost == rightHost && leftPort == rightPort
}

func (server Server) acceptTLS(ctx context.Context, listener net.Listener, errorsChannel chan<- error) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				errorsChannel <- nil
				return
			}
			errorsChannel <- err
			return
		}
		go server.handleTLS(ctx, connection)
	}
}

func (server Server) handleTLS(ctx context.Context, client net.Conn) {
	defer client.Close()
	dialTimeout := mustDuration(server.Config.DialTimeout)
	idleTimeout := mustDuration(server.Config.IdleTimeout)
	_ = client.SetReadDeadline(time.Now().Add(dialTimeout))
	hostname, preface, err := readTLSHostname(client)
	if err != nil {
		return
	}
	upstream, err := proxynetwork.DialUpstream(ctx, hostname, 443, dialTimeout, server.Config.AllowPrivate, server.Config.AllowedDomains)
	if err != nil {
		return
	}
	defer upstream.Close()
	_ = client.SetDeadline(time.Time{})
	if err := writeWithDeadline(upstream, preface, idleTimeout); err != nil {
		return
	}
	tunnel(client, client, upstream, idleTimeout)
}

func readTLSHostname(connection net.Conn) (string, []byte, error) {
	var raw bytes.Buffer
	var handshake bytes.Buffer
	for raw.Len() < maximumPreface {
		header := make([]byte, 5)
		if _, err := io.ReadFull(connection, header); err != nil {
			return "", nil, err
		}
		length := int(binary.BigEndian.Uint16(header[3:5]))
		if length < 1 || length > maximumTLSRecord || raw.Len()+5+length > maximumPreface {
			return "", nil, errors.New("invalid TLS record length")
		}
		if header[0] != 22 {
			return "", nil, errors.New("first TLS flight is not a handshake")
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(connection, payload); err != nil {
			return "", nil, err
		}
		raw.Write(header)
		raw.Write(payload)
		handshake.Write(payload)
		hostname, err := parseClientHelloSNI(handshake.Bytes())
		if err == nil {
			return hostname, raw.Bytes(), nil
		}
		if !errors.Is(err, errNeedMoreTLSData) {
			return "", nil, err
		}
	}
	return "", nil, errors.New("TLS ClientHello exceeds inspection limit")
}

type cursor struct {
	data   []byte
	offset int
}

func (reader *cursor) remaining() int { return len(reader.data) - reader.offset }

func (reader *cursor) readBytes(length int) ([]byte, error) {
	if length < 0 || length > reader.remaining() {
		return nil, io.ErrUnexpectedEOF
	}
	value := reader.data[reader.offset : reader.offset+length]
	reader.offset += length
	return value, nil
}

func (reader *cursor) readU8() (byte, error) {
	value, err := reader.readBytes(1)
	if err != nil {
		return 0, err
	}
	return value[0], nil
}

func (reader *cursor) readU16() (int, error) {
	value, err := reader.readBytes(2)
	if err != nil {
		return 0, err
	}
	return int(binary.BigEndian.Uint16(value)), nil
}

func (reader *cursor) readU24() (int, error) {
	value, err := reader.readBytes(3)
	if err != nil {
		return 0, err
	}
	return int(value[0])<<16 | int(value[1])<<8 | int(value[2]), nil
}

func parseClientHelloSNI(data []byte) (string, error) {
	if len(data) < 4 {
		return "", errNeedMoreTLSData
	}
	handshake := &cursor{data: data}
	messageType, _ := handshake.readU8()
	if messageType != 1 {
		return "", errors.New("TLS handshake is not ClientHello")
	}
	handshakeLength, _ := handshake.readU24()
	if handshakeLength > maximumPreface-4 {
		return "", errors.New("ClientHello exceeds inspection limit")
	}
	if handshake.remaining() < handshakeLength {
		return "", errNeedMoreTLSData
	}
	bodyBytes, _ := handshake.readBytes(handshakeLength)
	body := &cursor{data: bodyBytes}
	if _, err := body.readBytes(2 + 32); err != nil {
		return "", errors.New("ClientHello is truncated")
	}
	sessionLength, err := body.readU8()
	if err != nil {
		return "", errors.New("ClientHello session is truncated")
	}
	if _, err := body.readBytes(int(sessionLength)); err != nil {
		return "", errors.New("ClientHello session is truncated")
	}
	cipherLength, err := body.readU16()
	if err != nil || cipherLength < 2 || cipherLength%2 != 0 {
		return "", errors.New("ClientHello cipher list is invalid")
	}
	if _, err := body.readBytes(cipherLength); err != nil {
		return "", errors.New("ClientHello ciphers are truncated")
	}
	compressionLength, err := body.readU8()
	if err != nil || compressionLength < 1 {
		return "", errors.New("ClientHello compression list is invalid")
	}
	if _, err := body.readBytes(int(compressionLength)); err != nil {
		return "", errors.New("ClientHello compression list is truncated")
	}
	if body.remaining() == 0 {
		return "", errors.New("ClientHello does not include SNI")
	}
	extensionsLength, err := body.readU16()
	if err != nil || extensionsLength != body.remaining() {
		return "", errors.New("ClientHello extensions length is invalid")
	}
	extensionsBytes, _ := body.readBytes(extensionsLength)
	extensions := &cursor{data: extensionsBytes}
	hostname := ""
	foundSNI := false
	for extensions.remaining() > 0 {
		extensionType, err := extensions.readU16()
		if err != nil {
			return "", errors.New("ClientHello extension header is truncated")
		}
		extensionLength, err := extensions.readU16()
		if err != nil {
			return "", errors.New("ClientHello extension header is truncated")
		}
		extension, err := extensions.readBytes(extensionLength)
		if err != nil {
			return "", errors.New("invalid ClientHello extension length")
		}
		if extensionType == 0 {
			if foundSNI {
				return "", errors.New("duplicate ClientHello SNI extension")
			}
			foundSNI = true
			hostname, err = parseSNIExtension(extension)
			if err != nil {
				return "", err
			}
		}
	}
	if !foundSNI {
		return "", errors.New("ClientHello does not include SNI")
	}
	return hostname, nil
}

func parseSNIExtension(extension []byte) (string, error) {
	reader := &cursor{data: extension}
	listLength, err := reader.readU16()
	if err != nil || listLength != reader.remaining() {
		return "", errors.New("invalid SNI list length")
	}
	listBytes, _ := reader.readBytes(listLength)
	list := &cursor{data: listBytes}
	hostname := ""
	for list.remaining() > 0 {
		nameType, err := list.readU8()
		if err != nil {
			return "", errors.New("SNI name type is truncated")
		}
		nameLength, err := list.readU16()
		if err != nil || nameLength < 1 {
			return "", errors.New("invalid SNI name length")
		}
		name, err := list.readBytes(nameLength)
		if err != nil {
			return "", errors.New("invalid SNI name length")
		}
		if nameType == 0 {
			if hostname != "" {
				return "", errors.New("duplicate SNI host_name")
			}
			hostname = proxynetwork.NormalizeHostname(string(name))
			if !proxynetwork.ValidHostname(hostname) || net.ParseIP(hostname) != nil {
				return "", errors.New("invalid SNI hostname")
			}
		}
	}
	if hostname == "" {
		return "", errors.New("SNI host_name is missing")
	}
	return hostname, nil
}

func tunnel(client net.Conn, clientReader io.Reader, upstream net.Conn, idleTimeout time.Duration) {
	var wait sync.WaitGroup
	wait.Add(2)
	abort := func() {
		deadline := time.Now()
		_ = client.SetDeadline(deadline)
		_ = upstream.SetDeadline(deadline)
	}
	copySide := func(destination net.Conn, sourceConnection net.Conn, source io.Reader) {
		defer wait.Done()
		buffer := make([]byte, 32<<10)
		for {
			_ = sourceConnection.SetReadDeadline(time.Now().Add(idleTimeout))
			read, readErr := source.Read(buffer)
			if read > 0 {
				_ = destination.SetWriteDeadline(time.Now().Add(idleTimeout))
				if _, writeErr := destination.Write(buffer[:read]); writeErr != nil {
					abort()
					return
				}
			}
			if readErr != nil {
				if tcp, ok := destination.(*net.TCPConn); ok {
					_ = tcp.CloseWrite()
				}
				if !errors.Is(readErr, io.EOF) {
					abort()
				}
				return
			}
		}
	}
	go copySide(upstream, client, clientReader)
	go copySide(client, upstream, upstream)
	wait.Wait()
}

func writeWithDeadline(connection net.Conn, data []byte, timeout time.Duration) error {
	_ = connection.SetWriteDeadline(time.Now().Add(timeout))
	for len(data) > 0 {
		written, err := connection.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrNoProgress
		}
		data = data[written:]
	}
	return nil
}

func mustDuration(value string) time.Duration {
	duration, _ := time.ParseDuration(value)
	return duration
}

type connectionLimiter struct {
	mu       sync.Mutex
	total    int
	perIP    map[string]int
	maxTotal int
	maxPerIP int
}

func newConnectionLimiter(maxTotal, maxPerIP int) *connectionLimiter {
	return &connectionLimiter{perIP: make(map[string]int), maxTotal: maxTotal, maxPerIP: maxPerIP}
}

func (limiter *connectionLimiter) acquire(address net.Addr) (string, bool) {
	host, _, err := net.SplitHostPort(address.String())
	if err != nil {
		host = address.String()
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if limiter.total >= limiter.maxTotal || limiter.perIP[host] >= limiter.maxPerIP {
		return host, false
	}
	limiter.total++
	limiter.perIP[host]++
	return host, true
}

func (limiter *connectionLimiter) release(host string) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if limiter.total > 0 {
		limiter.total--
	}
	if limiter.perIP[host] <= 1 {
		delete(limiter.perIP, host)
	} else {
		limiter.perIP[host]--
	}
}

type limitedListener struct {
	net.Listener
	limiter *connectionLimiter
}

func (listener *limitedListener) Accept() (net.Conn, error) {
	for {
		connection, err := listener.Listener.Accept()
		if err != nil {
			return nil, err
		}
		host, accepted := listener.limiter.acquire(connection.RemoteAddr())
		if !accepted {
			_ = connection.Close()
			continue
		}
		return &limitedConn{Conn: connection, release: func() { listener.limiter.release(host) }}, nil
	}
}

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (connection *limitedConn) Close() error {
	err := connection.Conn.Close()
	connection.once.Do(connection.release)
	return err
}
