package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Elias02345/remote-agent/daemon/internal/db"
	"github.com/Elias02345/remote-agent/daemon/internal/files"
	"github.com/Elias02345/remote-agent/daemon/internal/identity"
	"github.com/Elias02345/remote-agent/daemon/internal/locks"
	"github.com/Elias02345/remote-agent/daemon/internal/terminal"
)

// A wildcard bind is the one mistake that turns "auth is enforced by the
// wiring, not the network" into an open shell reachable from anywhere.
func TestCheckBindAddressRejectsWildcards(t *testing.T) {
	bad := []string{"0.0.0.0:8080", ":8080", "[::]:8080", "*:8080"}
	for _, addr := range bad {
		if err := checkBindAddress(addr); err == nil {
			t.Errorf("checkBindAddress(%q) = nil, want an error", addr)
		}
	}
}

func TestCheckBindAddressAcceptsSpecificHosts(t *testing.T) {
	good := []string{"127.0.0.1:8080", "localhost:8080", "100.101.102.103:8080", "[::1]:8080"}
	for _, addr := range good {
		if err := checkBindAddress(addr); err != nil {
			t.Errorf("checkBindAddress(%q) = %v, want nil", addr, err)
		}
	}
}

func TestCheckBindAddressRejectsGarbage(t *testing.T) {
	if err := checkBindAddress("not-an-address"); err == nil {
		t.Error("expected an error for a malformed address")
	}
}

func TestCheckLoopbackOnlyAcceptsLoopback(t *testing.T) {
	good := []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080"}
	for _, addr := range good {
		if err := checkLoopbackOnly(addr); err != nil {
			t.Errorf("checkLoopbackOnly(%q) = %v, want nil", addr, err)
		}
	}
}

// This is the exact guard main() runs before starting when
// --insecure-no-auth is set: a bind address wider than loopback — even a
// Tailscale IP, which checkBindAddress alone would accept — must be
// refused, since disabling device auth on anything reachable beyond this
// machine is an open shell on the network, not a test configuration.
func TestCheckLoopbackOnlyRejectsNonLoopback(t *testing.T) {
	bad := []string{"100.101.102.103:8080", "0.0.0.0:8080", "example.com:8080"}
	for _, addr := range bad {
		if err := checkLoopbackOnly(addr); err == nil {
			t.Errorf("checkLoopbackOnly(%q) = nil, want an error (refusing to start with --insecure-no-auth)", addr)
		}
	}
}

// --- newRouter integration tests -------------------------------------------

// testRouter builds the same wiring main() does, against a temporary
// database and a scratch file root, so tests exercise the real
// terminal/files muxes wrapped in the real device-auth middleware instead
// of a hand-rolled stand-in.
func testRouter(t *testing.T, insecureNoAuth bool) (wiredRouter, *db.DB) {
	t.Helper()
	dir := t.TempDir()

	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	lockMgr, err := locks.New(filepath.Join(dir, "locks"))
	if err != nil {
		t.Fatalf("locks.New: %v", err)
	}
	mgr := terminal.NewManager(database, lockMgr, terminal.NewTmux())
	termMux := mgr.Routes()

	fileRoot := filepath.Join(dir, "exchange")
	if err := os.MkdirAll(fileRoot, 0o755); err != nil {
		t.Fatalf("mkdir file root: %v", err)
	}
	fileStore, err := files.NewStore([]string{fileRoot})
	if err != nil {
		t.Fatalf("files.NewStore: %v", err)
	}
	uploader, err := files.NewUploader(fileStore, filepath.Join(dir, "uploads"))
	if err != nil {
		t.Fatalf("files.NewUploader: %v", err)
	}
	filesMux := http.NewServeMux()
	(&files.API{Store: fileStore, Uploader: uploader}).Register(filesMux)

	wired, err := newRouter(routerConfig{
		TermMux:        termMux,
		FilesMux:       filesMux,
		Database:       database,
		InsecureNoAuth: insecureNoAuth,
	})
	if err != nil {
		t.Fatalf("newRouter: %v", err)
	}
	return wired, database
}

func pairTestDevice(t *testing.T, database *db.DB, id string) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	store := identity.NewStore(database)
	if err := store.PairDevice(id, "test-device", "linux", base64.StdEncoding.EncodeToString(pub)); err != nil {
		t.Fatalf("PairDevice: %v", err)
	}
	return priv
}

func signedRequest(t *testing.T, auth *identity.DeviceAuthenticator, deviceID string, priv ed25519.PrivateKey, method, path string, body []byte) *http.Request {
	t.Helper()
	nonce, err := auth.IssueChallenge(deviceID)
	if err != nil {
		t.Fatalf("IssueChallenge: %v", err)
	}
	sig := ed25519.Sign(priv, nonce)
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("X-Device-Id", deviceID)
	req.Header.Set("X-Device-Signature", base64.StdEncoding.EncodeToString(sig))
	return req
}

// G-02, resolved: /files/* gets exactly the same device auth as
// /sessions*. Neither must let an unauthenticated request through to the
// real handler.
func TestNewRouter_UnauthenticatedRequestsGet401AndNeverReachHandler(t *testing.T) {
	wired, _ := testRouter(t, false)

	for _, path := range []string{"/sessions", "/files"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		wired.Handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", path, rec.Code)
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Errorf("%s: response body did not decode as JSON: %v", path, err)
			continue
		}
		// A GET /sessions that actually reached the handler would answer
		// with a bare JSON list, not an {"error": "..."} envelope.
		if _, ok := out["error"]; !ok {
			t.Errorf("%s: response = %v, want an {\"error\": ...} body (proof the handler was never reached)", path, out)
		}
	}
}

func TestNewRouter_AuthenticatedSessionsRequestPasses(t *testing.T) {
	wired, database := testRouter(t, false)
	priv := pairTestDevice(t, database, "dev-1")

	req := signedRequest(t, wired.DeviceAuth, "dev-1", priv, http.MethodGet, "/sessions", nil)
	rec := httptest.NewRecorder()
	wired.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var list []any
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode sessions list: %v", err)
	}
}

func TestNewRouter_AuthenticatedFilesRequestPasses(t *testing.T) {
	wired, database := testRouter(t, false)
	priv := pairTestDevice(t, database, "dev-1")

	req := signedRequest(t, wired.DeviceAuth, "dev-1", priv, http.MethodGet, "/files?path=.", nil)
	rec := httptest.NewRecorder()
	wired.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body)
	}
}

// Section 10.1: revoking a device needs a fresh step-up grant even from an
// already-paired device presenting a perfectly valid Ed25519 signature.
func TestNewRouter_RevokeDeviceRequiresStepUp(t *testing.T) {
	wired, database := testRouter(t, false)
	callerPriv := pairTestDevice(t, database, "dev-1")
	pairTestDevice(t, database, "dev-2")

	revokeBody, err := json.Marshal(map[string]string{"device_id": "dev-2"})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	req := signedRequest(t, wired.DeviceAuth, "dev-1", callerPriv, http.MethodPost, "/owner/devices/revoke", revokeBody)
	rec := httptest.NewRecorder()
	wired.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoke without step-up: status = %d, want 401; body=%s", rec.Code, rec.Body)
	}

	wired.StepUp.Grant("dev-1", identity.ActionRevokeDevice)
	req = signedRequest(t, wired.DeviceAuth, "dev-1", callerPriv, http.MethodPost, "/owner/devices/revoke", revokeBody)
	rec = httptest.NewRecorder()
	wired.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke with step-up: status = %d, want 204; body=%s", rec.Code, rec.Body)
	}
}

// Listing devices needs device auth but NOT step-up. Section 10.1's catalogue
// of sensitive actions is revoke, pair, delete backup and change security
// settings; listing is a read of the owner's own devices, not a state change.
func TestNewRouter_ListDevicesNeedsAuthButNotStepUp(t *testing.T) {
	wired, database := testRouter(t, false)
	callerPriv := pairTestDevice(t, database, "dev-1")

	unauth := httptest.NewRequest(http.MethodGet, "/owner/devices", nil)
	rec := httptest.NewRecorder()
	wired.Handler.ServeHTTP(rec, unauth)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("list without device auth: status = %d, want 401", rec.Code)
	}

	req := signedRequest(t, wired.DeviceAuth, "dev-1", callerPriv, http.MethodGet, "/owner/devices", nil)
	rec = httptest.NewRecorder()
	wired.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list with device auth: status = %d, want 200; body=%s", rec.Code, rec.Body)
	}
}

// The regression this separation exists to prevent.
//
// Step-up grants are single-use. While listing and revoking shared one action,
// the obvious flow — look at the device list, then revoke one of them — spent
// the grant on the list and the revoke that followed failed with 401. Anyone
// hitting that would reasonably conclude step-up was broken.
func TestNewRouter_ListingDevicesDoesNotConsumeTheRevokeGrant(t *testing.T) {
	wired, database := testRouter(t, false)
	callerPriv := pairTestDevice(t, database, "dev-1")
	pairTestDevice(t, database, "dev-2")

	wired.StepUp.Grant("dev-1", identity.ActionRevokeDevice)

	// Look at the list first, exactly as a user deciding what to revoke would.
	listReq := signedRequest(t, wired.DeviceAuth, "dev-1", callerPriv, http.MethodGet, "/owner/devices", nil)
	rec := httptest.NewRecorder()
	wired.Handler.ServeHTTP(rec, listReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status = %d, want 200; body=%s", rec.Code, rec.Body)
	}

	// The grant must still be there.
	body, err := json.Marshal(map[string]string{"device_id": "dev-2"})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	revokeReq := signedRequest(t, wired.DeviceAuth, "dev-1", callerPriv, http.MethodPost, "/owner/devices/revoke", body)
	rec = httptest.NewRecorder()
	wired.Handler.ServeHTTP(rec, revokeReq)
	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("revoke after listing: status = %d, want success — listing consumed the grant; body=%s",
			rec.Code, rec.Body)
	}
}

func TestNewRouter_InsecureNoAuthBypassesDeviceAuth(t *testing.T) {
	wired, _ := testRouter(t, true)

	req := httptest.NewRequest(http.MethodGet, "/sessions", nil) // no auth headers at all
	rec := httptest.NewRecorder()
	wired.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("insecure-no-auth: status = %d, want 200; body=%s", rec.Code, rec.Body)
	}
}

// /auth/challenge (unauthenticated) -> /auth/ws-ticket (device-authed) ->
// the ticket unlocks exactly one WebSocket-upgrade request and no more.
func TestNewRouter_ChallengeThenWSTicketIsSingleUse(t *testing.T) {
	wired, database := testRouter(t, false)
	priv := pairTestDevice(t, database, "dev-1")

	challengeBody, _ := json.Marshal(map[string]string{"device_id": "dev-1"})
	req := httptest.NewRequest(http.MethodPost, "/auth/challenge", bytes.NewReader(challengeBody))
	rec := httptest.NewRecorder()
	wired.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/auth/challenge: status = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var chal struct {
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &chal); err != nil {
		t.Fatalf("decode challenge: %v", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(chal.Challenge)
	if err != nil {
		t.Fatalf("decode nonce: %v", err)
	}
	sig := ed25519.Sign(priv, nonce)

	req = httptest.NewRequest(http.MethodPost, "/auth/ws-ticket", nil)
	req.Header.Set("X-Device-Id", "dev-1")
	req.Header.Set("X-Device-Signature", base64.StdEncoding.EncodeToString(sig))
	rec = httptest.NewRecorder()
	wired.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/auth/ws-ticket: status = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var tk struct {
		Ticket string `json:"ticket"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tk); err != nil {
		t.Fatalf("decode ticket: %v", err)
	}
	if tk.Ticket == "" {
		t.Fatal("empty ticket")
	}

	// No ticket at all: refused before the WebSocket upgrade is attempted.
	req = httptest.NewRequest(http.MethodGet, "/sessions/does-not-exist/stream", nil)
	rec = httptest.NewRecorder()
	wired.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("stream without ticket: status = %d, want 401", rec.Code)
	}

	// A valid ticket gets past the ticket check (the session itself
	// doesn't exist, so this fails downstream — the point is only that it
	// got past auth, not that a WebSocket actually opened).
	req = httptest.NewRequest(http.MethodGet, "/sessions/does-not-exist/stream?ticket="+tk.Ticket, nil)
	rec = httptest.NewRecorder()
	wired.Handler.ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("stream with a valid ticket was still refused as unauthorized: body=%s", rec.Body)
	}

	// The same ticket, replayed: must be refused (single-use).
	req = httptest.NewRequest(http.MethodGet, "/sessions/does-not-exist/stream?ticket="+tk.Ticket, nil)
	rec = httptest.NewRecorder()
	wired.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("replayed ticket: status = %d, want 401 (single-use)", rec.Code)
	}
}
