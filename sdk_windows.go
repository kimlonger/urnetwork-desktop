package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

type sdk struct {
	procs      map[string]*syscall.Proc
	manager    uintptr
	space      uintptr
	api        uintptr
	device     uintptr
	refreshSub uintptr
	memory     sdkMemoryBudget
}

const mebibyte = 1024 * 1024

type sdkMemoryBudget struct {
	device, process int64
}

// Follow the official Windows client's paired device and process targets.
func windowsSDKMemoryBudget(totalPhysical uint64) sdkMemoryBudget {
	if totalPhysical > 7*1024*mebibyte {
		return sdkMemoryBudget{device: 256 * mebibyte, process: 768 * mebibyte}
	}
	return sdkMemoryBudget{device: 128 * mebibyte, process: 384 * mebibyte}
}

func hostPhysicalMemory() uint64 {
	// MEMORYSTATUSEX has two DWORDs followed by seven DWORDLONGs.
	var status struct {
		size, load uint32
		total      uint64
		remaining  [6]uint64
	}
	status.size = uint32(unsafe.Sizeof(status))
	ok, _, _ := kernel32.NewProc("GlobalMemoryStatusEx").Call(uintptr(unsafe.Pointer(&status)))
	if ok == 0 {
		return 0
	}
	return status.total
}

// The C ABI initializes the full defaults before applying these Go field names.
type sdkClientSettings struct {
	MemoryTargetByteCount  int64
	AllowProvider          bool
	ProvideExtenderEnabled bool
	DefaultProvideExtender bool
	DefaultRouteLocal      bool
	EnableRpc              bool
}

func clientOnlySDKSettings(budget sdkMemoryBudget) []byte {
	settings, _ := json.Marshal(sdkClientSettings{MemoryTargetByteCount: budget.device})
	return append(settings, 0)
}

func loadSDK(path string) (*sdk, error) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return nil, fmt.Errorf("requires a 64-bit Windows SDK")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	pathUTF16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// Resolve dependency DLLs from Windows System32 rather than the working
	// directory or other directories on PATH.
	const searchSystem32 = 0x800
	handle, _, winErr := kernel32.NewProc("LoadLibraryExW").Call(uintptr(unsafe.Pointer(pathUTF16)), 0, searchSystem32)
	if handle == 0 {
		return nil, fmt.Errorf("加载 URnetwork SDK：%w", winErr)
	}
	lib := &syscall.DLL{Name: path, Handle: syscall.Handle(handle)}
	s := &sdk{procs: make(map[string]*syscall.Proc)}
	for _, name := range strings.Fields(`
		free_string release new_id new_network_space_manager_no_storage
		network_space_manager_update_network_space_values network_space_manager_close
		network_space_get_api api_set_by_jwt get_licenses
		set_memory_limit new_device_local device_local_set_connect_location_checked device_local_reconnect_checked device_get_route_local
		device_get_provide_enabled device_get_provide_mode device_get_provide_extender device_local_get_memory_stats
		device_close device_add_jwt_refresh_listener sub_close device_dial conn_read conn_write conn_close
		conn_close_write conn_set_deadline conn_set_read_deadline conn_set_write_deadline`) {
		proc, err := lib.FindProc("urnet_" + name)
		if err != nil {
			_ = lib.Release()
			return nil, fmt.Errorf("SDK interface unavailable: %w", err)
		}
		s.procs[name] = proc
	}
	return s, nil
}

//go:uintptrescapes
func (s *sdk) call(name string, args ...uintptr) uintptr {
	r, _, _ := s.procs[name].Call(args...)
	return r
}

func cString(s string) []byte { return append([]byte(s), 0) }

var (
	cStringLength = kernel32.NewProc("lstrlenA")
	copyMemory    = kernel32.NewProc("RtlMoveMemory")
)

func copyCString(ptr uintptr) string {
	if ptr == 0 {
		return ""
	}
	length, _, _ := cStringLength.Call(ptr)
	if length == 0 || length >= 1<<20 {
		return ""
	}
	data := make([]byte, length)
	copyMemory.Call(uintptr(unsafe.Pointer(&data[0])), ptr, length)
	return string(data)
}

func (s *sdk) init() error {
	s.memory = windowsSDKMemoryBudget(hostPhysicalMemory())
	s.call("set_memory_limit", uintptr(s.memory.process))
	log.Printf("SDK 内存预算：设备 %d MiB，进程 %d MiB。", s.memory.device/mebibyte, s.memory.process/mebibyte)
	s.manager = s.call("new_network_space_manager_no_storage")
	if s.manager == 0 {
		return errors.New("could not initialize URnetwork SDK")
	}
	key := cString(`{"host_name":"bringyour.com","env_name":"main"}`)
	value := cString(`{"bundled":true,"net_expose_server_ips":true,"net_expose_server_host_names":true,"link_host_name":"ur.io"}`)
	s.space = s.call("network_space_manager_update_network_space_values", s.manager, uintptr(unsafe.Pointer(&key[0])), uintptr(unsafe.Pointer(&value[0])))
	runtime.KeepAlive(key)
	runtime.KeepAlive(value)
	if s.space == 0 {
		return errors.New("could not initialize URnetwork network space")
	}
	s.api = s.call("network_space_get_api", s.space)
	if s.api == 0 {
		return errors.New("could not initialize URnetwork API")
	}
	return nil
}

var (
	refreshLock     sync.Mutex
	refreshPath     string
	refreshInstance string
	refreshCallback = syscall.NewCallback(func(_, jwt uintptr) uintptr {
		refreshLock.Lock()
		defer refreshLock.Unlock()
		if refreshPath != "" {
			updated := session{JWT: copyCString(jwt), Instance: refreshInstance}
			if validateDeviceSession(updated) == nil {
				if saveSession(refreshPath, updated) != nil {
					log.Print("Could not save refreshed URnetwork device credential.")
				}
			}
		}
		return 0
	})
)

func (s *sdk) setJWT(jwt string) {
	data := cString(jwt)
	s.call("api_set_by_jwt", s.api, uintptr(unsafe.Pointer(&data[0])))
	runtime.KeepAlive(data)
}

func (s *sdk) licenses() (string, error) {
	app := cString("windows")
	ptr := s.call("get_licenses", uintptr(unsafe.Pointer(&app[0])))
	runtime.KeepAlive(app)
	if ptr == 0 {
		return "", errors.New("SDK license data unavailable")
	}
	defer s.call("free_string", ptr)
	value := copyCString(ptr)
	if !json.Valid([]byte(value)) {
		return "", errors.New("SDK license data is invalid")
	}
	return value, nil
}

func (s *sdk) newID() string {
	ptr := s.call("new_id")
	defer s.call("free_string", ptr)
	return copyCString(ptr)
}

func (s *sdk) start(jwt, instance, locationID, providerID string) error {
	s.setJWT(jwt)
	args := [][]byte{cString(jwt), cString("URnetwork OneClick local proxy"), cString("windows " + runtime.GOARCH), cString(appVersion), cString(instance)}
	settings := clientOnlySDKSettings(s.memory)
	var reason uintptr
	s.device = s.call("new_device_local", s.space,
		uintptr(unsafe.Pointer(&args[0][0])), uintptr(unsafe.Pointer(&args[1][0])),
		uintptr(unsafe.Pointer(&args[2][0])), uintptr(unsafe.Pointer(&args[3][0])),
		uintptr(unsafe.Pointer(&args[4][0])), uintptr(unsafe.Pointer(&settings[0])), uintptr(unsafe.Pointer(&reason)))
	runtime.KeepAlive(args)
	runtime.KeepAlive(settings)
	if reason != 0 {
		s.call("free_string", reason)
	}
	if s.device == 0 {
		return errors.New("无法创建 URnetwork 连接会话")
	}
	if s.call("device_get_provide_mode", s.device) != 0 ||
		s.call("device_get_provide_enabled", s.device)&0xff != 0 ||
		s.call("device_get_provide_extender", s.device)&0xff != 0 ||
		s.call("device_get_route_local", s.device)&0xff != 0 {
		return errors.New("无法确认只使用代理的模式，已停止启动")
	}
	s.logTransportBudget()
	path, err := guestStatePath()
	if err != nil {
		return err
	}
	refreshLock.Lock()
	refreshPath, refreshInstance = path, instance
	refreshLock.Unlock()
	s.refreshSub = s.call("device_add_jwt_refresh_listener", s.device, refreshCallback, 0)
	if s.refreshSub == 0 {
		return errors.New("could not monitor credential renewal")
	}
	log.Print("客户端模式：流量共享已关闭，RPC 和贡献中继已禁用。")
	return s.setLocation(locationID, providerID, false)
}

func (s *sdk) logTransportBudget() {
	ptr := s.call("device_local_get_memory_stats", s.device)
	if ptr == 0 {
		return
	}
	defer s.call("free_string", ptr)
	var stats struct {
		PlatformTransportBudgetTotalByteCount int64
	}
	if json.Unmarshal([]byte(copyCString(ptr)), &stats) == nil && stats.PlatformTransportBudgetTotalByteCount > 0 {
		log.Printf("SDK 实际传输预算：%d MiB。", stats.PlatformTransportBudgetTotalByteCount/mebibyte)
	}
}

func (s *sdk) setLocation(locationID, providerID string, reconnect bool) error {
	choice := map[string]any{"best_available": true}
	if locationID != "" {
		choice = map[string]any{"location_id": locationID}
	}
	if providerID != "" {
		choice = map[string]any{"client_id": providerID}
	}
	encoded, _ := json.Marshal(map[string]any{"connect_location_id": choice})
	data := cString(string(encoded))
	method := "device_local_set_connect_location_checked"
	if reconnect {
		method = "device_local_reconnect_checked"
	}
	var reason uintptr
	ok := s.call(method, s.device, uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(&reason)))
	runtime.KeepAlive(data)
	if reason != 0 {
		log.Printf("SDK 设置出口失败：%s", copyCString(reason))
		s.call("free_string", reason)
	}
	if ok&0xff == 0 {
		return errors.New("无法切换或重连出口，请查看运行日志")
	}
	return nil
}

func (s *sdk) close() {
	if s.refreshSub != 0 {
		s.call("sub_close", s.refreshSub)
		s.call("release", s.refreshSub)
	}
	refreshLock.Lock()
	refreshPath, refreshInstance = "", ""
	refreshLock.Unlock()
	if s.device != 0 {
		s.call("device_close", s.device)
		s.call("release", s.device)
	}
	if s.api != 0 {
		s.call("release", s.api)
	}
	if s.space != 0 {
		s.call("release", s.space)
	}
	if s.manager != 0 {
		s.call("network_space_manager_close", s.manager)
		s.call("release", s.manager)
	}
}

func (s *sdk) dial(ctx context.Context, network, address string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	timeout := 20 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		timeout = min(timeout, time.Until(deadline))
	}
	if timeout <= 0 {
		return nil, context.DeadlineExceeded
	}
	n, addr := cString(network), cString(address)
	start := time.Now()
	var reason uintptr
	handle := s.call("device_dial", s.device, uintptr(unsafe.Pointer(&n[0])), uintptr(unsafe.Pointer(&addr[0])), uintptr(max(1, timeout.Milliseconds())), uintptr(unsafe.Pointer(&reason)))
	runtime.KeepAlive(n)
	runtime.KeepAlive(addr)
	if reason != 0 {
		s.call("free_string", reason)
	}
	if handle == 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if time.Since(start)+time.Millisecond >= timeout {
			return nil, context.DeadlineExceeded
		}
		return nil, errors.New("URnetwork dial failed")
	}
	conn := &sdkConn{s: s, handle: handle, remote: sdkAddr(address)}
	if err := ctx.Err(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

type sdkAddr string

func (sdkAddr) Network() string  { return "tcp" }
func (a sdkAddr) String() string { return string(a) }

type sdkConn struct {
	s      *sdk
	mu     sync.RWMutex
	close  sync.Once
	handle uintptr
	remote sdkAddr
}

func (c *sdkConn) transfer(name string, b []byte, read bool) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.handle == 0 {
		return 0, net.ErrClosed
	}
	var eof byte
	var reason uintptr
	var value uintptr
	length := uintptr(min(len(b), 1<<20))
	if read {
		value = c.s.call(name, c.handle, uintptr(unsafe.Pointer(&b[0])), length, uintptr(unsafe.Pointer(&eof)), uintptr(unsafe.Pointer(&reason)))
	} else {
		value = c.s.call(name, c.handle, uintptr(unsafe.Pointer(&b[0])), length, uintptr(unsafe.Pointer(&reason)))
	}
	n := int(int32(value))
	runtime.KeepAlive(b)
	if reason != 0 || n < 0 {
		if reason != 0 {
			c.s.call("free_string", reason)
		}
		return max(n, 0), errors.New("URnetwork stream I/O failed")
	}
	if eof != 0 {
		return n, io.EOF
	}
	return n, nil
}

func (c *sdkConn) Read(b []byte) (int, error) { return c.transfer("conn_read", b, true) }

func (c *sdkConn) Write(b []byte) (int, error) {
	count := 0
	for count < len(b) {
		n, err := c.transfer("conn_write", b[count:], false)
		count += n
		if err != nil {
			return count, err
		}
		if n == 0 {
			return count, io.ErrShortWrite
		}
	}
	return count, nil
}

func (c *sdkConn) set(name string, millis ...uintptr) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.handle == 0 {
		return net.ErrClosed
	}
	var reason uintptr
	var result uintptr
	if len(millis) == 0 {
		result = c.s.call(name, c.handle, uintptr(unsafe.Pointer(&reason)))
	} else {
		result = c.s.call(name, c.handle, millis[0], uintptr(unsafe.Pointer(&reason)))
	}
	if reason != 0 {
		c.s.call("free_string", reason)
	}
	if result&0xff == 0 {
		return errors.New("URnetwork stream operation failed")
	}
	return nil
}

func (c *sdkConn) CloseWrite() error { return c.set("conn_close_write") }

func (c *sdkConn) Close() error {
	c.close.Do(func() {
		_ = c.set("conn_close")
		c.mu.Lock()
		c.s.call("release", c.handle)
		c.handle = 0
		c.mu.Unlock()
	})
	return nil
}

func (c *sdkConn) LocalAddr() net.Addr  { return sdkAddr("URnetwork") }
func (c *sdkConn) RemoteAddr() net.Addr { return c.remote }

func (c *sdkConn) setDeadline(name string, t time.Time) error {
	var millis int64
	if !t.IsZero() {
		millis = t.UnixMilli()
	}
	return c.set(name, uintptr(millis))
}

func (c *sdkConn) SetDeadline(t time.Time) error { return c.setDeadline("conn_set_deadline", t) }
func (c *sdkConn) SetReadDeadline(t time.Time) error {
	return c.setDeadline("conn_set_read_deadline", t)
}
func (c *sdkConn) SetWriteDeadline(t time.Time) error {
	return c.setDeadline("conn_set_write_deadline", t)
}
