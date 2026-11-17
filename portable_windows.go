package main

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"
)

const appVersion = "0.6.6"

var logFile *os.File

//go:embed URnetworkSdk.dll rules.json cn-domain.list cn-ip.list
//go:embed README-Start.md THIRD-PARTY-NOTICES.txt SDK-Licenses.json CN-rule-lists-LICENSE.txt
var portableFiles embed.FS

func appDirectory() (string, error) {
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		return "", errors.New("无法找到 Windows 用户数据目录（LOCALAPPDATA）")
	}
	return filepath.Join(local, "URnetwork-OneClick"), nil
}

func extractFile(dir, name string, editable bool) error {
	path := filepath.Join(dir, name)
	if editable {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	data, err := portableFiles.ReadFile(name)
	if err != nil {
		return err
	}
	if current, err := os.Open(path); err == nil {
		sum := sha256.New()
		n, err := io.Copy(sum, io.LimitReader(current, int64(len(data))+1))
		_ = current.Close()
		expected := sha256.Sum256(data)
		if err == nil && n == int64(len(data)) && bytes.Equal(sum.Sum(nil), expected[:]) {
			return nil
		}
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "urnetwork-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, writeErr := tmp.Write(data)
	if err := errors.Join(writeErr, tmp.Close()); err != nil {
		return err
	}
	if !editable {
		return os.Rename(tmp.Name(), path)
	}
	// MoveFile refuses to replace a user rule file created by another launch.
	from, err := syscall.UTF16PtrFromString(tmp.Name())
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	if err := syscall.MoveFile(from, to); !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}

func prepareRuleFiles(assets string) error {
	for _, name := range []string{"cn-domain.list", "cn-ip.list", "README-Start.md", "THIRD-PARTY-NOTICES.txt", "SDK-Licenses.json", "CN-rule-lists-LICENSE.txt"} {
		if err := extractFile(assets, name, false); err != nil {
			return fmt.Errorf("解包 %s：%w", name, err)
		}
	}
	return nil
}

func startLog(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, time.Now().Format("20060102-150405.000000000")+"-*.log")
	if err != nil {
		return "", err
	}
	// The DLL has its own Go runtime, which reads the Windows stderr handle
	// when it initializes. The EXE keeps its original stderr for status lines.
	setStdHandle := kernel32.NewProc("SetStdHandle")
	const stderrHandle = ^uint32(11)
	ok, _, winErr := setStdHandle.Call(uintptr(stderrHandle), f.Fd())
	if ok == 0 {
		_ = f.Close()
		return "", winErr
	}
	logFile = f
	log.SetOutput(f)
	if os.Stderr != nil && os.Stderr.Fd() != 0 {
		log.SetOutput(io.MultiWriter(f, os.Stderr))
	}
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	// Keep the redirected handle open for the SDK until the process exits.
	if names, err := filepath.Glob(filepath.Join(dir, "*.log")); err == nil {
		for _, name := range names[:max(0, len(names)-10)] {
			if name != f.Name() {
				_ = os.Remove(name)
			}
		}
	}
	return f.Name(), nil
}

func openLogFolder(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	explorer := filepath.Join(os.Getenv("SystemRoot"), "explorer.exe")
	cmd := exec.Command(explorer, dir)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func messageBox(message string, flags uintptr) uintptr {
	title := syscall.StringToUTF16Ptr("URnetwork 一键代理")
	content := syscall.StringToUTF16Ptr(message)
	proc := syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW")
	result, _, _ := proc.Call(0, uintptr(unsafe.Pointer(content)), uintptr(unsafe.Pointer(title)), flags)
	return result
}

func showStartupError(err error, path string) {
	message := fmt.Sprintf("启动失败\n\n%s", err)
	if path != "" {
		message += "\n\n日志已保存。是否打开日志文件夹？"
		const yesNoErrorDefaultNo = 0x04 | 0x10 | 0x100
		if messageBox(message, yesNoErrorDefaultNo) == 6 {
			_ = openLogFolder(filepath.Dir(path))
		}
		return
	}
	messageBox(message, 0x10)
}
