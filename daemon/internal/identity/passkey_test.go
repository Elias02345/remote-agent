package identity

import (
	"net/http"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/pquerna/otp/totp"
)

// A wrong password during the bootstrap window must not register anything
// — the same "wrong credentials never advance state" rule the pairing
// chain itself enforces.
func TestPasskeyRegisterBootstrap_WrongPasswordRefused(t *testing.T) {
	owner, store, secret := testOwner(t)
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}

	rec, _ := doJSON(t, owner.HandlePasskeyRegisterBegin, http.MethodPost, "/owner/passkey/register/begin", map[string]any{
		"email": testOwnerEmail, "password": "definitely-wrong", "code": code,
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body)
	}
	n, err := store.CountWebAuthnCredentials()
	if err != nil {
		t.Fatalf("CountWebAuthnCredentials: %v", err)
	}
	if n != 0 {
		t.Fatal("a credential was registered despite the wrong password")
	}
}

// Same as above, for a wrong TOTP code with the correct password.
func TestPasskeyRegisterBootstrap_WrongTOTPRefused(t *testing.T) {
	owner, store, _ := testOwner(t)

	rec, _ := doJSON(t, owner.HandlePasskeyRegisterBegin, http.MethodPost, "/owner/passkey/register/begin", map[string]any{
		"email": testOwnerEmail, "password": testOwnerPassword, "code": "000000",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body)
	}
	n, err := store.CountWebAuthnCredentials()
	if err != nil {
		t.Fatalf("CountWebAuthnCredentials: %v", err)
	}
	if n != 0 {
		t.Fatal("a credential was registered despite the wrong TOTP code")
	}
}

// The door shuts for good the moment one passkey exists: no amount of
// correct email+password+TOTP reopens the unauthenticated bootstrap path.
func TestPasskeyRegisterBootstrap_ClosesAfterFirstCredential(t *testing.T) {
	owner, store, secret := testOwner(t)
	if err := store.AddWebAuthnCredential("existing", &webauthn.Credential{ID: []byte("existing-cred")}); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	rec, _ := doJSON(t, owner.HandlePasskeyRegisterBegin, http.MethodPost, "/owner/passkey/register/begin", map[string]any{
		"email": testOwnerEmail, "password": testOwnerPassword, "code": code,
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body)
	}
}

// The passkey step must be last: nothing may start an assertion ceremony
// for a pairing attempt before password and TOTP are both already proven
// for that specific attempt.
func TestPairPasskeyBegin_RequiresPasswordAndTOTPFirst(t *testing.T) {
	owner, _, _ := testOwner(t)
	pairingID := startPairing(t, owner)

	rec, _ := doJSON(t, owner.handlePairPasskeyBegin, http.MethodPost, "/owner/pair/passkey/begin", map[string]any{
		"pairing_id": pairingID,
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body)
	}
}

// A sign count that fails to increase past a nonzero stored value is the
// textbook signal of a cloned authenticator; it must fail the factor, not
// merely log a warning and continue.
func TestRecordPasskeyUse_RefusesNonIncreasingSignCount(t *testing.T) {
	owner, store, _ := testOwner(t)
	credID := []byte("cred-1")
	if err := store.AddWebAuthnCredential("test key", &webauthn.Credential{
		ID:            credID,
		Authenticator: webauthn.Authenticator{SignCount: 5},
	}); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	err := owner.recordPasskeyUse(&webauthn.Credential{
		ID:            credID,
		Authenticator: webauthn.Authenticator{SignCount: 5}, // not greater than stored
	})
	if err == nil {
		t.Fatal("recordPasskeyUse accepted a non-increasing sign count")
	}
}

// Zero-to-zero must NOT be treated as a clone signal: many authenticators
// never implement a counter at all and always report 0.
func TestRecordPasskeyUse_ZeroToZeroIsNotACloneSignal(t *testing.T) {
	owner, store, _ := testOwner(t)
	credID := []byte("cred-1")
	if err := store.AddWebAuthnCredential("test key", &webauthn.Credential{
		ID:            credID,
		Authenticator: webauthn.Authenticator{SignCount: 0},
	}); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	err := owner.recordPasskeyUse(&webauthn.Credential{
		ID:            credID,
		Authenticator: webauthn.Authenticator{SignCount: 0},
	})
	if err != nil {
		t.Fatalf("recordPasskeyUse refused a legitimate zero-counter authenticator: %v", err)
	}
}

// A genuinely increasing sign count must be accepted and persisted.
func TestRecordPasskeyUse_AcceptsIncreasingSignCount(t *testing.T) {
	owner, store, _ := testOwner(t)
	credID := []byte("cred-1")
	if err := store.AddWebAuthnCredential("test key", &webauthn.Credential{
		ID:            credID,
		Authenticator: webauthn.Authenticator{SignCount: 5},
	}); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	if err := owner.recordPasskeyUse(&webauthn.Credential{
		ID:            credID,
		Authenticator: webauthn.Authenticator{SignCount: 6},
	}); err != nil {
		t.Fatalf("recordPasskeyUse refused a legitimate increasing sign count: %v", err)
	}

	creds, err := store.ListWebAuthnCredentials()
	if err != nil {
		t.Fatalf("ListWebAuthnCredentials: %v", err)
	}
	if len(creds) != 1 || creds[0].Authenticator.SignCount != 6 {
		t.Fatalf("sign count not persisted: %+v", creds)
	}
}

// D-04 open, RP ID unset: bootstrap registration must fail even with
// correct email+password+TOTP.
func TestPasskeyRegisterBootstrap_FailsClosedWithoutRPID(t *testing.T) {
	owner, _, secret := testOwner(t)

	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	rec, _ := doJSON(t, owner.HandlePasskeyRegisterBegin, http.MethodPost, "/owner/passkey/register/begin", map[string]any{
		"email": testOwnerEmail, "password": testOwnerPassword, "code": code,
	})
	if rec.Code == http.StatusOK {
		t.Fatal("bootstrap registration succeeded with no WebAuthn RP ID configured (D-04 still open)")
	}
}

// D-04 open, RP ID unset: the pairing chain's passkey assertion step must
// fail even once password and TOTP are both satisfied.
func TestPairPasskeyBegin_FailsClosedWithoutRPID(t *testing.T) {
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
	rec, _ = doJSON(t, owner.handlePairTOTP, http.MethodPost, "/owner/pair/totp", map[string]any{
		"pairing_id": pairingID, "code": code,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("totp: status = %d, body=%s", rec.Code, rec.Body)
	}

	rec, _ = doJSON(t, owner.handlePairPasskeyBegin, http.MethodPost, "/owner/pair/passkey/begin", map[string]any{
		"pairing_id": pairingID,
	})
	if rec.Code == http.StatusOK {
		t.Fatal("passkey assertion begin succeeded with no WebAuthn RP ID configured (D-04 still open)")
	}
}

// GET /owner/pair/status must not leak anything about a pairing id that
// was never issued, beyond a plain 404.
func TestPairStatus_UnknownPairingIDReturns404(t *testing.T) {
	owner, _, _ := testOwner(t)
	rec, _ := doJSON(t, owner.handlePairStatus, http.MethodGet, "/owner/pair/status?pairing_id=nope", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// A fresh attempt reports all three factors outstanding and not completed.
func TestPairStatus_ReportsOutstandingFactors(t *testing.T) {
	owner, _, _ := testOwner(t)
	pairingID := startPairing(t, owner)

	rec, out := doJSON(t, owner.handlePairStatus, http.MethodGet, "/owner/pair/status?pairing_id="+pairingID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	outstanding, _ := out["outstanding"].([]any)
	if len(outstanding) != 3 {
		t.Fatalf("outstanding = %v, want all 3 factors", outstanding)
	}
	if completed, _ := out["completed"].(bool); completed {
		t.Fatal("a fresh pairing attempt reports completed = true")
	}
	if _, leaked := out["device_id"]; leaked {
		t.Fatal("pair/status leaked a device_id field")
	}
}
