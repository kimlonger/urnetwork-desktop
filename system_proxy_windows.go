package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

const (
	perConnectionOption = 75
	proxyFlags          = 1
	proxyServer         = 2
	proxyBypass         = 3
	proxyAutoConfig     = 4
	proxyFlagsUI        = 10
	proxySettingsChange = 95
	proxyRefresh        = 37
	proxyTypeDirect     = 1
	proxyTypeManual     = 2
	proxyTypeAutoConfig = 4
)

var (
	wininet     = syscall.NewLazyDLL("wininet.dll")
	queryOption = wininet.NewProc("InternetQueryOptionW")
	setOption   = wininet.NewProc("InternetSetOptionW")
	globalFree  = kernel32.NewProc("GlobalFree")
)

type connectionOption struct {
	id    uint32
	value uintptr
}

type connectionOptions struct {
	size       uint32
	connection *uint16
	count      uint32
	errorIndex uint32
	options    *connectionOption
}

type proxySettings struct {
	Flags  uint32
	Server string
	Bypass string
	PAC    string
}

func (s proxySettings) matches(active proxySettings) bool {
	return s.Flags == active.Flags &&
		strings.EqualFold(s.Server, active.Server) &&
		strings.EqualFold(s.Bypass, active.Bypass) &&
		(s.Flags&proxyTypeAutoConfig == 0 || s.PAC == active.PAC)
}

func connectionList(options []connectionOption) connectionOptions {
	return connectionOptions{size: uint32(unsafe.Sizeof(connectionOptions{})), count: uint32(len(options)), options: &options[0]}
}

func queryProxySettings() (proxySettings, error) {
	options := []connectionOption{{id: proxyFlagsUI}, {id: proxyServer}, {id: proxyBypass}, {id: proxyAutoConfig}}
	list := connectionList(options)
	size := list.size
	ok, _, winErr := queryOption.Call(0, perConnectionOption, uintptr(unsafe.Pointer(&list)), uintptr(unsafe.Pointer(&size)))
	if ok == 0 {
		return proxySettings{}, winErr
	}
	values := make([]string, 0, 3)
	for _, option := range options[1:] {
		var value []uint16
		if option.value != 0 {
			for i := uintptr(0); i < 32768; i++ {
				ch := *(*uint16)(unsafe.Pointer(option.value + i*2))
				if ch == 0 {
					break
				}
				value = append(value, ch)
			}
			globalFree.Call(option.value)
		}
		values = append(values, syscall.UTF16ToString(value))
	}
	return proxySettings{uint32(options[0].value), values[0], values[1], values[2]}, nil
}

func writeProxySettings(settings proxySettings) error {
	options := []connectionOption{{id: proxyFlags, value: uintptr(settings.Flags)}, {id: proxyServer}, {id: proxyBypass}, {id: proxyAutoConfig}}
	var pins runtime.Pinner
	defer pins.Unpin()
	for i, value := range []string{settings.Server, settings.Bypass, settings.PAC} {
		ptr, err := syscall.UTF16PtrFromString(value)
		if err != nil {
			return err
		}
		pins.Pin(ptr)
		options[i+1].value = uintptr(unsafe.Pointer(ptr))
	}
	list := connectionList(options)
	ok, _, winErr := setOption.Call(0, perConnectionOption, uintptr(unsafe.Pointer(&list)), uintptr(list.size))
	if ok == 0 {
		return winErr
	}
	setOption.Call(0, proxySettingsChange, 0, 0)
	setOption.Call(0, proxyRefresh, 0, 0)
	return nil
}

type proxyRecovery struct {
	Before, Active proxySettings
}

func proxyWatch(reader io.Reader, ready io.Writer, query func() (proxySettings, error), write func(proxySettings) error) error {
	input := bufio.NewReader(reader)
	line, err := input.ReadBytes('\n')
	if err != nil {
		return err
	}
	var change proxyRecovery
	if err := json.Unmarshal(line, &change); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(ready, "ready"); err != nil {
		return err
	}
	// The pipe also closes if Windows terminates the proxy process.
	_, _ = io.Copy(io.Discard, input)
	current, err := query()
	if err != nil {
		return fmt.Errorf("读取系统代理以恢复设置：%w", err)
	}
	if current.matches(change.Active) {
		if err := write(change.Before); err != nil {
			return fmt.Errorf("恢复原系统代理设置：%w", err)
		}
		log.Print("已恢复原系统代理设置。")
	}
	return nil
}

func enableSystemProxy(address string) (restore func() error, err error) {
	before, err := queryProxySettings()
	if err != nil {
		return nil, fmt.Errorf("读取原系统代理设置：%w", err)
	}
	active := proxySettings{Flags: proxyTypeDirect | proxyTypeManual, Server: address, Bypass: "<local>"}
	if before.matches(active) {
		return nil, errors.New("Windows 系统代理已指向这个端口，请先关闭旧代理或清理旧的手动代理设置")
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, "-proxy-watch")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	cmd.Stderr = os.Stderr
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
	restore = func() error {
		_ = input.Close()
		return cmd.Wait()
	}
	if err := json.NewEncoder(input).Encode(proxyRecovery{Before: before, Active: active}); err != nil {
		_ = restore()
		return nil, err
	}
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || line != "ready\n" {
		_ = restore()
		return nil, errors.New("无法启动系统代理恢复程序")
	}
	if err := writeProxySettings(active); err != nil {
		_ = restore()
		return nil, fmt.Errorf("启用 Windows 系统代理：%w", err)
	}
	current, err := queryProxySettings()
	if err != nil || !current.matches(active) {
		_ = restore()
		return nil, errors.New("Windows 未确认系统代理已启用，请检查是否由其他软件或系统策略管理")
	}
	return restore, nil
}
