package httpx

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ClientIPResolver determines the client address of a request. It honors
// X-Forwarded-For only when the direct peer is in the configured
// trusted-proxy list, and trusts nothing else.
type ClientIPResolver struct {
	trusted []netip.Prefix
}

// NewClientIPResolver builds a resolver for the given trusted proxy CIDRs.
func NewClientIPResolver(trusted []netip.Prefix) *ClientIPResolver {
	return &ClientIPResolver{trusted: trusted}
}

func (c *ClientIPResolver) isTrusted(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, p := range c.trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// ClientIP returns the client's IP as a string. When the peer is a trusted
// proxy, it walks X-Forwarded-For from the right and returns the first
// address that is not itself a trusted proxy.
func (c *ClientIPResolver) ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap()
	if !c.isTrusted(peer) {
		return peer.String()
	}
	var hops []string
	for _, header := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(header, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break // garbage in the chain: stop trusting it
		}
		addr = addr.Unmap()
		if !c.isTrusted(addr) {
			return addr.String()
		}
	}
	return peer.String()
}
