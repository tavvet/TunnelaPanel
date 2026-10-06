package panel

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

type trustedProxyPolicy []netip.Prefix

func parseTrustedProxies(values []string) (trustedProxyPolicy, error) {
	var prefixes trustedProxyPolicy
	for _, value := range values {
		value = strings.TrimSpace(value)
		if addr, err := netip.ParseAddr(value); err == nil && addr.Zone() == "" {
			addr = addr.Unmap()
			prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, fmt.Errorf("invalid PANEL_TRUSTED_PROXIES entry %q: expected an IP address or CIDR", value)
		}
		// Treat IPv4-mapped addresses consistently with ordinary IPv4 peers.
		if prefix.Addr().Is4In6() {
			if prefix.Bits() < 96 {
				return nil, fmt.Errorf("invalid PANEL_TRUSTED_PROXIES entry %q: mapped IPv4 CIDR must have at least 96 prefix bits", value)
			}
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}

// remoteIP trusts only the direct peer. A proxy must overwrite X-Real-IP rather
// than pass client input through; X-Forwarded-For is deliberately never read.
func (s *Server) remoteIP(r *http.Request) string {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap()
	trusted := false
	for _, prefix := range s.trustedProxies {
		if prefix.Contains(peer) {
			trusted = true
			break
		}
	}
	if trusted {
		values := r.Header.Values("X-Real-IP")
		if len(values) == 1 {
			if forwarded, err := netip.ParseAddr(values[0]); err == nil && forwarded.Zone() == "" {
				return forwarded.Unmap().String()
			}
		}
	}
	return peer.String()
}
