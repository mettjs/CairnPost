package webhook

import (
	"errors"
	"net"
	"net/netip"
	"syscall"
	"time"
)

// errBlocked is an endpoint whose name leads to an address that is not on
// the public internet.
var errBlocked = errors.New("webhook: the endpoint's address is not a public one")

// inward are the addresses that are not the public internet beyond what
// netip's own tests name: an endpoint's URL comes from whoever holds an
// admin key, and must not be a way to reach what only this machine can.
var inward = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),     // "this network"
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),  // protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),   // reserved, and broadcast
	netip.MustParsePrefix("192.0.2.0/24"),  // documentation
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("168.63.129.16/32"), // a cloud platform's in-machine address
	netip.MustParsePrefix("::/96"),            // IPv4 written as "compatible" IPv6
	netip.MustParsePrefix("::ffff:0:0:0/96"),  // IPv4 "translated"
	netip.MustParsePrefix("fec0::/10"),        // site-local, retired but still routed
	netip.MustParsePrefix("100::/64"),         // discard
	netip.MustParsePrefix("2001::/32"),        // IPv4 reached through Teredo
	netip.MustParsePrefix("2001:db8::/32"),    // documentation
	netip.MustParsePrefix("64:ff9b::/96"),     // IPv4 reached through NAT64
	netip.MustParsePrefix("64:ff9b:1::/48"),   // the same, locally
	netip.MustParsePrefix("2002::/16"),        // IPv4 reached through 6to4
}

// public says whether an address is one on the public internet.
func public(a netip.Addr) bool {
	a = a.Unmap() // an IPv4 address written as IPv6 is that IPv4 address
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsUnspecified() || a.IsMulticast() ||
		a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast() {
		return false
	}
	for _, p := range inward {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// dialer connects to endpoints. Unless anywhere is set, it refuses an
// address that is not public; the check is made on the address actually
// connected to, after the name is resolved, so a name cannot pass it and
// then lead elsewhere.
func dialer(anywhere bool, timeout time.Duration) *net.Dialer {
	d := &net.Dialer{Timeout: timeout}
	if !anywhere {
		d.Control = func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || !public(ap.Addr()) {
				return errBlocked
			}
			return nil
		}
	}
	return d
}
