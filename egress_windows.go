package main

import (
	"log"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	ipHelper            = syscall.NewLazyDLL(filepath.Join(os.Getenv("SystemRoot"), "System32", "iphlpapi.dll"))
	getIPForwardTable2  = ipHelper.NewProc("GetIpForwardTable2")
	getIPInterfaceEntry = ipHelper.NewProc("GetIpInterfaceEntry")
	getIfEntry2         = ipHelper.NewProc("GetIfEntry2")
	freeMIBTable        = ipHelper.NewProc("FreeMibTable")
)

// Layouts of the 64-bit Windows NetIO structs. Unused fields are padding.
type ipForwardRow struct {
	luid         uint64
	index        uint32
	_            [28]byte // DestinationPrefix.Prefix
	prefixLength uint8
	_            [3]byte
	nextHop      [28]byte // SOCKADDR_INET
	_            [12]byte
	metric       uint32
	_            [16]byte
}

type ipForwardTable struct {
	count uint32
	rows  [1]ipForwardRow
}

type ipInterfaceRow struct {
	family uint16
	luid   uint64
	_      [132]byte
	metric uint32
	_      [16]byte
}

type interfaceRow struct {
	luid        uint64
	index       uint32
	_           [1116]byte
	kind        uint32
	_           [32]byte
	mediaState  uint32
	networkGUID [16]byte
	_           [168]byte
}

type networkUplink struct {
	luid        uint64
	index       uint32
	nextHop     [28]byte
	networkGUID [16]byte
	unavailable bool
}

type sdkEgress struct {
	v4, v6 networkUplink
}

// Select the default route per family, preferring a connected interface
// before comparing the Windows route and interface metrics.
func defaultUplink(family uint16, prior networkUplink) networkUplink {
	const (
		mediaDisconnected = 2
		loopbackInterface = 24
		virtualInterface  = 53
		tunnelInterface   = 131
	)
	var table *ipForwardTable
	if status, _, _ := getIPForwardTable2.Call(uintptr(family), uintptr(unsafe.Pointer(&table))); status == 0 && table != nil {
		defer freeMIBTable.Call(uintptr(unsafe.Pointer(table)))
		var chosen networkUplink
		var chosenMetric uint64
		for _, route := range unsafe.Slice(&table.rows[0], int(table.count)) {
			if route.prefixLength != 0 || route.index == 0 {
				continue
			}
			adapter := interfaceRow{luid: route.luid}
			if status, _, _ := getIfEntry2.Call(uintptr(unsafe.Pointer(&adapter))); status == 0 {
				switch adapter.kind {
				case loopbackInterface, virtualInterface, tunnelInterface:
					continue
				}
			}
			current := networkUplink{
				luid: route.luid, index: route.index, nextHop: route.nextHop,
				networkGUID: adapter.networkGUID, unavailable: adapter.mediaState == mediaDisconnected,
			}
			ipInterface := ipInterfaceRow{family: family, luid: route.luid}
			status, _, _ := getIPInterfaceEntry.Call(uintptr(unsafe.Pointer(&ipInterface)))
			metric := uint64(route.metric)
			if status == 0 {
				metric += uint64(ipInterface.metric)
			}
			if chosen.index == 0 || (chosen.unavailable && !current.unavailable) ||
				(chosen.unavailable == current.unavailable && metric < chosenMetric) {
				chosen, chosenMetric = current, metric
			}
		}
		if chosen.index != 0 {
			return chosen
		}
	}

	// Keep an existing binding during a temporary route loss, provided the
	// index still belongs to the same interface.
	if prior.index != 0 {
		adapter := interfaceRow{index: prior.index}
		if status, _, _ := getIfEntry2.Call(uintptr(unsafe.Pointer(&adapter))); status == 0 && adapter.luid == prior.luid {
			prior.unavailable = true
			return prior
		}
	}
	return networkUplink{}
}

func (s *sdk) refreshEgress() {
	next := sdkEgress{
		v4: defaultUplink(syscall.AF_INET, s.egress.v4),
		v6: defaultUplink(syscall.AF_INET6, s.egress.v6),
	}
	if next == s.egress && s.egressInitialized {
		return
	}
	s.egressInitialized = true
	s.egress = next
	s.call("set_egress_interface_index", uintptr(next.v4.index), uintptr(next.v6.index))
	log.Printf("SDK 出口网卡：IPv4 索引 %d，IPv6 索引 %d（0 表示使用系统路由）。", next.v4.index, next.v6.index)
	if s.device != 0 {
		s.call("device_local_network_changed", s.device)
	}
}
