package terminal

import (
	"net/http/httptest"
	"testing"
)

// A WebSocket handshake is exempt from the same-origin policy, so without this
// check any page the operator happens to visit could open a socket to the
// daemon. The ticket blocks it too; this is the second lock on the same door.
func TestSameOriginOrNone(t *testing.T) {
	cases := []struct {
		name   string
		host   string
		origin string
		want   bool
	}{
		// Native clients — the Flutter app, curl, these tests — send no Origin
		// at all. Only browsers do, so an absent header is not a cross-site
		// request.
		{"no origin header", "remote.example.com", "", true},

		{"same origin over https", "remote.example.com", "https://remote.example.com", true},
		// The tunnel terminates TLS at Cloudflare's edge and forwards
		// plaintext, so the daemon sees http where the browser saw https. Only
		// the host may be compared.
		{"same host, different scheme", "remote.example.com", "http://remote.example.com", true},
		{"case-insensitive host", "Remote.Example.com", "https://remote.example.com", true},
		{"local dev client", "127.0.0.1:8080", "http://127.0.0.1:8080", true},

		{"hostile origin", "remote.example.com", "https://evil.example", false},
		// The classic bypass: a domain that merely ends with the real one.
		{"suffix lookalike", "remote.example.com", "https://evil-remote.example.com", false},
		{"prefix lookalike", "remote.example.com", "https://remote.example.com.evil.test", false},
		{"port mismatch", "127.0.0.1:8080", "http://127.0.0.1:9999", false},
		{"unparseable origin", "remote.example.com", "://not a url", false},
		{"null origin", "remote.example.com", "null", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/sessions/x/stream", nil)
			r.Host = c.host
			if c.origin != "" {
				r.Header.Set("Origin", c.origin)
			}
			if got := sameOriginOrNone(r); got != c.want {
				t.Errorf("sameOriginOrNone(host=%q, origin=%q) = %v, want %v",
					c.host, c.origin, got, c.want)
			}
		})
	}
}
