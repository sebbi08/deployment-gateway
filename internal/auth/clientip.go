package auth

import (
	"net/http"
	"net/netip"
	"strings"
)

// ClientIP returns the client address, honouring X-Forwarded-For only when
// the direct peer is a trusted proxy. It walks XFF from the right and
// returns the first address that is not itself a trusted proxy.
func ClientIP(r *http.Request, trusted []netip.Prefix) string {
	peer := peerAddr(r)
	if !peer.IsValid() {
		return r.RemoteAddr
	}
	if inPrefixes(peer, trusted) {
		// All header lines: a proxy may append its own line instead of
		// extending the client's.
		parts := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
		for i := len(parts) - 1; i >= 0; i-- {
			a, ok := parseForwarded(strings.TrimSpace(parts[i]))
			if !ok {
				break
			}
			a = a.Unmap()
			if !inPrefixes(a, trusted) {
				return a.String()
			}
		}
	}
	return peer.String()
}

func PeerTrusted(r *http.Request, trusted []netip.Prefix) bool {
	p := peerAddr(r)
	return p.IsValid() && inPrefixes(p, trusted)
}

func peerAddr(r *http.Request) netip.Addr {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	return ap.Addr().Unmap()
}

func inPrefixes(a netip.Addr, prefixes []netip.Prefix) bool {
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// parseForwarded accepts "ip", "ip:port" and "[v6]:port".
func parseForwarded(s string) (netip.Addr, bool) {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr(), true
	}
	a, err := netip.ParseAddr(s)
	return a, err == nil
}
