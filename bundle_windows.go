package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var errGuestCancelled = errors.New("已取消访客授权")

type guestProvisioner interface {
	createGuest() (string, error)
	registerGuest(string) (string, error)
}

func guestStatePath() (string, error) {
	root, err := appDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "session.dpapi"), nil
}

func loadProvisionedSession(a *authorization, newID func() string) (session, error) {
	path, err := guestStatePath()
	if err != nil {
		return session{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return session{}, err
	}
	return provisionSession(path, a, newID, askGuestConsent)
}

func provisionSession(path string, provisioner guestProvisioner, newID func() string, consent func() bool) (session, error) {
	state, err := loadSession(path)
	if err == nil && state.JWT != "" {
		log.Print("使用本机已有的 URnetwork 访客身份。")
		return state, validateDeviceSession(state)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return session{}, errors.New("the saved URnetwork guest session could not be read")
	}
	if state.PendingNetworkJWT == "" {
		if !consent() {
			return session{}, errGuestCancelled
		}
		state.Instance = newID()
		if !validID(state.Instance) {
			return session{}, errors.New("invalid SDK instance ID")
		}
		log.Print("正在为这台电脑开通 URnetwork 访客身份，请稍等。")
		state.PendingNetworkJWT, err = provisioner.createGuest()
		if err != nil {
			return session{}, err
		}
		if err := saveSession(path, state); err != nil {
			return session{}, fmt.Errorf("could not save guest setup state: %w", err)
		}
		log.Print("访客身份已开通。")
	}
	log.Print("正在注册本机代理设备。")
	state.JWT, err = provisioner.registerGuest(state.PendingNetworkJWT)
	if err != nil {
		return session{}, err
	}
	state.PendingNetworkJWT = ""
	if err := validateDeviceSession(state); err != nil {
		return session{}, err
	}
	if err := saveSession(path, state); err != nil {
		return session{}, fmt.Errorf("could not save the encrypted device session: %w", err)
	}
	return state, nil
}

func askGuestConsent() bool {
	message := "首次启动会为本机创建一个 URnetwork 访客账号。\n\n服务条款：https://ur.io/terms-of-service\n隐私政策：https://ur.io/privacy-policy\n\n选择「是」表示你已阅读并同意服务条款，程序将自动开通访客身份并启动代理。"
	const yesNoInformationDefaultNo = 0x04 | 0x40 | 0x100
	return messageBox(message, yesNoInformationDefaultNo) == 6
}

func validateDeviceSession(value session) error {
	if !validID(value.Instance) {
		return errors.New("invalid installation identity")
	}
	if deviceID(value.JWT) == "" {
		return errors.New("activation requires a device-scoped URnetwork credential")
	}
	return nil
}

func deviceID(jwt string) string {
	if len(jwt) > 64<<10 {
		return ""
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		ClientID string `json:"client_id"`
	}
	if err != nil || json.Unmarshal(payload, &claims) != nil || !validID(claims.ClientID) {
		return ""
	}
	return claims.ClientID
}

func launchBrowser(listener net.Listener) error {
	if listener == nil {
		return errors.New("the auto profile is required to open the browser")
	}
	return launchBrowserAt(listener.Addr().String())
}

func launchBrowserAt(address string) error {
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		return errors.New("Windows user profile directory is unavailable")
	}
	for _, browser := range []string{"Google/Chrome/Application/chrome.exe", "Microsoft/Edge/Application/msedge.exe"} {
		for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), local} {
			if base == "" {
				continue
			}
			path := filepath.Join(base, filepath.FromSlash(browser))
			if _, err := os.Stat(path); err == nil {
				profileDir := filepath.Join(local, "URnetwork-OneClick", "Browser")
				cmd := exec.Command(path, "--user-data-dir="+profileDir, "--proxy-server=http://"+address, "about:blank")
				if err := cmd.Start(); err != nil {
					return fmt.Errorf("could not open the separate browser: %w", err)
				}
				return cmd.Process.Release()
			}
		}
	}
	return errors.New("Chrome or Edge is required for the one-click browser")
}
