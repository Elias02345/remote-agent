package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
)

// authedRequest builds a request against a fresh challenge for deviceID,
// correctly signed, ready to pass RequireDevice.
func authedRequest(t *testing.T, auth *DeviceAuthenticator, deviceID string, priv ed25519.PrivateKey) *http.Request {
	t.Helper()
	nonce, err := auth.IssueChallenge(deviceID)
	if err != nil {
		t.Fatalf("IssueChallenge: %v", err)
	}
	sig := ed25519.Sign(priv, nonce)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("X-Device-Id", deviceID)
	req.Header.Set("X-Device-Signature", base64.StdEncoding.EncodeToString(sig))
	return req
}

func TestRequireDevice_AuthenticatedRequestReachesHandlerWithDeviceID(t *testing.T) {
	lookup := newFakeLookup()
	pub, priv, _ := ed25519.GenerateKey(nil)
	lookup.add("dev-1", pub, false)
	auth := NewDeviceAuthenticator(lookup)

	var gotID string
	var ok bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID, ok = DeviceIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := authedRequest(t, auth, "dev-1", priv)
	rec := httptest.NewRecorder()
	RequireDevice(auth, next).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !ok || gotID != "dev-1" {
		t.Fatalf("DeviceIDFromContext = (%q, %v), want (\"dev-1\", true)", gotID, ok)
	}
}

func TestRequireDevice_UnauthenticatedRequestGets401AndNeverReachesHandler(t *testing.T) {
	lookup := newFakeLookup()
	auth := NewDeviceAuthenticator(lookup)

	invoked := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		invoked = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil) // no headers at all
	rec := httptest.NewRecorder()
	RequireDevice(auth, next).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if invoked {
		t.Fatal("wrapped handler was invoked for an unauthenticated request")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestRequireDevice_MalformedSignatureGets401(t *testing.T) {
	lookup := newFakeLookup()
	pub, _, _ := ed25519.GenerateKey(nil)
	lookup.add("dev-1", pub, false)
	auth := NewDeviceAuthenticator(lookup)
	if _, err := auth.IssueChallenge("dev-1"); err != nil {
		t.Fatalf("IssueChallenge: %v", err)
	}

	invoked := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		invoked = true
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("X-Device-Id", "dev-1")
	req.Header.Set("X-Device-Signature", "not valid base64 !!!")
	rec := httptest.NewRecorder()
	RequireDevice(auth, next).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if invoked {
		t.Fatal("wrapped handler was invoked for a malformed signature header")
	}
}

func TestRequireStepUp_RejectsWithoutGrantAndPassesWithOne(t *testing.T) {
	lookup := newFakeLookup()
	pub, priv, _ := ed25519.GenerateKey(nil)
	lookup.add("dev-1", pub, false)
	auth := NewDeviceAuthenticator(lookup)
	gate := NewStepUpGate()

	invoked := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		invoked++
		w.WriteHeader(http.StatusOK)
	})
	protected := RequireDevice(auth, RequireStepUp(gate, ActionRevokeDevice, next))

	// No grant yet: refused, handler not invoked.
	req := authedRequest(t, auth, "dev-1", priv)
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("without grant: status = %d, want 401", rec.Code)
	}
	if invoked != 0 {
		t.Fatalf("without grant: handler invoked %d times, want 0", invoked)
	}

	// Grant it, then retry with a fresh device challenge (the previous one
	// was already consumed by the rejected attempt above).
	gate.Grant("dev-1", ActionRevokeDevice)
	req = authedRequest(t, auth, "dev-1", priv)
	rec = httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("with grant: status = %d, want 200", rec.Code)
	}
	if invoked != 1 {
		t.Fatalf("with grant: handler invoked %d times, want 1", invoked)
	}
}
