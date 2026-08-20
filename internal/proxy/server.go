package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/CareyChi/sni-proxy/internal/config"
	proxynetwork "github.com/CareyChi/sni-proxy/internal/network"
)

const maximumPreface = 128 << 10

var errNeedMoreTLSData = errors.New("more TLS handshake data is required")

type Server struct {
	Config config.Config
}

func (server Server) Serve(ctx context.Context) error {
	if err := server.Config.Validate(); err != nil {
		return err
	}
	httpListener, err := net.Listen("tcp", net.JoinHostPort(server.Config.ListenAddress, fmt.Sprintf("%d", server.Config.HTTPPort)))
	if err != nil {
		return fmt.Errorf("listen HTTP proxy: %w", err)
	}
	httpsListener, err := net.Listen("tcp", net.JoinHostPort(server.Config.ListenAddress, fmt.Sprintf("%d", server.Config.HTTPSPort)))
	if err != nil {
		httpListener.Close()
		return fmt.Errorf("listen HTTPS proxy: %w", err)
	}
	defer httpListener.Close()
	defer httpsListener.Close()

	errorsChannel := make(chan error, 2)
	go server.accept(ctx, httpListener, false, errorsChannel)
	go server.accept(ctx, httpsListener, true, errorsChannel)
	go func() {
		<-ctx.Done()
		httpListener.Close()
		httpsListener.Close()
	}()

	for completed := 0; completed < 2; completed++ {
		if serveErr := <-errorsChannel; serveErr != nil && ctx.Err() == nil {
			return serveErr
		}
	}
	return ctx.Err()
}

func (server Server) accept(ctx context.Context, listener net.Listener, tlsMode bool, errorsChannel chan<- error) {
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
		go server.handle(ctx, connection, tlsMode)
	}
}

func (server Server) handle(ctx context.Context, client net.Conn, tlsMode bool) {
	defer client.Close()
	dialTimeout, _ := time.ParseDuration(server.Config.DialTimeout)
	idleTimeout, _ := time.ParseDuration(server.Config.IdleTimeout)
	client.SetReadDeadline(time.Now().Add(dialTimeout))

	var hostname string
	var preface []byte
	var clientReader io.Reader = client
	var err error
	if tlsMode {
		hostname, preface, err = readTLSHostname(client)
	} else {
		var reader *bufio.Reader
		hostname, preface, reader, err = readHTTPHostname(client)
		clientReader = reader
	}
	if err != nil {
		return
	}
	port := 80
	if tlsMode {
		port = 443
	}
	upstream, err := proxynetwork.DialUpstream(ctx, hostname, port, dialTimeout, server.Config.AllowPrivate, server.Config.AllowedDomains)
	if err != nil {
		return
	}
	defer upstream.Close()
	client.SetDeadline(time.Time{})
	upstream.SetDeadline(time.Time{})
	if _, err := upstream.Write(preface); err != nil {
		return
	}
	tunnel(client, clientReader, upstream, idleTimeout)
}

func readHTTPHostname(connection net.Conn) (string, []byte, *bufio.Reader, error) {
	reader := bufio.NewReaderSize(connection, 16<<10)
	var preface bytes.Buffer
	host := ""
	for preface.Len() < 64<<10 {
		line, err := reader.ReadString('\n')
		preface.WriteString(line)
		if err != nil {
			return "", nil, reader, err
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			break
		}
		name, value, found := strings.Cut(trimmed, ":")
		if found && strings.EqualFold(strings.TrimSpace(name), "Host") {
			host = strings.TrimSpace(value)
		}
	}
	if host == "" {
		return "", nil, reader, errors.New("HTTP Host header is missing")
	}
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	return strings.Trim(host, "[]"), preface.Bytes(), reader, nil
}

func readTLSHostname(connection net.Conn) (string, []byte, error) {
	var raw bytes.Buffer
	var handshake bytes.Buffer
	for record := 0; record < 4 && raw.Len() < maximumPreface; record++ {
		header := make([]byte, 5)
		if _, err := io.ReadFull(connection, header); err != nil {
			return "", nil, err
		}
		length := int(binary.BigEndian.Uint16(header[3:5]))
		if length < 1 || length > 65535 || raw.Len()+5+length > maximumPreface {
			return "", nil, errors.New("invalid TLS record length")
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(connection, payload); err != nil {
			return "", nil, err
		}
		raw.Write(header)
		raw.Write(payload)
		if header[0] != 22 {
			return "", nil, errors.New("first TLS flight is not a handshake")
		}
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

func parseClientHelloSNI(data []byte) (string, error) {
	if len(data) < 4 {
		return "", errNeedMoreTLSData
	}
	if data[0] != 1 {
		return "", errors.New("TLS handshake is not ClientHello")
	}
	handshakeLength := int(data[1])<<16 | int(data[2])<<8 | int(data[3])
	if len(data) < 4+handshakeLength {
		return "", errNeedMoreTLSData
	}
	body := data[4 : 4+handshakeLength]
	if len(body) < 35 {
		return "", errors.New("ClientHello is truncated")
	}
	offset := 34
	sessionLength := int(body[offset])
	offset += 1 + sessionLength
	if offset+2 > len(body) {
		return "", errors.New("ClientHello session is truncated")
	}
	cipherLength := int(binary.BigEndian.Uint16(body[offset : offset+2]))
	offset += 2 + cipherLength
	if offset+1 > len(body) {
		return "", errors.New("ClientHello ciphers are truncated")
	}
	compressionLength := int(body[offset])
	offset += 1 + compressionLength
	if offset == len(body) {
		return "", errors.New("ClientHello does not include SNI")
	}
	if offset+2 > len(body) {
		return "", errors.New("ClientHello extensions are truncated")
	}
	extensionsLength := int(binary.BigEndian.Uint16(body[offset : offset+2]))
	offset += 2
	if offset+extensionsLength > len(body) {
		return "", errors.New("ClientHello extensions exceed message length")
	}
	end := offset + extensionsLength
	for offset+4 <= end {
		extensionType := binary.BigEndian.Uint16(body[offset : offset+2])
		extensionLength := int(binary.BigEndian.Uint16(body[offset+2 : offset+4]))
		offset += 4
		if offset+extensionLength > end {
			return "", errors.New("invalid ClientHello extension length")
		}
		if extensionType == 0 {
			extension := body[offset : offset+extensionLength]
			if len(extension) < 5 {
				return "", errors.New("invalid SNI extension")
			}
			listLength := int(binary.BigEndian.Uint16(extension[0:2]))
			if listLength+2 > len(extension) {
				return "", errors.New("invalid SNI list length")
			}
			position := 2
			for position+3 <= 2+listLength {
				nameType := extension[position]
				nameLength := int(binary.BigEndian.Uint16(extension[position+1 : position+3]))
				position += 3
				if position+nameLength > len(extension) {
					return "", errors.New("invalid SNI name length")
				}
				if nameType == 0 {
					return strings.ToLower(string(extension[position : position+nameLength])), nil
				}
				position += nameLength
			}
			return "", errors.New("SNI host_name is missing")
		}
		offset += extensionLength
	}
	return "", errors.New("ClientHello does not include SNI")
}

func tunnel(client net.Conn, clientReader io.Reader, upstream net.Conn, idleTimeout time.Duration) {
	var wait sync.WaitGroup
	wait.Add(2)
	copySide := func(destination net.Conn, source io.Reader) {
		defer wait.Done()
		buffer := make([]byte, 32<<10)
		for {
			destination.SetWriteDeadline(time.Now().Add(idleTimeout))
			if sourceConnection, ok := source.(net.Conn); ok {
				sourceConnection.SetReadDeadline(time.Now().Add(idleTimeout))
			}
			read, readErr := source.Read(buffer)
			if read > 0 {
				if _, writeErr := destination.Write(buffer[:read]); writeErr != nil {
					break
				}
			}
			if readErr != nil {
				break
			}
		}
		if tcp, ok := destination.(*net.TCPConn); ok {
			tcp.CloseWrite()
		}
	}
	go copySide(upstream, clientReader)
	go copySide(client, upstream)
	wait.Wait()
}
