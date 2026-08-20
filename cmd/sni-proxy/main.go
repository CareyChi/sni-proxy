package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/CareyChi/sni-proxy/internal/config"
	"github.com/CareyChi/sni-proxy/internal/credentials"
	proxynetwork "github.com/CareyChi/sni-proxy/internal/network"
	"github.com/CareyChi/sni-proxy/internal/platform"
	"github.com/CareyChi/sni-proxy/internal/privilege"
	proxyserver "github.com/CareyChi/sni-proxy/internal/proxy"
	"github.com/CareyChi/sni-proxy/internal/service"
	updateclient "github.com/CareyChi/sni-proxy/internal/update"
	webserver "github.com/CareyChi/sni-proxy/internal/web"
	"golang.org/x/term"
)

var version = "dev"

type exitError struct {
	code    int
	message string
}

func (err exitError) Error() string { return err.message }

func main() {
	if err := run(os.Args[1:]); err != nil {
		var status exitError
		if errors.As(err, &status) {
			if status.message != "" {
				fmt.Fprintln(os.Stderr, status.message)
			}
			os.Exit(status.code)
		}
		fmt.Fprintln(os.Stderr, "错误："+err.Error())
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) == 0 {
		return usageError()
	}
	paths := config.DiscoverPaths()
	switch arguments[0] {
	case "serve":
		return serve(paths, arguments[1:])
	case "info":
		return showInfo(paths, arguments[1:])
	case "health":
		return health(paths, arguments[1:])
	case "version":
		fmt.Println(effectiveVersion(paths))
		return nil
	case "config":
		return configCommand(paths, arguments[1:])
	case "admin":
		return adminCommand(paths, arguments[1:])
	case "network":
		return networkCommand(arguments[1:])
	case "platform":
		return platformCommand(paths, arguments[1:])
	case "service":
		return serviceCommand(paths, arguments[1:])
	case "update":
		return updateCommand(paths, arguments[1:])
	default:
		return usageError()
	}
}

func usageError() error {
	return errors.New("用法：sni-proxy <serve|info|health|version|config|admin|network|platform|service|update>")
}

func serve(paths config.Paths, arguments []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := flags.String("config", paths.ConfigFile(), "configuration file")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	configuration, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	store := credentials.NewStore(paths.DataDir)
	if _, err := store.Username(); err != nil {
		return fmt.Errorf("administrator is not initialized; run sni-proxy admin init: %w", err)
	}
	info, manager, err := detectRuntime(paths)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	web := webserver.NewServer(paths, configuration, info, manager, effectiveVersion(paths))
	proxy := proxyserver.Server{Config: configuration}
	errorsChannel := make(chan error, 2)
	go func() { errorsChannel <- web.Serve(ctx) }()
	go func() { errorsChannel <- proxy.Serve(ctx) }()
	firstError := <-errorsChannel
	cancel()
	<-errorsChannel
	if errors.Is(firstError, context.Canceled) {
		return nil
	}
	return firstError
}

func showInfo(paths config.Paths, arguments []string) error {
	flags := flag.NewFlagSet("info", flag.ContinueOnError)
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	configuration, err := config.Load(paths.ConfigFile())
	if err != nil {
		return err
	}
	info, manager, err := detectRuntime(paths)
	if err != nil {
		return err
	}
	running, enabled := false, false
	runningKnown, enabledKnown := false, false
	if manager != nil {
		running, err = manager.IsRunning(context.Background())
		runningKnown = err == nil
		enabled, err = manager.IsEnabled(context.Background())
		enabledKnown = err == nil
	}
	data := map[string]any{
		"platform": info, "version": effectiveVersion(paths), "addresses": proxynetwork.ServerIPs(),
		"ports":     map[string]int{"http": configuration.HTTPPort, "https": configuration.HTTPSPort, "web": configuration.WebPort},
		"listeners": map[string]string{"proxy": configuration.ProxyListenAddress, "admin": configuration.AdminListenAddress},
		"service":   map[string]any{"running": running, "running_known": runningKnown, "enabled": enabled, "enabled_known": enabledKnown},
	}
	if *asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(data)
	}
	fmt.Printf("系统：%s\n", info.Distribution)
	fmt.Printf("版本：%s\n", info.DistributionVersion)
	fmt.Printf("架构：%s\n", info.Architecture)
	fmt.Printf("Init：%s\n", info.InitSystem)
	fmt.Printf("包管理器：%s\n", info.PackageManager)
	fmt.Printf("安装目录：%s\n", info.InstallDir)
	fmt.Printf("程序版本：%s\n", effectiveVersion(paths))
	fmt.Printf("HTTP/HTTPS/Web：%d/%d/%d\n", configuration.HTTPPort, configuration.HTTPSPort, configuration.WebPort)
	fmt.Printf("后台地址：%s\n", adminURL(configuration))
	return nil
}

func health(paths config.Paths, arguments []string) error {
	flags := flag.NewFlagSet("health", flag.ContinueOnError)
	asJSON := flags.Bool("json", false, "print JSON")
	timeout := flags.Duration("timeout", 5*time.Second, "health timeout")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	configuration, err := config.Load(paths.ConfigFile())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, adminHealthURL(configuration), nil)
	response, err := http.DefaultClient.Do(request)
	healthy := err == nil && response.StatusCode == http.StatusOK
	if response != nil {
		io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		response.Body.Close()
	}
	if *asJSON {
		json.NewEncoder(os.Stdout).Encode(map[string]any{"healthy": healthy, "web_port": configuration.WebPort, "address": adminHealthHost(configuration)})
	} else if healthy {
		fmt.Println("healthy")
	}
	if !healthy {
		return exitError{code: 1, message: "unhealthy"}
	}
	return nil
}

func configCommand(paths config.Paths, arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("用法：sni-proxy config <get|set-ports>")
	}
	switch arguments[0] {
	case "get":
		flags := flag.NewFlagSet("config get", flag.ContinueOnError)
		key := flags.String("key", "", "single key")
		asJSON := flags.Bool("json", false, "print JSON")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		configuration, err := config.Load(paths.ConfigFile())
		if err != nil {
			return err
		}
		if *key != "" {
			value, ok := configValue(configuration, *key)
			if !ok {
				return errors.New("unknown configuration key")
			}
			fmt.Println(value)
			return nil
		}
		if *asJSON || *key == "" {
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(configuration)
		}
	case "set-ports":
		if err := privilege.RequireRoot(); err != nil {
			return err
		}
		flags := flag.NewFlagSet("config set-ports", flag.ContinueOnError)
		httpPort := flags.Int("http", 0, "HTTP port")
		httpsPort := flags.Int("https", 0, "HTTPS port")
		webPort := flags.Int("web", 0, "Web port")
		allowedDomains := flags.String("allow-domains", "", "comma-separated upstream domain allowlist")
		initialize := flags.Bool("initialize", false, "initialize a new configuration without restarting a service")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		configuration, err := config.Load(paths.ConfigFile())
		if err != nil {
			return err
		}
		updated := configuration
		updated.HTTPPort, updated.HTTPSPort, updated.WebPort = *httpPort, *httpsPort, *webPort
		if *allowedDomains != "" {
			updated.AllowedDomains = nil
			for _, domain := range strings.Split(*allowedDomains, ",") {
				updated.AllowedDomains = append(updated.AllowedDomains, strings.TrimSpace(domain))
			}
		}
		if *initialize {
			if _, statErr := os.Stat(paths.ConfigFile()); !errors.Is(statErr, os.ErrNotExist) {
				return errors.New("--initialize is allowed only when the configuration file does not exist")
			}
			return config.Save(paths.ConfigFile(), updated)
		}
		return setPortsTransaction(paths, configuration, updated)
	}
	return errors.New("unknown config operation")
}

func adminCommand(paths config.Paths, arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("用法：sni-proxy admin <init|show-credentials|reset-credentials>")
	}
	store := credentials.NewStore(paths.DataDir)
	switch arguments[0] {
	case "show-credentials":
		username, err := store.Username()
		if err != nil {
			return err
		}
		fmt.Printf("用户名：%s\n密码：不会显示已保存的密码；请在需要时执行重置。\n", username)
		return nil
	case "init", "reset-credentials":
		flags := flag.NewFlagSet("admin "+arguments[0], flag.ContinueOnError)
		username := flags.String("username", "admin", "administrator username")
		password := flags.String("password", "", "administrator password (unsafe: visible in argv)")
		passwordFile := flags.String("password-file", "", "read administrator password from a file")
		passwordStdin := flags.Bool("password-stdin", false, "read administrator password from stdin")
		generate := flags.Bool("generate", false, "generate a random administrator password")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		resolvedPassword, err := readAdminPassword(arguments[0], *password, *passwordFile, *passwordStdin, *generate)
		if err != nil {
			return err
		}
		var generated string
		if arguments[0] == "init" {
			generated, err = store.Initialize(*username, resolvedPassword)
		} else {
			generated, err = store.Reset(*username, resolvedPassword)
		}
		if err != nil {
			return err
		}
		fmt.Printf("用户名：%s\n", *username)
		if generated != "" {
			fmt.Printf("密码：%s\n", generated)
		} else {
			fmt.Println("密码：已更新（不回显）")
		}
		return nil
	}
	return errors.New("unknown admin operation")
}

func networkCommand(arguments []string) error {
	if len(arguments) == 0 || arguments[0] != "check-port" {
		return errors.New("用法：sni-proxy network check-port --port <1-65535> [--host 0.0.0.0] [--json]")
	}
	flags := flag.NewFlagSet("network check-port", flag.ContinueOnError)
	host := flags.String("host", "0.0.0.0", "listen host")
	port := flags.Int("port", 0, "listen port")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	result := proxynetwork.CheckPort(*host, *port)
	if *asJSON {
		json.NewEncoder(os.Stdout).Encode(result)
	} else if result.Available {
		fmt.Printf("端口 %d 可用\n", result.Port)
	} else {
		fmt.Printf("端口 %d 不可用：%s\n", result.Port, result.Reason)
	}
	if !result.Available {
		return exitError{code: 1}
	}
	return nil
}

func platformCommand(paths config.Paths, arguments []string) error {
	flags := flag.NewFlagSet("platform", flag.ContinueOnError)
	field := flags.String("field", "", "single platform field")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	info, _, err := detectRuntime(paths)
	if err != nil {
		return err
	}
	if *field != "" {
		value, ok := platformValue(info, *field)
		if !ok {
			return errors.New("unknown platform field")
		}
		fmt.Println(value)
		return nil
	}
	if *asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(info)
	}
	fmt.Printf("%s %s / %s\n", info.Distribution, info.DistributionVersion, info.InitSystem)
	return nil
}

func serviceCommand(paths config.Paths, arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("用法：sni-proxy service <start|stop|restart|enable|disable|is-running|is-enabled|install|uninstall>")
	}
	_, manager, err := detectRuntime(paths)
	if err != nil {
		return err
	}
	if manager == nil {
		return errors.New("当前环境未检测到受支持的服务管理器")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	switch arguments[0] {
	case "start", "stop", "restart", "enable", "disable", "install", "uninstall":
		if err := privilege.RequireRoot(); err != nil {
			return err
		}
	}
	switch arguments[0] {
	case "start":
		return manager.Start(ctx)
	case "stop":
		return manager.Stop(ctx)
	case "restart":
		return manager.Restart(ctx)
	case "enable":
		return manager.Enable(ctx)
	case "disable":
		return manager.Disable(ctx)
	case "install":
		return manager.Install(ctx)
	case "uninstall":
		return manager.Uninstall(ctx)
	case "is-running":
		value, err := manager.IsRunning(ctx)
		if err != nil {
			return err
		}
		if !value {
			return exitError{code: 3}
		}
		return nil
	case "is-enabled":
		value, err := manager.IsEnabled(ctx)
		if err != nil {
			return err
		}
		if !value {
			return exitError{code: 3}
		}
		return nil
	default:
		return errors.New("unknown service operation")
	}
}

func updateCommand(paths config.Paths, arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("用法：sni-proxy update <check|apply>")
	}
	configuration, err := config.Load(paths.ConfigFile())
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("update "+arguments[0], flag.ContinueOnError)
	channel := flags.String("channel", "stable", "stable or beta")
	requestedVersion := flags.String("version", "", "version required for apply")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	client := updateclient.NewClient(configuration.UpdateRepository)
	result, err := client.Check(context.Background(), effectiveVersion(paths), *channel)
	if err != nil {
		return err
	}
	if arguments[0] == "check" {
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(result)
		}
		fmt.Printf("当前版本：%s\n最新版本：%s\n可更新：%t\n", result.Current, result.Latest, result.Available)
		return nil
	}
	if arguments[0] != "apply" || *requestedVersion == "" || *requestedVersion != result.Latest {
		return errors.New("apply requires --version matching the latest checked release")
	}
	if err := privilege.RequireRoot(); err != nil {
		return err
	}
	_, publicKey, err := config.LoadUpdateTrust(paths.UpdateTrustFile())
	if err != nil {
		return fmt.Errorf("load root-owned update trust policy: %w", err)
	}
	_, manager, detectErr := detectRuntime(paths)
	if detectErr != nil {
		return detectErr
	}
	if err := client.Apply(context.Background(), updateclient.ApplyOptions{Paths: paths, Release: result.Release, Manager: manager, PublicKey: publicKey}); err != nil {
		return err
	}
	return nil
}

func readAdminPassword(operation, inline, passwordFile string, passwordStdin, generate bool) (string, error) {
	modes := 0
	if inline != "" {
		modes++
	}
	if passwordFile != "" {
		modes++
	}
	if passwordStdin {
		modes++
	}
	if generate {
		modes++
	}
	if modes > 1 {
		return "", errors.New("choose exactly one of --password, --password-file, --password-stdin, or --generate")
	}
	if inline != "" {
		fmt.Fprintln(os.Stderr, "警告：--password 会暴露在进程参数和 shell history 中；请优先使用隐藏输入、--password-file 或 --password-stdin。")
		return inline, nil
	}
	if generate {
		return "", nil
	}
	if passwordFile != "" {
		path := filepath.Clean(passwordFile)
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return "", errors.New("password file must be regular and accessible only to its owner")
		}
		file, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer file.Close()
		return readPasswordValue(io.LimitReader(file, 257))
	}
	if passwordStdin {
		return readPasswordValue(io.LimitReader(os.Stdin, 257))
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("no interactive terminal; use --password-file, --password-stdin, or --generate")
	}
	fmt.Fprint(os.Stderr, "请输入管理员密码：")
	first, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "请再次输入管理员密码：")
	second, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(first) != string(second) {
		return "", errors.New("password confirmation does not match")
	}
	if operation == "init" && len(first) == 0 {
		return "", errors.New("empty password is not allowed; use --generate explicitly")
	}
	return string(first), nil
}

func readPasswordValue(reader io.Reader) (string, error) {
	content, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	if len(content) > 256 {
		return "", errors.New("password exceeds 256 bytes")
	}
	value := strings.TrimSuffix(strings.TrimSuffix(string(content), "\n"), "\r")
	if strings.ContainsRune(value, '\x00') {
		return "", errors.New("password contains NUL")
	}
	return value, nil
}

func setPortsTransaction(paths config.Paths, previous, updated config.Config) error {
	if err := updated.Validate(); err != nil {
		return err
	}
	checks := []struct {
		address   string
		old, next int
	}{
		{updated.ProxyListenAddress, previous.HTTPPort, updated.HTTPPort},
		{updated.ProxyListenAddress, previous.HTTPSPort, updated.HTTPSPort},
		{updated.AdminListenAddress, previous.WebPort, updated.WebPort},
	}
	oldPorts := map[int]bool{previous.HTTPPort: true, previous.HTTPSPort: true, previous.WebPort: true}
	for _, candidate := range checks {
		if candidate.old != candidate.next && !oldPorts[candidate.next] {
			check := proxynetwork.CheckPort(candidate.address, candidate.next)
			if !check.Available {
				return fmt.Errorf("port %d is unavailable: %s", candidate.next, check.Reason)
			}
		}
	}
	_, manager, err := detectRuntime(paths)
	if err != nil {
		return err
	}
	if manager == nil {
		return errors.New("a supported service manager is required to change ports transactionally")
	}
	if err := config.Save(paths.ConfigFile(), updated); err != nil {
		return err
	}
	operationContext, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	transactionErr := manager.Restart(operationContext)
	if transactionErr == nil {
		transactionErr = waitForConfigReadiness(operationContext, updated, 30*time.Second)
	}
	if transactionErr == nil {
		return nil
	}
	rollbackErrors := []error{config.Save(paths.ConfigFile(), previous)}
	rollbackContext, rollbackCancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer rollbackCancel()
	if restartErr := manager.Restart(rollbackContext); restartErr != nil {
		rollbackErrors = append(rollbackErrors, restartErr)
	} else if healthErr := waitForConfigReadiness(rollbackContext, previous, 30*time.Second); healthErr != nil {
		rollbackErrors = append(rollbackErrors, healthErr)
	}
	rollbackErr := errors.Join(rollbackErrors...)
	if rollbackErr != nil {
		return fmt.Errorf("port change failed and rollback failed: change=%v; rollback=%v", transactionErr, rollbackErr)
	}
	return fmt.Errorf("port change failed; previous ports were restored: %w", transactionErr)
}

func waitForConfigReadiness(ctx context.Context, configuration config.Config, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := probeConfiguration(ctx, configuration); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("service did not become ready before the deadline")
}

func probeConfiguration(ctx context.Context, configuration config.Config) error {
	requestContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(requestContext, http.MethodGet, adminHealthURL(configuration), nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("admin health returned %s", response.Status)
	}
	proxyHost := dialableHost(configuration.ProxyListenAddress)
	for _, port := range []int{configuration.HTTPPort, configuration.HTTPSPort} {
		connection, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(requestContext, "tcp", net.JoinHostPort(proxyHost, strconv.Itoa(port)))
		if err != nil {
			return err
		}
		connection.Close()
	}
	return nil
}

func dialableHost(host string) string {
	if host == "0.0.0.0" {
		return "127.0.0.1"
	}
	if host == "::" {
		return "::1"
	}
	return host
}

func adminHealthHost(configuration config.Config) string {
	return dialableHost(configuration.AdminListenAddress)
}

func adminHealthURL(configuration config.Config) string {
	return (&url.URL{Scheme: "http", Host: net.JoinHostPort(adminHealthHost(configuration), strconv.Itoa(configuration.WebPort)), Path: "/healthz"}).String()
}

func adminURL(configuration config.Config) string {
	if configuration.AdminPublicURL != "" {
		return configuration.AdminPublicURL
	}
	return (&url.URL{Scheme: "http", Host: net.JoinHostPort(adminHealthHost(configuration), strconv.Itoa(configuration.WebPort))}).String()
}

func detectRuntime(paths config.Paths) (platform.Info, service.Manager, error) {
	info, err := platform.NewDetector().Detect(paths)
	if err != nil {
		return platform.Info{}, nil, err
	}
	manager, managerErr := service.New(service.Options{Platform: info, Paths: paths, ServiceName: "sni-proxy"})
	if errors.Is(managerErr, service.ErrUnsupported) {
		return info, nil, nil
	}
	return info, manager, managerErr
}

func effectiveVersion(paths config.Paths) string {
	if version != "" && version != "dev" {
		return version
	}
	content, err := os.ReadFile(paths.VersionFile())
	if err == nil && strings.TrimSpace(string(content)) != "" {
		return strings.TrimSpace(string(content))
	}
	return "dev"
}

func configValue(configuration config.Config, key string) (string, bool) {
	switch key {
	case "http_port":
		return strconv.Itoa(configuration.HTTPPort), true
	case "https_port":
		return strconv.Itoa(configuration.HTTPSPort), true
	case "web_port":
		return strconv.Itoa(configuration.WebPort), true
	case "listen_address":
		return configuration.ProxyListenAddress, true
	case "proxy_listen_address":
		return configuration.ProxyListenAddress, true
	case "admin_listen_address":
		return configuration.AdminListenAddress, true
	case "cookie_secure":
		return strconv.FormatBool(configuration.CookieSecure), true
	case "update_repository":
		return configuration.UpdateRepository, true
	default:
		return "", false
	}
}

func platformValue(info platform.Info, field string) (string, bool) {
	switch field {
	case "distribution":
		return info.Distribution, true
	case "distribution_id":
		return info.DistributionID, true
	case "distribution_version":
		return info.DistributionVersion, true
	case "architecture":
		return info.Architecture, true
	case "kernel":
		return info.Kernel, true
	case "init_system":
		return string(info.InitSystem), true
	case "package_manager":
		return info.PackageManager, true
	case "install_dir":
		return info.InstallDir, true
	default:
		return "", false
	}
}
