package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type workerEvent struct {
	Kind    string
	Value   string
	Exit    *exitObservation `json:",omitempty"`
	Traffic *trafficSample   `json:",omitempty"`
}

type exitObservation struct {
	IP, Country string
}

func reportTraffic(ctx context.Context, traffic *trafficCounters) {
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	last := time.Now()
	var previous trafficSample
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-timer.C:
			sample := traffic.sample(previous, now.Sub(last))
			sendWorkerEvent(workerEvent{Kind: "traffic", Traffic: &sample})
			last, previous = now, sample
		}
	}
}

type workerCommand struct {
	Kind string
	ID   string
}

var workerOutput sync.Mutex

func sendWorkerEvent(event workerEvent) {
	workerOutput.Lock()
	defer workerOutput.Unlock()
	_ = json.NewEncoder(os.Stdout).Encode(event)
}

func readWorkerCommands(ctx context.Context, stop context.CancelFunc, lines *bufio.Scanner, connection *sdk, active *activePolicy, address string) {
	defer stop()
	var restore func() error
	defer func() {
		if restore != nil {
			if err := restore(); err != nil {
				log.Printf("退出时恢复 Windows 代理失败：%v", err)
			}
		}
	}()
	for lines.Scan() {
		var command workerCommand
		if json.Unmarshal(lines.Bytes(), &command) != nil {
			continue
		}
		switch command.Kind {
		case "stop":
			return
		case "system-on":
			if restore == nil {
				var err error
				restore, err = enableSystemProxy(address)
				if err != nil {
					sendWorkerEvent(workerEvent{Kind: "system-error", Value: err.Error()})
					continue
				}
				log.Printf("已启用 Windows 系统代理 %s。", address)
			}
			sendWorkerEvent(workerEvent{Kind: "system-on"})
		case "system-off":
			if restore != nil {
				err := restore()
				restore = nil
				if err != nil {
					sendWorkerEvent(workerEvent{Kind: "system-error", Value: "恢复系统代理失败，请查看日志和 Windows 代理设置"})
					continue
				}
			}
			sendWorkerEvent(workerEvent{Kind: "system-off"})
		case "location", "reconnect":
			if connection == nil || (command.ID != "" && !validID(command.ID)) {
				sendWorkerEvent(workerEvent{Kind: "location-error", Value: "无法应用这个出口位置"})
				continue
			}
			if err := connection.setLocation(command.ID, "", command.Kind == "reconnect"); err != nil {
				sendWorkerEvent(workerEvent{Kind: "location-error", Value: err.Error()})
				continue
			}
			active.exitGeneration.Add(1)
			active.closeTunnels(ur)
			log.Printf("已请求切换代理出口，地区标识 %q。", command.ID)
			sendWorkerEvent(workerEvent{Kind: "location", Value: command.ID})
		case "probe":
			go func() {
				if connection == nil {
					sendWorkerEvent(workerEvent{Kind: "probe-error", Value: "URnetwork 代理尚未就绪"})
					return
				}
				exit, err := probeExit(ctx, connection.dial)
				if err != nil {
					log.Printf("出口检测失败：%v", err)
					sendWorkerEvent(workerEvent{Kind: "probe-error", Value: "出口检测失败，请稍后重试或查看日志"})
					return
				}
				log.Printf("出口检测地区：%s", exit.Country)
				sendWorkerEvent(workerEvent{Kind: "probe", Exit: &exit})
			}()
		case "browser":
			if err := launchBrowserAt(address); err != nil {
				sendWorkerEvent(workerEvent{Kind: "error", Value: err.Error()})
			}
		}
	}
}

func probeExit(ctx context.Context, dial dialFunc) (exitObservation, error) {
	ctx, cancel := context.WithTimeout(ctx, 29*time.Second)
	defer cancel()
	var lastErr error
	for _, url := range []string{"https://www.cloudflare.com/cdn-cgi/trace", "https://1.1.1.1/cdn-cgi/trace"} {
		attempt, finish := context.WithTimeout(ctx, 14*time.Second)
		exit, err := probeExitURL(attempt, dial, url)
		finish()
		if err == nil {
			return exit, nil
		}
		lastErr = err
	}
	return exitObservation{}, lastErr
}

func probeExitURL(ctx context.Context, dial dialFunc, url string) (exitObservation, error) {
	transport := &http.Transport{DialContext: dial, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		Timeout:       28 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return exitObservation{}, err
	}
	res, err := client.Do(req)
	if err != nil {
		return exitObservation{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return exitObservation{}, fmt.Errorf("出口检测返回 HTTP %d", res.StatusCode)
	}
	return parseExitTrace(res.Body)
}

func parseExitTrace(body io.Reader) (exitObservation, error) {
	var exit exitObservation
	lines := bufio.NewScanner(io.LimitReader(body, 8192))
	for lines.Scan() {
		key, value, _ := strings.Cut(lines.Text(), "=")
		switch key {
		case "ip":
			if ip, err := netip.ParseAddr(value); err == nil && ip.IsGlobalUnicast() && !ip.IsPrivate() && ip.Zone() == "" {
				exit.IP = ip.Unmap().String()
			}
		case "loc":
			if code := strings.ToUpper(value); validCountryCode(code) && code != "XX" {
				exit.Country = code
			}
		}
	}
	if err := lines.Err(); err != nil {
		return exitObservation{}, err
	}
	if exit.IP == "" {
		return exitObservation{}, errors.New("检测站点未返回有效的出口 IP")
	}
	return exit, nil
}

type proxyWorker struct {
	cmd   *exec.Cmd
	input io.WriteCloser
}

func startWorker(location string, post func(workerEvent)) (*proxyWorker, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	args := []string{"-desktop-worker"}
	if location != "" {
		args = append(args, "-location-id", location)
	}
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	if logFile != nil {
		cmd.Stderr = logFile
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		return nil, err
	}
	go func() {
		lines := bufio.NewScanner(output)
		for lines.Scan() {
			var event workerEvent
			if json.Unmarshal(lines.Bytes(), &event) == nil {
				post(event)
			}
		}
		event := workerEvent{Kind: "exit"}
		if err := cmd.Wait(); err != nil {
			event.Value = err.Error()
		}
		post(event)
	}()
	return &proxyWorker{cmd, input}, nil
}

func (w *proxyWorker) send(kind, id string) error {
	return json.NewEncoder(w.input).Encode(workerCommand{Kind: kind, ID: id})
}
