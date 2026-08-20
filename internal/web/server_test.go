package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CareyChi/sni-proxy/internal/config"
	"github.com/CareyChi/sni-proxy/internal/platform"
)

func TestPrivilegedWebOperationsAreDisabled(t *testing.T) {
	server := testServer(t, false)
	tests := []struct {
		path    string
		handler http.HandlerFunc
	}{
		{"/api/v1/config/ports", server.handlePorts},
		{"/api/v1/service/restart", server.handleService},
		{"/api/v1/update/apply", server.handleUpdateApply},
	}
	for _, test := range tests {
		request := httptest.NewRequest(http.MethodPut, test.path, strings.NewReader(`{}`))
		if strings.Contains(test.path, "/service/") || strings.Contains(test.path, "/update/") {
			request.Method = http.MethodPost
		}
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		test.handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "privileged_operation_disabled") {
			t.Errorf("%s response = %d %s", test.path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestCookieSecureUsesExplicitConfigurationOnly(t *testing.T) {
	secureServer := testServer(t, true)
	secureCookie := login(t, secureServer, "admin", "correct-password", "")
	if !secureCookie.Secure {
		t.Fatal("cookie_secure=true did not produce a Secure cookie")
	}

	insecureServer := testServer(t, false)
	insecureCookie := login(t, insecureServer, "admin", "correct-password", "https")
	if insecureCookie.Secure {
		t.Fatal("untrusted X-Forwarded-Proto enabled the Secure cookie")
	}
}

func TestLoginFailureBackoff(t *testing.T) {
	server := testServer(t, false)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"wrong-password"}`))
	request.RemoteAddr = "192.0.2.1:1234"
	request.Header.Set("Content-Type", "application/json")
	first := httptest.NewRecorder()
	server.handleLogin(first, request)
	if first.Code != http.StatusUnauthorized {
		t.Fatalf("first failure status = %d", first.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"wrong-password"}`))
	request.RemoteAddr = "192.0.2.1:4321"
	request.Header.Set("Content-Type", "application/json")
	second := httptest.NewRecorder()
	server.handleLogin(second, request)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("backoff status = %d, want 429", second.Code)
	}
}

func TestSessionLimit(t *testing.T) {
	server := testServer(t, false)
	server.sessionMu.Lock()
	for index := 0; index < 80; index++ {
		server.sessions[string(rune(index+1))] = session{Username: "admin", ExpiresAt: time.Now().Add(time.Duration(index+1) * time.Minute)}
	}
	server.limitSessionsLocked(63)
	count := len(server.sessions)
	server.sessionMu.Unlock()
	if count != 63 {
		t.Fatalf("session count = %d, want 63", count)
	}
}

func testServer(t *testing.T, cookieSecure bool) *Server {
	t.Helper()
	root := t.TempDir()
	paths := config.Paths{InstallDir: root, ConfigDir: root, DataDir: root, LogDir: root}
	configuration := config.DefaultConfig()
	configuration.CookieSecure = cookieSecure
	server := NewServer(paths, configuration, platform.Info{}, nil, "test")
	if _, err := server.Credentials.Initialize("admin", "correct-password"); err != nil {
		t.Fatal(err)
	}
	return server
}

func login(t *testing.T, server *Server, username, password, forwardedProto string) *http.Cookie {
	t.Helper()
	body := `{"username":"` + username + `","password":"` + password + `"}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
	request.RemoteAddr = "192.0.2.10:1234"
	request.Header.Set("Content-Type", "application/json")
	if forwardedProto != "" {
		request.Header.Set("X-Forwarded-Proto", forwardedProto)
	}
	recorder := httptest.NewRecorder()
	server.handleLogin(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("login status = %d: %s", recorder.Code, recorder.Body.String())
	}
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == sessionCookie {
			return cookie
		}
	}
	t.Fatal("session cookie was not set")
	return nil
}
