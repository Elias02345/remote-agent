package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Without a cap, json.Decoder reads until EOF — so an endless body sent at an
// unauthenticated endpoint is a memory exhaustion primitive that needs no
// credentials at all.
func TestLimitRequestBodyCapsJSONEndpoints(t *testing.T) {
	var read int64
	h := limitRequestBody(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		read = n
	}))

	body := strings.NewReader(strings.Repeat("A", maxJSONBody+4096))
	req := httptest.NewRequest(http.MethodPost, "/auth/challenge", body)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if read > maxJSONBody {
		t.Fatalf("handler read %d bytes, cap is %d", read, maxJSONBody)
	}
}

// Uploads stream to disk rather than buffering, so capping them would cap the
// file size for no benefit.
func TestLimitRequestBodyExemptsUploads(t *testing.T) {
	var read int64
	h := limitRequestBody(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		read = n
	}))

	size := maxJSONBody + 4096
	req := httptest.NewRequest(http.MethodPatch, "/files/upload/abc123",
		strings.NewReader(strings.Repeat("A", size)))
	h.ServeHTTP(httptest.NewRecorder(), req)

	if read != int64(size) {
		t.Fatalf("upload body was truncated to %d of %d bytes", read, size)
	}
}

func TestSecureHeaders(t *testing.T) {
	h := secureHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sessions", nil))

	// nosniff is the one that matters: without it a browser may decide a JSON
	// error body carrying attacker-influenced text is HTML, and run it.
	for header, want := range map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "no-referrer",
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

// The test client legitimately loads xterm.js from a CDN, so the strict CSP
// must not reach it — otherwise the one tool for verifying the terminal
// protocol stops working.
func TestSecureHeadersDoesNotBreakTheTestClient(t *testing.T) {
	h := secureHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if csp := rec.Header().Get("Content-Security-Policy"); csp != "" {
		t.Errorf("index page got CSP %q; it would block the CDN script it needs", csp)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("nosniff must still apply to the index page, got %q", got)
	}
}
