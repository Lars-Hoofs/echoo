// Package netguard is the single source of truth for which destination addresses Echoo's
// outbound connections may not reach: internal networks, unless an owner explicitly allowed it.
package netguard

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"syscall"
	"time"
)

// ErrInternal is returned when a connection targets a blocked address. Its message contains
// neither the address nor any credentials.
var ErrInternal = errors.New("destination address is not allowed (internal network)")

// blockedNets are ranges that netip's predicates do not cover.
var blockedNets = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // CGNAT
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("192.0.0.0/24"),  // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),   // reserved, includes 255.255.255.255
	netip.MustParsePrefix("255.255.255.255/32"),
	netip.MustParsePrefix("64:ff9b::/96"),   // NAT64: a gateway may translate to any IPv4 address
	netip.MustParsePrefix("64:ff9b:1::/48"), // local-use NAT64
	netip.MustParsePrefix("fec0::/10"),      // deprecated site-local, still routed on some networks
}

var (
	sixToFour = netip.MustParsePrefix("2002::/16")
	teredo    = netip.MustParsePrefix("2001::/32")
	v4Compat  = netip.MustParsePrefix("::/96") // deprecated IPv4-compatible addresses
)

// embeddedV4 returns the IPv4 addresses that a tunnelling prefix carries, so that an internal
// address cannot be reached by wrapping it in 6to4 or Teredo.
func embeddedV4(addr netip.Addr) []netip.Addr {
	b := addr.As16()
	switch {
	case v4Compat.Contains(addr):
		return []netip.Addr{netip.AddrFrom4([4]byte(b[12:16]))}
	case sixToFour.Contains(addr):
		return []netip.Addr{netip.AddrFrom4([4]byte(b[2:6]))}
	case teredo.Contains(addr):
		client := [4]byte{b[12] ^ 0xff, b[13] ^ 0xff, b[14] ^ 0xff, b[15] ^ 0xff}
		return []netip.Addr{netip.AddrFrom4([4]byte(b[4:8])), netip.AddrFrom4(client)}
	}
	return nil
}

// IsInternal reports whether addr is loopback, private, link-local (including the cloud
// metadata address 169.254.169.254), CGNAT, unspecified, multicast a documentation, benchmarking or reserved range, NAT64,
// site-local, also as IPv4-mapped or IPv4-compatible IPv6 or wrapped in 6to4 or Teredo.
func IsInternal(addr netip.Addr) bool {
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsInterfaceLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return true
	}
	for _, p := range blockedNets {
		if p.Contains(addr) {
			return true
		}
	}
	return slices.ContainsFunc(embeddedV4(addr), IsInternal)
}

// Dialer returns a dialer that refuses blocked destinations unless allowInternal is set. The
// check runs in Control, on the IP that is actually about to be connected to, so a name that
// resolves to an internal address later (DNS rebinding) is refused as well.
func Dialer(allowInternal bool, timeout time.Duration) *net.Dialer {
	d := &net.Dialer{Timeout: timeout}
	if !allowInternal {
		d.Control = control
	}
	return d
}

func control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("parse dial address: %w", err)
	}
	if IsInternal(ap.Addr()) {
		return ErrInternal
	}
	return nil
}
