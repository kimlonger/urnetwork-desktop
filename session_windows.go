package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

type session struct {
	JWT               string `json:"jwt"`
	Instance          string `json:"instance"`
	PendingNetworkJWT string `json:"pending_network_jwt,omitempty"`
}

type dataBlob struct {
	length uint32
	data   *byte
}

var (
	crypt32       = syscall.NewLazyDLL("crypt32.dll")
	kernel32      = syscall.NewLazyDLL("kernel32.dll")
	protectData   = crypt32.NewProc("CryptProtectData")
	unprotectData = crypt32.NewProc("CryptUnprotectData")
	localFree     = kernel32.NewProc("LocalFree")
	moveFileEx    = kernel32.NewProc("MoveFileExW")
)

func crypt(proc *syscall.LazyProc, in []byte) ([]byte, error) {
	if len(in) == 0 || len(in) > 1<<20 {
		return nil, errors.New("invalid session file length")
	}
	input := dataBlob{uint32(len(in)), &in[0]}
	var output dataBlob
	ok, _, err := proc.Call(uintptr(unsafe.Pointer(&input)), 0, 0, 0, 0, 1, uintptr(unsafe.Pointer(&output)))
	if ok == 0 {
		return nil, err
	}
	defer localFree.Call(uintptr(unsafe.Pointer(output.data)))
	if output.data == nil || output.length > 1<<20 {
		return nil, errors.New("invalid encrypted session data")
	}
	return append([]byte(nil), unsafe.Slice(output.data, int(output.length))...), nil
}

func loadSession(path string) (session, error) {
	var value session
	f, err := os.Open(path)
	if err != nil {
		return value, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return value, err
	}
	plain, err := crypt(unprotectData, data)
	if err != nil {
		return value, err
	}
	defer clear(plain)
	if json.Unmarshal(plain, &value) != nil || (value.JWT == "" && value.PendingNetworkJWT == "") || !validID(value.Instance) {
		return session{}, errors.New("invalid session content")
	}
	return value, nil
}

func saveSession(path string, value session) error {
	plain, err := json.Marshal(value)
	if err != nil {
		return err
	}
	defer clear(plain)
	encrypted, err := crypt(protectData, plain)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "session-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(encrypted)
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	from, err := syscall.UTF16PtrFromString(f.Name())
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	ok, _, err := moveFileEx.Call(uintptr(unsafe.Pointer(from)), uintptr(unsafe.Pointer(to)), 1|8)
	if ok == 0 {
		return err
	}
	return nil
}
