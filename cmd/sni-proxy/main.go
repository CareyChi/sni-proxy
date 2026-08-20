package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/CareyChi/sni-proxy/internal/config"
	"github.com/CareyChi/sni-proxy/internal/credentials"
	proxynetwork "github.com/CareyChi/sni-proxy/internal/network"
	"github.com/CareyChi/sni-proxy/internal/platform"
	proxyserver "github.com/CareyChi/sni-proxy/internal/proxy"
	"github.com/CareyChi/sni-proxy/internal/service"
	updateclient "github.com/CareyChi/sni-proxy/internal/update"
	webserver "github.com/CareyChi/sni-proxy/internal/web"
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
		"ports": map[string]int{"http": configuration.HTTPPort, "https": configuration.HTTPSPort, "web": configuration.WebPort},
		"service": map[string]any{"running": running, "running_known": runningKnown, "enabled": enabled, "enabled_known": enabledKnown},
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
	for _, address := range proxynetwork.ServerIPs() {
		fmt.Printf("后台地址：http://%s:%d\n", address, configuration.WebPort)
	}
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
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/healthz", configuration.WebPort), nil)
	response, err := http.DefaultClient.Do(request)
	healthy := err == nil && response.StatusCode == http.StatusOK
	if response != nil {
		io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		response.Body.Close()
	}
	if *asJSON {
		json.NewEncoder(os.Stdout).Encode(map[string]any{"healthy": healthy, "web_port": configuration.WebPort})
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
		flags := flag.NewFlagSet("config set-ports", flag.ContinueOnError)
		httpPort := flags.Int("http", 0, "HTTP port")
		httpsPort := flags.Int("https", 0, "HTTPS port")
		webPort := flags.Int("web", 0, "Web port")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		configuration, err := config.Load(paths.ConfigFile())
		if err != nil {
			return err
		}
		configuration.HTTPPort, configuration.HTTPSPort, configuration.WebPort = *httpPort, *httpsPort, *webPort
		return config.Save(paths.ConfigFile(), configuration)
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
		password := flags.String("password", "", "administrator password; empty generates one")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		var generated string
		var err error
		if arguments[0] == "init" {
			generated, err = store.Initialize(*username, *password)
		} else {
			generated, err = store.Reset(*username, *password)
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
	_, manager, detectErr := detectRuntime(paths)
	if detectErr != nil {
		return detectErr
	}
	if err := client.Apply(context.Background(), updateclient.ApplyOptions{Paths: paths, Release: result.Release, Manager: manager}); err != nil {
		return err
	}
	if manager == nil {
		return errors.New("update files installed, but no supported service manager is available for restart")
	}
	restartContext, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := manager.Restart(restartContext); err != nil {
		return fmt.Errorf("update files installed, but service restart failed: %w", err)
	}
	return waitForHealth(paths, 30*time.Second)
}

func waitForHealth(paths config.Paths, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := health(paths, []string{"--timeout", "2s"}); err == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return errors.New("updated service did not become healthy before the deadline; rollback backup was preserved")
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
		return configuration.ListenAddress, true
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
