package web

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/CareyChi/sni-proxy/internal/config"
	"github.com/CareyChi/sni-proxy/internal/credentials"
	proxynetwork "github.com/CareyChi/sni-proxy/internal/network"
	"github.com/CareyChi/sni-proxy/internal/platform"
	"github.com/CareyChi/sni-proxy/internal/service"
	updateclient "github.com/CareyChi/sni-proxy/internal/update"
)

//go:embed assets/*
var assets embed.FS

const sessionCookie = "sni_proxy_session"

type session struct {
	Username  string
	ExpiresAt time.Time
}

type Server struct {
	Paths       config.Paths
	Credentials credentials.Store
	Platform    platform.Info
	Manager     service.Manager
	Version     string
	StartedAt   time.Time

	configuration config.Config
	configMu      sync.RWMutex
	sessions      map[string]session
	sessionMu     sync.Mutex
}

func NewServer(paths config.Paths, configuration config.Config, info platform.Info, manager service.Manager, version string) *Server {
	return &Server{
		Paths:         paths,
		Credentials:   credentials.NewStore(paths.DataDir),
		Platform:      info,
		Manager:       manager,
		Version:       version,
		StartedAt:     time.Now(),
		configuration: configuration,
		sessions:      make(map[string]session),
	}
}

func (server *Server) Serve(ctx context.Context) error {
	configuration := server.currentConfig()
	httpServer := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", configuration.ListenAddress, configuration.WebPort),
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdownContext)
	}()
	err := httpServer.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (server *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", server.handlePublicHealth)
	mux.HandleFunc("/api/v1/auth/login", server.handleLogin)
	mux.Handle("/api/v1/", server.requireAuthentication(http.HandlerFunc(server.handleAPI)))
	static, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(static))
	mux.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/" && strings.Contains(filepathClean(request.URL.Path), ".") {
			fileServer.ServeHTTP(writer, request)
			return
		}
		request.URL.Path = "/"
		fileServer.ServeHTTP(writer, request)
	})
	return securityHeaders(mux)
}

func (server *Server) handleAPI(writer http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/api/v1/auth/session":
		server.handleSession(writer, request)
	case "/api/v1/auth/logout":
		server.handleLogout(writer, request)
	case "/api/v1/status":
		server.handleStatus(writer, request)
	case "/api/v1/health":
		server.handleHealth(writer, request)
	case "/api/v1/system/info":
		server.handleSystemInfo(writer, request)
	case "/api/v1/config/ports":
		server.handlePorts(writer, request)
	case "/api/v1/network/check-port":
		server.handleCheckPort(writer, request)
	case "/api/v1/admin/credentials":
		server.handleCredentials(writer, request)
	case "/api/v1/update/check":
		server.handleUpdateCheck(writer, request)
	case "/api/v1/update/apply":
		server.handleUpdateApply(writer, request)
	case "/api/v1/service/start", "/api/v1/service/stop", "/api/v1/service/restart", "/api/v1/service/enable", "/api/v1/service/disable":
		server.handleService(writer, request)
	default:
		writeError(writer, http.StatusNotFound, "not_found", "API endpoint not found")
	}
}

func (server *Server) handleLogin(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, http.MethodPost)
		return
	}
	if !isJSON(request) || !sameOrigin(request) {
		writeError(writer, http.StatusBadRequest, "invalid_request", "a same-origin JSON request is required")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	valid, err := server.Credentials.Verify(input.Username, input.Password)
	if err != nil || !valid {
		writeError(writer, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误")
		return
	}
	tokenBytes := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, tokenBytes); err != nil {
		writeError(writer, http.StatusInternalServerError, "session_failed", "无法创建安全会话")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	expires := time.Now().Add(12 * time.Hour)
	server.sessionMu.Lock()
	server.pruneSessionsLocked(time.Now())
	server.sessions[token] = session{Username: input.Username, ExpiresAt: expires}
	server.sessionMu.Unlock()
	http.SetCookie(writer, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: request.TLS != nil, Expires: expires,
	})
	writeData(writer, http.StatusOK, map[string]any{"username": input.Username, "expires_at": expires})
}

func (server *Server) handleSession(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	current, _ := request.Context().Value(sessionContextKey{}).(session)
	writeData(writer, http.StatusOK, map[string]any{"authenticated": true, "username": current.Username, "expires_at": current.ExpiresAt})
}

func (server *Server) handleLogout(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, http.MethodPost)
		return
	}
	if !isJSON(request) || !sameOrigin(request) {
		writeError(writer, http.StatusBadRequest, "invalid_request", "a same-origin JSON request is required")
		return
	}
	if cookie, err := request.Cookie(sessionCookie); err == nil {
		server.sessionMu.Lock()
		delete(server.sessions, cookie.Value)
		server.sessionMu.Unlock()
	}
	http.SetCookie(writer, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	writeData(writer, http.StatusOK, true)
}

func (server *Server) handleStatus(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	running, enabled, runningKnown, enabledKnown := false, false, false, false
	if server.Manager != nil {
		var err error
		running, err = server.Manager.IsRunning(request.Context())
		runningKnown = err == nil
		enabled, err = server.Manager.IsEnabled(request.Context())
		enabledKnown = err == nil
	}
	configuration := server.currentConfig()
	writeData(writer, http.StatusOK, map[string]any{
		"core_running": running, "core_status_known": runningKnown, "web_running": true,
		"autostart_enabled": enabled, "autostart_status_known": enabledKnown,
		"version": server.Version, "http_port": configuration.HTTPPort,
		"https_port": configuration.HTTPSPort, "web_port": configuration.WebPort,
		"install_dir": server.Paths.InstallDir, "system": server.Platform.Distribution,
		"system_version": server.Platform.DistributionVersion, "init_system": server.Platform.InitSystem,
	})
}

func (server *Server) handlePublicHealth(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	writeData(writer, http.StatusOK, map[string]any{"status": "healthy", "version": server.Version})
}

func (server *Server) handleHealth(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	writeData(writer, http.StatusOK, map[string]any{"status": "healthy", "uptime_seconds": int64(time.Since(server.StartedAt).Seconds()), "checked_at": time.Now().UTC()})
}

func (server *Server) handleSystemInfo(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	writeData(writer, http.StatusOK, map[string]any{"platform": server.Platform, "addresses": proxynetwork.ServerIPs()})
}

func (server *Server) handlePorts(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		configuration := server.currentConfig()
		writeData(writer, http.StatusOK, map[string]int{"http_port": configuration.HTTPPort, "https_port": configuration.HTTPSPort, "web_port": configuration.WebPort})
	case http.MethodPut:
		if !isJSON(request) || !sameOrigin(request) {
			writeError(writer, http.StatusBadRequest, "invalid_request", "a same-origin JSON request is required")
			return
		}
		var input struct {
			HTTPPort  int `json:"http_port"`
			HTTPSPort int `json:"https_port"`
			WebPort   int `json:"web_port"`
		}
		if err := decodeJSON(request, &input); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		oldConfiguration := server.currentConfig()
		newConfiguration := oldConfiguration
		newConfiguration.HTTPPort = input.HTTPPort
		newConfiguration.HTTPSPort = input.HTTPSPort
		newConfiguration.WebPort = input.WebPort
		if err := newConfiguration.Validate(); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid_ports", err.Error())
			return
		}
		for _, candidate := range []struct{ old, next int }{{oldConfiguration.HTTPPort, input.HTTPPort}, {oldConfiguration.HTTPSPort, input.HTTPSPort}, {oldConfiguration.WebPort, input.WebPort}} {
			if candidate.old != candidate.next {
				check := proxynetwork.CheckPort(oldConfiguration.ListenAddress, candidate.next)
				if !check.Available {
					writeError(writer, http.StatusConflict, "port_unavailable", fmt.Sprintf("port %d is unavailable: %s", candidate.next, check.Reason))
					return
				}
			}
		}
		if err := config.Save(server.Paths.ConfigFile(), newConfiguration); err != nil {
			writeError(writer, http.StatusInternalServerError, "config_write_failed", err.Error())
			return
		}
		if server.Manager == nil {
			config.Save(server.Paths.ConfigFile(), oldConfiguration)
			writeError(writer, http.StatusServiceUnavailable, "service_manager_unavailable", "未检测到可用的服务管理器")
			return
		}
		server.configMu.Lock()
		server.configuration = newConfiguration
		server.configMu.Unlock()
		if err := server.Manager.Restart(request.Context()); err != nil {
			config.Save(server.Paths.ConfigFile(), oldConfiguration)
			server.configMu.Lock()
			server.configuration = oldConfiguration
			server.configMu.Unlock()
			writeError(writer, http.StatusInternalServerError, "restart_failed", err.Error())
			return
		}
		writeData(writer, http.StatusOK, map[string]any{"http_port": input.HTTPPort, "https_port": input.HTTPSPort, "web_port": input.WebPort, "restart_requested": true})
	default:
		methodNotAllowed(writer, http.MethodGet+", "+http.MethodPut)
	}
}

func (server *Server) handleCheckPort(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, http.MethodPost)
		return
	}
	if !isJSON(request) {
		writeError(writer, http.StatusBadRequest, "invalid_request", "JSON request required")
		return
	}
	var input struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	writeData(writer, http.StatusOK, proxynetwork.CheckPort(input.Host, input.Port))
}

func (server *Server) handleCredentials(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		username, err := server.Credentials.Username()
		if err != nil {
			writeError(writer, http.StatusInternalServerError, "credential_read_failed", err.Error())
			return
		}
		writeData(writer, http.StatusOK, map[string]string{"username": username})
	case http.MethodPut:
		if !isJSON(request) || !sameOrigin(request) {
			writeError(writer, http.StatusBadRequest, "invalid_request", "a same-origin JSON request is required")
			return
		}
		var input struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := decodeJSON(request, &input); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		generated, err := server.Credentials.Reset(input.Username, input.Password)
		if err != nil {
			writeError(writer, http.StatusBadRequest, "credential_reset_failed", err.Error())
			return
		}
		server.sessionMu.Lock()
		server.sessions = make(map[string]session)
		server.sessionMu.Unlock()
		writeData(writer, http.StatusOK, map[string]string{"username": input.Username, "generated_password": generated})
	default:
		methodNotAllowed(writer, http.MethodGet+", "+http.MethodPut)
	}
}

func (server *Server) handleService(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, http.MethodPost)
		return
	}
	if !isJSON(request) || !sameOrigin(request) {
		writeError(writer, http.StatusBadRequest, "invalid_request", "a same-origin JSON request is required")
		return
	}
	if server.Manager == nil {
		writeError(writer, http.StatusServiceUnavailable, "service_manager_unavailable", "当前环境未检测到受支持的服务管理器")
		return
	}
	action := strings.TrimPrefix(request.URL.Path, "/api/v1/service/")
	var err error
	switch action {
	case "start":
		err = server.Manager.Start(request.Context())
	case "stop":
		err = server.Manager.Stop(request.Context())
	case "restart":
		err = server.Manager.Restart(request.Context())
	case "enable":
		err = server.Manager.Enable(request.Context())
	case "disable":
		err = server.Manager.Disable(request.Context())
	default:
		writeError(writer, http.StatusNotFound, "not_found", "unknown service action")
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "service_action_failed", err.Error())
		return
	}
	writeData(writer, http.StatusOK, map[string]any{"action": action, "accepted": true})
}

func (server *Server) handleUpdateCheck(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, http.MethodPost)
		return
	}
	if !isJSON(request) {
		writeError(writer, http.StatusBadRequest, "invalid_request", "JSON request required")
		return
	}
	var input struct{ Channel string `json:"channel"` }
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	client := updateclient.NewClient(server.currentConfig().UpdateRepository)
	result, err := client.Check(request.Context(), server.Version, input.Channel)
	if err != nil {
		writeError(writer, http.StatusBadGateway, "update_check_failed", err.Error())
		return
	}
	writeData(writer, http.StatusOK, result)
}

func (server *Server) handleUpdateApply(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, http.MethodPost)
		return
	}
	if !isJSON(request) || !sameOrigin(request) {
		writeError(writer, http.StatusBadRequest, "invalid_request", "a same-origin JSON request is required")
		return
	}
	var input struct {
		Version string `json:"version"`
		Channel string `json:"channel"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	client := updateclient.NewClient(server.currentConfig().UpdateRepository)
	result, err := client.Check(request.Context(), server.Version, input.Channel)
	if err != nil {
		writeError(writer, http.StatusBadGateway, "update_check_failed", err.Error())
		return
	}
	if input.Version == "" || input.Version != result.Latest {
		writeError(writer, http.StatusConflict, "version_changed", "requested version is not the current release candidate")
		return
	}
	err = client.Apply(request.Context(), updateclient.ApplyOptions{Paths: server.Paths, Release: result.Release, Manager: server.Manager})
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "update_failed", err.Error())
		return
	}
	writeData(writer, http.StatusAccepted, map[string]any{"version": input.Version, "restart_requested": server.Manager != nil})
	if server.Manager != nil {
		go func() {
			time.Sleep(300 * time.Millisecond)
			restartContext, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			server.Manager.Restart(restartContext)
		}()
	}
}

func (server *Server) requireAuthentication(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		cookie, err := request.Cookie(sessionCookie)
		if err != nil {
			writeError(writer, http.StatusUnauthorized, "authentication_required", "请先登录")
			return
		}
		now := time.Now()
		server.sessionMu.Lock()
		current, exists := server.sessions[cookie.Value]
		if exists && now.After(current.ExpiresAt) {
			delete(server.sessions, cookie.Value)
			exists = false
		}
		server.sessionMu.Unlock()
		if !exists {
			writeError(writer, http.StatusUnauthorized, "authentication_required", "会话已失效")
			return
		}
		contextWithSession := context.WithValue(request.Context(), sessionContextKey{}, current)
		next.ServeHTTP(writer, request.WithContext(contextWithSession))
	})
}

type sessionContextKey struct{}

func (server *Server) currentConfig() config.Config {
	server.configMu.RLock()
	defer server.configMu.RUnlock()
	return server.configuration
}

func (server *Server) pruneSessionsLocked(now time.Time) {
	for token, current := range server.sessions {
		if now.After(current.ExpiresAt) {
			delete(server.sessions, token)
		}
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'")
		next.ServeHTTP(writer, request)
	})
}

func sameOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if origin == "" {
		return true
	}
	expectedHTTP := "http://" + request.Host
	expectedHTTPS := "https://" + request.Host
	return origin == expectedHTTP || origin == expectedHTTPS
}

func isJSON(request *http.Request) bool {
	value := request.Header.Get("Content-Type")
	mediaType := strings.TrimSpace(strings.SplitN(value, ";", 2)[0])
	return mediaType == "application/json"
}

func decodeJSON(request *http.Request, destination any) error {
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}

func writeData(writer http.ResponseWriter, status int, data any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	json.NewEncoder(writer).Encode(map[string]any{"data": data, "error": nil})
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	json.NewEncoder(writer).Encode(map[string]any{"data": nil, "error": map[string]string{"code": code, "message": message}})
}

func methodNotAllowed(writer http.ResponseWriter, allowed string) {
	writer.Header().Set("Allow", allowed)
	writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "HTTP method is not allowed")
}

func filepathClean(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	for strings.Contains(path, "//") {
		path = strings.ReplaceAll(path, "//", "/")
	}
	return path
}
