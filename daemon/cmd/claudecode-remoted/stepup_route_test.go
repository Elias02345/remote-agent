package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Elias02345/remote-agent/daemon/internal/db"
	"github.com/Elias02345/remote-agent/daemon/internal/files"
	"github.com/Elias02345/remote-agent/daemon/internal/identity"
	"github.com/Elias02345/remote-agent/daemon/internal/locks"
	"github.com/Elias02345/remote-agent/daemon/internal/terminal"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

const stepUpTestEmail = "owner@example.com"
const stepUpTestPassword = "correct horse battery staple"

// routerWithOwner builds the same wiring as testRouter, but with owner
// credentials configured — which is what makes the step-up ceremony
// performable at all.
func routerWithOwner(t *testing.T) (wiredRouter, *db.DB, string) {
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
	termMux := terminal.NewManager(database, lockMgr, terminal.NewTmux()).Routes()

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

	hash, err := identity.HashPassword(stepUpTestPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	secret, _, err := identity.GenerateTOTPSecret("ClaudeCode Remote Test", stepUpTestEmail)
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}

	wired, err := newRouter(routerConfig{
		TermMux:           termMux,
		FilesMux:          filesMux,
		Database:          database,
		OwnerEmail:        stepUpTestEmail,
		OwnerPasswordHash: hash,
		OwnerTOTPSecret:   secret,
	})
	if err != nil {
		t.Fatalf("newRouter: %v", err)
	}
	return wired, database, secret
}

func currentTOTP(t *testing.T, secret string) string {
	t.Helper()
	code, err := totp.GenerateCodeCustom(secret, time.Now(), totp.ValidateOpts{
		Period:    30,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		t.Fatalf("GenerateCodeCustom: %v", err)
	}
	return code
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	buf, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return buf
}

// The finding this route exists to close: nothing in production ever called
// StepUpGate.Grant, so /owner/devices/revoke was unreachable rather than
// guarded. An owner whose phone was stolen could not revoke that phone's key
// through the shipped system at all.
//
// This drives the whole flow through the real router: device auth, the
// ceremony, then the revoke.
func TestStepUpCeremonyMakesRevocationReachable(t *testing.T) {
	wired, database, secret := routerWithOwner(t)
	callerPriv := pairTestDevice(t, database, "dev-1")
	pairTestDevice(t, database, "dev-2")

	revokeBody := mustJSON(t, map[string]string{"device_id": "dev-2"})

	// Without a grant: refused, exactly as before.
	req := signedRequest(t, wired.DeviceAuth, "dev-1", callerPriv, http.MethodPost, "/owner/devices/revoke", revokeBody)
	rec := httptest.NewRecorder()
	wired.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoke without step-up: status = %d, want 401; body=%s", rec.Code, rec.Body)
	}

	// The ceremony, performed by the device itself.
	stepUpBody := mustJSON(t, map[string]string{
		"action":   string(identity.ActionRevokeDevice),
		"email":    stepUpTestEmail,
		"password": stepUpTestPassword,
		"code":     currentTOTP(t, secret),
	})
	req = signedRequest(t, wired.DeviceAuth, "dev-1", callerPriv, http.MethodPost, "/owner/step-up", stepUpBody)
	rec = httptest.NewRecorder()
	wired.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("step-up: status = %d, want 200; body=%s", rec.Code, rec.Body)
	}

	// And now the revoke goes through.
	req = signedRequest(t, wired.DeviceAuth, "dev-1", callerPriv, http.MethodPost, "/owner/devices/revoke", revokeBody)
	rec = httptest.NewRecorder()
	wired.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke after step-up: status = %d, want 204; body=%s", rec.Code, rec.Body)
	}
}

// A grant is bound to the device that earned it. Otherwise one compromised
// device could mint step-up for every other paired device.
func TestStepUpGrantDoesNotTransferToAnotherDevice(t *testing.T) {
	wired, database, secret := routerWithOwner(t)
	privOne := pairTestDevice(t, database, "dev-1")
	privTwo := pairTestDevice(t, database, "dev-2")
	pairTestDevice(t, database, "dev-3")

	stepUpBody := mustJSON(t, map[string]string{
		"action":   string(identity.ActionRevokeDevice),
		"email":    stepUpTestEmail,
		"password": stepUpTestPassword,
		"code":     currentTOTP(t, secret),
	})
	req := signedRequest(t, wired.DeviceAuth, "dev-1", privOne, http.MethodPost, "/owner/step-up", stepUpBody)
	rec := httptest.NewRecorder()
	wired.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("step-up for dev-1: status = %d, want 200; body=%s", rec.Code, rec.Body)
	}

	// dev-2 tries to spend dev-1's grant.
	revokeBody := mustJSON(t, map[string]string{"device_id": "dev-3"})
	req = signedRequest(t, wired.DeviceAuth, "dev-2", privTwo, http.MethodPost, "/owner/devices/revoke", revokeBody)
	rec = httptest.NewRecorder()
	wired.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("dev-2 revoking on dev-1's grant: status = %d, want 401; body=%s", rec.Code, rec.Body)
	}
}

// The ceremony is a re-authentication, so a valid device signature alone must
// not be enough to obtain a grant.
func TestStepUpRefusesWrongFactors(t *testing.T) {
	wired, database, secret := routerWithOwner(t)
	priv := pairTestDevice(t, database, "dev-1")

	cases := map[string]map[string]string{
		"wrong password": {"password": "wrong", "code": currentTOTP(t, secret), "email": stepUpTestEmail},
		"wrong code":     {"password": stepUpTestPassword, "code": "000000", "email": stepUpTestEmail},
		"wrong email":    {"password": stepUpTestPassword, "code": currentTOTP(t, secret), "email": "someone@else.invalid"},
	}
	for name, fields := range cases {
		body := map[string]string{"action": string(identity.ActionRevokeDevice)}
		for k, v := range fields {
			body[k] = v
		}
		req := signedRequest(t, wired.DeviceAuth, "dev-1", priv, http.MethodPost, "/owner/step-up", mustJSON(t, body))
		rec := httptest.NewRecorder()
		wired.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401; body=%s", name, rec.Code, rec.Body)
		}
	}
}

// An unauthenticated caller has no device identity, so there is nothing to
// bind a grant to. It must not be possible to name one in the body.
func TestStepUpRequiresDeviceAuth(t *testing.T) {
	wired, _, secret := routerWithOwner(t)

	body := mustJSON(t, map[string]string{
		"action":    string(identity.ActionRevokeDevice),
		"email":     stepUpTestEmail,
		"password":  stepUpTestPassword,
		"code":      currentTOTP(t, secret),
		"device_id": "dev-1",
	})
	req := httptest.NewRequest(http.MethodPost, "/owner/step-up", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	wired.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated step-up: status = %d, want 401; body=%s", rec.Code, rec.Body)
	}
}

// An installation without owner credentials must refuse rather than hand out
// a grant because there was nothing to verify against.
func TestStepUpFailsClosedWithoutOwnerCredentials(t *testing.T) {
	wired, database := testRouter(t, false)
	priv := pairTestDevice(t, database, "dev-1")

	body := mustJSON(t, map[string]string{
		"action":   string(identity.ActionRevokeDevice),
		"email":    stepUpTestEmail,
		"password": stepUpTestPassword,
		"code":     "123456",
	})
	req := signedRequest(t, wired.DeviceAuth, "dev-1", priv, http.MethodPost, "/owner/step-up", body)
	rec := httptest.NewRecorder()
	wired.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("step-up without owner credentials: status = %d, want 401; body=%s", rec.Code, rec.Body)
	}
}

// Only registered actions. An arbitrary string would put an attacker-chosen
// key into the gate's map and grant permission for actions that do not exist.
func TestStepUpRejectsUnknownAction(t *testing.T) {
	wired, database, secret := routerWithOwner(t)
	priv := pairTestDevice(t, database, "dev-1")

	body := mustJSON(t, map[string]string{
		"action":   "delete_everything",
		"email":    stepUpTestEmail,
		"password": stepUpTestPassword,
		"code":     currentTOTP(t, secret),
	})
	req := signedRequest(t, wired.DeviceAuth, "dev-1", priv, http.MethodPost, "/owner/step-up", body)
	rec := httptest.NewRecorder()
	wired.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown action: status = %d, want 400; body=%s", rec.Code, rec.Body)
	}
}
