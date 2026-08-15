package identity

import (
	"net"
	"net/http"
	"strings"
)

// ClientIPResolver turns an *http.Request into the address the rate limiter
// should count against.
//
// r.RemoteAddr is the only trustworthy answer when nothing sits in front of the
// daemon, and the only safe default: a forwarded-for header is caller-supplied
// text, so trusting it unconditionally lets anyone pick their own rate-limit
// bucket and never trip a lockout.
//
// But this daemon normally runs behind CloudGate's Cloudflare tunnel, where
// RemoteAddr is the tunnel's address for every request on earth. Per-IP
// limiting then does the opposite of its job twice over: it never isolates an
// attacker, and one attacker's lockout blocks every other device too.
//
// So the header is honoured only when the connection actually came from an
// address the operator listed as a proxy. Nothing configured, nothing trusted.
type ClientIPResolver struct {
	trusted []*net.IPNet
}

// NewClientIPResolver parses CIDRs (or bare IPs) that may forward on a client's
// behalf. Unparseable entries are skipped rather than fatal — a typo in this
// list should narrow trust, never widen it.
func NewClientIPResolver(trustedProxies []string) *ClientIPResolver {
	r := &ClientIPResolver{}
	for _, entry := range trustedProxies {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if _, block, err := net.ParseCIDR(entry); err == nil {
			r.trusted = append(r.trusted, block)
			continue
		}
		if ip := net.ParseIP(entry); ip != nil {
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			r.trusted = append(r.trusted, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
		}
	}
	return r
}

func (c *ClientIPResolver) isTrusted(ip net.IP) bool {
	for _, block := range c.trusted {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}

// IP returns the address to rate-limit this request against.
func (c *ClientIPResolver) IP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(strings.TrimSpace(host))
	if peer == nil || !c.isTrusted(peer) {
		return host
	}

	// Cloudflare's own header first: it carries exactly one address and the
	// edge overwrites whatever the client sent, so there is nothing to walk.
	if cf := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); cf != "" {
		if ip := net.ParseIP(cf); ip != nil {
			return ip.String()
		}
	}

	// Otherwise walk X-Forwarded-For from the right, discarding hops we trust,
	// and take the first address we do not. Reading left-to-right instead would
	// take whatever the client typed into the header themselves.
	forwarded := r.Header.Get("X-Forwarded-For")
	parts := strings.Split(forwarded, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(parts[i]))
		if ip == nil {
			continue
		}
		if c.isTrusted(ip) {
			continue
		}
		return ip.String()
	}
	return host
}
