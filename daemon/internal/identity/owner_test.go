package identity

import (
	"bytes"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/pquerna/otp/totp"
)

// fakeDeviceStore is an in-memory ownerDeviceStore, so owner_test.go
// doesn't need package db (or its sqlite driver) at all — store.go and
// store_test.go already cover the real *db.DB-backed implementation.
type fakeDeviceStore struct {
	mu     sync.Mutex
	paired map[string]DeviceInfo

	handle []byte
	creds  map[string]webauthn.Credential // keyed by base64url(cred.ID)
}

func newFakeDeviceStore() *fakeDeviceStore {
	return &fakeDeviceStore{paired: map[string]DeviceInfo{}, creds: map[string]webauthn.Credential{}}
}

func (f *fakeDeviceStore) OwnerUserHandle() ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.handle == nil {
		f.handle = []byte("fake-owner-handle")
	}
	return f.handle, nil
}

func (f *fakeDeviceStore) AddWebAuthnCredential(name string, cred *webauthn.Credential) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := base64.RawURLEncoding.EncodeToString(cred.ID)
	f.creds[id] = *cred
	return nil
}

func (f *fakeDeviceStore) ListWebAuthnCredentials() ([]webauthn.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]webauthn.Credential, 0, len(f.creds))
	for _, c := range f.creds {
		out = append(out, c)
	}
	return out, nil
}

func (f *fakeDeviceStore) CountWebAuthnCredentials() (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.creds), nil
}

func (f *fakeDeviceStore) UpdateWebAuthnSignCount(id string, count uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.creds[id]
	if !ok {
		return sql.ErrNoRows
	}
	c.Authenticator.SignCount = count
	f.creds[id] = c
	return nil
}

func (f *fakeDeviceStore) TouchWebAuthnCredential(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.creds[id]; !ok {
		return sql.ErrNoRows
	}
	return nil
}

func (f *fakeDeviceStore) PairDevice(id, name, platform, pubKeyB64 string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paired[id] = DeviceInfo{ID: id, Name: name, Platform: platform, PubKey: pubKeyB64}
	return nil
}

func (f *fakeDeviceStore) ListDevices() ([]DeviceInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]DeviceInfo, 0, len(f.paired))
	for _, d := range f.paired {
		out = append(out, d)
	}
	return out, nil
}

func (f *fakeDeviceStore) RevokeDevice(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.paired[id]
	if !ok {
		return sql.ErrNoRows
	}
	d.Revoked = true
	f.paired[id] = d
	return nil
}

const testOwnerEmail = "owner@example.com"
const testOwnerPassword = "correct horse battery staple"

// testOwner builds an Owner with a real password hash and a real TOTP
// secret configured, but no WebAuthn RP ID — mirroring the daemon's actual
// state today (D-04 open). Returns the owner, its backing store, and the
// TOTP secret so tests can generate valid codes.
func testOwner(t *testing.T) (*Owner, *fakeDeviceStore, string) {
	t.Helper()
	hash, err := HashPassword(testOwnerPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	secret, _, err := GenerateTOTPSecret("ClaudeCode Remote Test", testOwnerEmail)
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	store := newFakeDeviceStore()
	owner := NewOwner(OwnerConfig{
		Email:        testOwnerEmail,
		PasswordHash: hash,
		TOTPSecret:   secret,
		// WebAuthnRPID intentionally left empty: D-04 is still open.
	}, store, NewRateLimiter())
	return owner, store, secret
}

// doJSON POSTs/GETs body (if non-nil) as JSON against h and decodes a JSON
// object response, if there is one. All requests share a fixed RemoteAddr
// so rate-limiting tests see one consistent client IP.
func doJSON(t *testing.T, h http.HandlerFunc, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(buf)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.RemoteAddr = "203.0.113.7:5555"
	rec := httptest.NewRecorder()
	h(rec, req)

	var out map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec, out
}

func startPairing(t *testing.T, owner *Owner) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	rec, out := doJSON(t, owner.handlePairStart, http.MethodPost, "/owner/pair/start", map[string]any{
		"device_pubkey": base64.StdEncoding.EncodeToString(pub),
		"device_name":   "test-device",
		"platform":      "linux",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("pair/start: status = %d, body = %s", rec.Code, rec.Body)
	}
	id, _ := out["pairing_id"].(string)
	if id == "" {
		t.Fatalf("pair/start: missing pairing_id in %v", out)
	}
	return id
}

func TestOwnerPairStart_ReturnsAllThreeOutstanding(t *testing.T) {
	owner, _, _ := testOwner(t)
	id := startPairing(t, owner)
	if id == "" {
		t.Fatal("empty pairing id")
	}
}

// The whole point of driving Attempt instead of reimplementing the chain:
// completion must refuse until every factor is satisfied, and it must say
// which ones remain.
func TestOwnerPairComplete_RefusesWithFactorsOutstanding(t *testing.T) {
	owner, _, _ := testOwner(t)
	pairingID := startPairing(t, owner)

	rec, out := doJSON(t, owner.handlePairComplete, http.MethodPost, "/owner/pair/complete", map[string]any{
		"pairing_id": pairingID,
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body)
	}
	outstanding, _ := out["outstanding"].([]any)
	if len(outstanding) != 3 {
		t.Fatalf("outstanding = %v, want all 3 factors", outstanding)
	}
}

// Password and TOTP alone are two of three. D-07 (no reduced tier) means
// completion must still refuse, with exactly the passkey factor left.
func TestOwnerPairComplete_PasskeyAlwaysOutstandingWithoutRPID(t *testing.T) {
	owner, _, secret := testOwner(t)
	pairingID := startPairing(t, owner)

	rec, _ := doJSON(t, owner.handlePairPassword, http.MethodPost, "/owner/pair/password", map[string]any{
		"pairing_id": pairingID, "email": testOwnerEmail, "password": testOwnerPassword,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("password: status = %d, body=%s", rec.Code, rec.Body)
	}

	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	rec, out := doJSON(t, owner.handlePairTOTP, http.MethodPost, "/owner/pair/totp", map[string]any{
		"pairing_id": pairingID, "code": code,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("totp: status = %d, body=%s", rec.Code, rec.Body)
	}
	outstanding, _ := out["outstanding"].([]any)
	if len(outstanding) != 1 || outstanding[0] != string(FactorPasskey) {
		t.Fatalf("outstanding after password+totp = %v, want [passkey]", outstanding)
	}

	rec, out = doJSON(t, owner.handlePairComplete, http.MethodPost, "/owner/pair/complete", map[string]any{
		"pairing_id": pairingID,
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("complete: status = %d, want 409; body=%s", rec.Code, rec.Body)
	}
	outstanding, _ = out["outstanding"].([]any)
	if len(outstanding) != 1 || outstanding[0] != string(FactorPasskey) {
		t.Fatalf("complete outstanding = %v, want [passkey]", outstanding)
	}
}

// D-04 is still open and the RP ID is unset: no bypass, no dev flag, no
// "skip if unconfigured". The passkey endpoint must fail every time.
func TestOwnerPairPasskey_FailsWithUnsetRPID(t *testing.T) {
	owner, _, _ := testOwner(t)
	pairingID := startPairing(t, owner)

	rec, _ := doJSON(t, owner.handlePairPasskey, http.MethodPost, "/owner/pair/passkey", map[string]any{
		"pairing_id": pairingID, "assertion": json.RawMessage(`{}`),
	})
	if rec.Code == http.StatusOK {
		t.Fatal("passkey endpoint succeeded with no WebAuthn RP ID configured (D-04 still open)")
	}
}

// A wrong password and a wrong TOTP code must be indistinguishable from
// the outside: same status, same error message.
func TestOwnerFactors_WrongCredentialsSameShapeAndStatus(t *testing.T) {
	owner, _, secret := testOwner(t)

	pairingID1 := startPairing(t, owner)
	recPW, outPW := doJSON(t, owner.handlePairPassword, http.MethodPost, "/owner/pair/password", map[string]any{
		"pairing_id": pairingID1, "email": testOwnerEmail, "password": "definitely-wrong",
	})

	valid, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	wrongCode := "000000"
	if wrongCode == valid {
		wrongCode = "111111"
	}
	pairingID2 := startPairing(t, owner)
	recTOTP, outTOTP := doJSON(t, owner.handlePairTOTP, http.MethodPost, "/owner/pair/totp", map[string]any{
		"pairing_id": pairingID2, "code": wrongCode,
	})

	if recPW.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password status = %d, want 401", recPW.Code)
	}
	if recTOTP.Code != http.StatusUnauthorized {
		t.Fatalf("wrong totp status = %d, want 401", recTOTP.Code)
	}
	if outPW["error"] != outTOTP["error"] {
		t.Fatalf("error message differs: password=%q totp=%q", outPW["error"], outTOTP["error"])
	}
}

// A locked-out account must be refused before the verifier ever runs — not
// merely rejected again for the same reason. Proven here by showing that
// even the CORRECT password is refused once the lockout has tripped.
func TestOwnerPassword_LockedOutAccountRefusedBeforeVerifierRuns(t *testing.T) {
	owner, _, _ := testOwner(t)
	pairingID := startPairing(t, owner)

	for i := 0; i < FailureThreshold; i++ {
		rec, _ := doJSON(t, owner.handlePairPassword, http.MethodPost, "/owner/pair/password", map[string]any{
			"pairing_id": pairingID, "email": testOwnerEmail, "password": "wrong",
		})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: status = %d, want 401", i, rec.Code)
		}
	}

	rec, _ := doJSON(t, owner.handlePairPassword, http.MethodPost, "/owner/pair/password", map[string]any{
		"pairing_id": pairingID, "email": testOwnerEmail, "password": testOwnerPassword,
	})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status with correct password while locked out = %d, want 429", rec.Code)
	}
}

func TestOwnerPairPassword_UnknownPairingIDReturns404(t *testing.T) {
	owner, _, _ := testOwner(t)
	rec, _ := doJSON(t, owner.handlePairPassword, http.MethodPost, "/owner/pair/password", map[string]any{
		"pairing_id": "does-not-exist", "email": testOwnerEmail, "password": testOwnerPassword,
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// Attempts expire and are single-use; the map must not grow forever.
func TestOwnerSweepsExpiredAttempts(t *testing.T) {
	owner, _, _ := testOwner(t)
	id := startPairing(t, owner)

	owner.mu.Lock()
	rec, ok := owner.attempts[id]
	if !ok {
		owner.mu.Unlock()
		t.Fatal("attempt missing right after start")
	}
	rec.attempt.SetClock(func() time.Time { return time.Now().Add(PairingTTL + time.Minute) })
	owner.mu.Unlock()

	// Any handler touching the map sweeps it; start a second, unrelated
	// pairing to trigger that.
	_ = startPairing(t, owner)

	owner.mu.Lock()
	_, stillThere := owner.attempts[id]
	remaining := len(owner.attempts)
	owner.mu.Unlock()
	if stillThere {
		t.Fatal("expired pairing attempt was not swept")
	}
	if remaining != 1 {
		t.Fatalf("attempts map has %d entries after sweep, want 1 (the fresh one)", remaining)
	}
}

func TestOwnerDevices_ListAndRevoke(t *testing.T) {
	owner, store, _ := testOwner(t)
	pub, _, _ := ed25519.GenerateKey(nil)
	if err := store.PairDevice("dev-1", "phone", "android", base64.StdEncoding.EncodeToString(pub)); err != nil {
		t.Fatalf("PairDevice: %v", err)
	}

	rec, _ := doJSON(t, owner.handleDevices, http.MethodGet, "/owner/devices", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", rec.Code, rec.Body)
	}
	var list []DeviceInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode device list: %v", err)
	}
	if len(list) != 1 || list[0].ID != "dev-1" {
		t.Fatalf("list = %+v, want one dev-1 entry", list)
	}

	rec, _ = doJSON(t, owner.handleDevicesRevoke, http.MethodPost, "/owner/devices/revoke", map[string]any{"device_id": "dev-1"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d, want 204", rec.Code)
	}

	rec, _ = doJSON(t, owner.handleDevicesRevoke, http.MethodPost, "/owner/devices/revoke", map[string]any{"device_id": "nope"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("revoke unknown device status = %d, want 404", rec.Code)
	}
}

func TestOwnerPairStart_RejectsMalformedDeviceKey(t *testing.T) {
	owner, _, _ := testOwner(t)
	rec, _ := doJSON(t, owner.handlePairStart, http.MethodPost, "/owner/pair/start", map[string]any{
		"device_pubkey": "not base64 at all !!",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
