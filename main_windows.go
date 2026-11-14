package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "-proxy-watch" {
		if err := proxyWatch(os.Stdin, os.Stdout, queryProxySettings, writeProxySettings); err != nil {
			log.Print(err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 1 {
		if err := runDesktop(); err != nil {
			log.Print(err)
			path := ""
			if logFile != nil {
				path = logFile.Name()
			}
			showStartupError(err, path)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		log.Print(err)
		if len(os.Args) > 1 && os.Args[1] == "-desktop-worker" {
			sendWorkerEvent(workerEvent{Kind: "error", Value: err.Error()})
		}
		os.Exit(1)
	}
}

func run() error {
	root, err := appDirectory()
	if err != nil {
		return err
	}
	assets := filepath.Join(root, "runtime", appVersion)
	logs := filepath.Join(root, "logs")
	rulesPath := flag.String("rules", filepath.Join(root, "rules.json"), "local rules JSON")
	bypassPath := flag.String("bypass", filepath.Join(root, "bypass.json"), "website and IP bypass entries")
	builtinPath := flag.String("builtin-rules", filepath.Join(root, "builtin-rules.json"), "built-in rule overrides")
	geoDir := flag.String("geo", assets, "directory containing CN rule lists")
	dllPath := flag.String("sdk", filepath.Join(assets, "URnetworkSdk.dll"), "path to the bundled SDK DLL")
	locationID := flag.String("location-id", "", "optional URnetwork location UUID")
	providerID := flag.String("provider-id", "", "optional URnetwork provider client UUID")
	noUR := flag.Bool("offline", false, "test local rules with URnetwork routes returning an error")
	checkSDK := flag.Bool("check-sdk", false, "check SDK exports and initialize a separate in-memory network space, then exit")
	checkAPI := flag.Bool("check-api", false, "check the public URnetwork HTTPS endpoint without creating an account")
	licenses := flag.Bool("sdk-licenses", false, "print bundled SDK notices and exit")
	checkHost := flag.String("check-host", "", "show the route for a host or IP, then exit")
	checkProfile := flag.String("profile", "auto", "profile to use with -check-host")
	verbose := flag.Bool("verbose", false, "print only destination and matched route for each request")
	launch := flag.Bool("launch", false, "open a separate browser profile through the automatic proxy")
	systemProxy := flag.Bool("system-proxy", false, "temporarily use the Windows system proxy")
	openLogs := flag.Bool("logs", false, "open the log folder")
	desktopWorker := flag.Bool("desktop-worker", false, "run the desktop proxy engine")
	flag.Parse()
	if *openLogs {
		return openLogFolder(logs)
	}
	logPath, err := startLog(logs)
	if err != nil {
		return fmt.Errorf("无法打开日志文件：%w", err)
	}
	log.Printf("URnetwork 一键代理 v%s，Windows %s。日志：%s", appVersion, runtime.GOARCH, logPath)
	if *desktopWorker {
		sendWorkerEvent(workerEvent{Kind: "log", Value: logPath})
	}
	if *checkAPI {
		if err := newAuthorization().check(); err != nil {
			return err
		}
		log.Print("URnetwork 官方 HTTPS 接口可达。")
		return nil
	}
	if *dllPath == filepath.Join(assets, "URnetworkSdk.dll") && (!*noUR && *checkHost == "" || *checkSDK || *licenses) {
		if err := extractFile(assets, "URnetworkSdk.dll", false); err != nil {
			return fmt.Errorf("解包 URnetwork SDK：%w", err)
		}
	}
	if *licenses {
		s, err := loadSDK(*dllPath)
		if err != nil {
			return err
		}
		notices, err := s.licenses()
		if err != nil {
			return err
		}
		fmt.Println(notices)
		return nil
	}
	if *locationID != "" && *providerID != "" {
		return errors.New("choose either -location-id or -provider-id")
	}
	for _, id := range []string{*locationID, *providerID} {
		if id != "" && !validID(id) {
			return errors.New("location and provider IDs must be UUIDs")
		}
	}
	if *checkSDK {
		s, err := loadSDK(*dllPath)
		if err != nil {
			return err
		}
		defer s.close()
		if err := s.init(); err != nil {
			return err
		}
		if !validID(s.newID()) {
			return errors.New("SDK returned an invalid instance ID")
		}
		log.Printf("SDK exports and in-memory network space verified (Windows %s).", runtime.GOARCH)
		return nil
	}
	if *geoDir == assets {
		if err := prepareRuleFiles(assets); err != nil {
			return err
		}
	}
	if *rulesPath == filepath.Join(root, "rules.json") {
		if err := extractFile(root, "rules.json", true); err != nil {
			return fmt.Errorf("保存默认分流规则：%w", err)
		}
	}
	geo, err := loadGeo(*geoDir)
	if err != nil {
		return err
	}
	initial, snapshot, err := readDesktopPolicy(*rulesPath, *bypassPath, *builtinPath, geo)
	if err != nil {
		return fmt.Errorf("read proxy rules: %w", err)
	}
	if *checkHost != "" {
		if _, ok := initial.profiles[*checkProfile]; !ok {
			return errors.New("unknown proxy profile")
		}
		host, _, err := destination(*checkHost, "443")
		if err != nil {
			return err
		}
		route, reason := initial.choose(*checkProfile, host)
		log.Printf("%s: %s (%s)", host, route, reason)
		return nil
	}

	if *systemProxy && (*noUR || *launch) {
		return errors.New("-system-proxy 不能和 -offline 或 -launch 一起使用")
	}
	listeners := make(map[string]net.Listener, len(initial.profiles))
	defer func() {
		for _, l := range listeners {
			_ = l.Close()
		}
	}()
	for name, profile := range initial.profiles {
		l, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", profile.port))
		if err != nil {
			return fmt.Errorf("无法监听 %s 端口 %d，请检查旧版代理是否还在运行：%w", name, profile.port, err)
		}
		listeners[name] = l
	}

	var upstream dialFunc
	var connection *sdk
	if !*noUR {
		s, err := loadSDK(*dllPath)
		if err != nil {
			return err
		}
		defer s.close()
		state, err := loadProvisionedSession(newAuthorization(), s.newID)
		if err != nil {
			if *desktopWorker && errors.Is(err, errGuestCancelled) {
				sendWorkerEvent(workerEvent{Kind: "error", Value: err.Error()})
				return nil
			}
			return err
		}
		log.Print("设备授权已就绪，正在初始化 URnetwork。")
		if err := s.init(); err != nil {
			return err
		}
		if err := s.start(state.JWT, state.Instance, *locationID, *providerID); err != nil {
			return err
		}
		log.Print("已请求 URnetwork 连接，首次访问外网时可能需要等待节点建立连接。")
		upstream = s.dial
		connection = s
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	var active activePolicy
	active.current.Store(initial)
	go watchPolicy(ctx, func() (*policy, []byte, error) {
		return readDesktopPolicy(*rulesPath, *bypassPath, *builtinPath, geo)
	}, &active, snapshot)
	var traffic trafficCounters
	ordinary := traffic.count((&net.Dialer{Timeout: 12 * time.Second, KeepAlive: 30 * time.Second}).DialContext)
	upstream = traffic.count(upstream)
	var servers []*http.Server
	defer func() {
		for _, server := range servers {
			_ = server.Close()
		}
		active.closeTunnels("")
	}()
	for name, l := range listeners {
		server := &http.Server{
			Handler:           proxy{name: name, policy: &active, direct: ordinary, upstream: upstream, verbose: *verbose},
			ReadHeaderTimeout: 8 * time.Second,
			IdleTimeout:       45 * time.Second,
			MaxHeaderBytes:    64 << 10,
		}
		servers = append(servers, server)
		go func() {
			if err := server.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Print(err)
				cancel()
			}
		}()
		log.Printf("%s: http://%s", name, l.Addr())
	}
	if *systemProxy {
		if listeners["auto"] == nil {
			return errors.New("启用系统代理需要 auto 分流端口")
		}
		address := listeners["auto"].Addr().String()
		restore, err := enableSystemProxy(address)
		if err != nil {
			return err
		}
		defer func() {
			if err := restore(); err != nil {
				log.Printf("恢复系统代理失败，请检查 Windows 的代理设置：%v", err)
			}
		}()
		log.Printf("已启用 Windows 系统代理 %s，可以使用原来的浏览器。", address)
	} else if *launch {
		if err := launchBrowser(listeners["auto"]); err != nil {
			return err
		}
		log.Print("已打开独立代理浏览器。")
	}
	if *desktopWorker {
		if listeners["auto"] == nil {
			return errors.New("桌面版需要 auto 分流端口")
		}
		address := listeners["auto"].Addr().String()
		sendWorkerEvent(workerEvent{Kind: "ready", Value: address})
		go reportTraffic(ctx, &traffic)
		go readWorkerCommands(ctx, cancel, bufio.NewScanner(os.Stdin), connection, &active, address)
	}
	<-ctx.Done()
	log.Print("正在退出代理。")
	return nil
}

func reloadRules(ctx context.Context, path string, geo *policy, active *activePolicy, snapshot []byte) {
	watchPolicy(ctx, func() (*policy, []byte, error) { return readPolicy(path, geo) }, active, snapshot)
}

func watchPolicy(ctx context.Context, read func() (*policy, []byte, error), active *activePolicy, snapshot []byte) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	lastError := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			value, data, err := read()
			if err == nil && !sameListeners(active.current.Load(), value) {
				err = errors.New("profile names or ports changed; restart to activate those changes")
			}
			if err != nil {
				if err.Error() != lastError {
					log.Printf("Rules not applied: %v", err)
					lastError = err.Error()
				}
				continue
			}
			lastError = ""
			if !bytes.Equal(snapshot, data) {
				active.apply(value)
				snapshot = data
				log.Print("Rules reloaded for new requests.")
			}
		}
	}
}

func validID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range strings.ToLower(s) {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
