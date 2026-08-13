package identity

import (
	"errors"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"
)

// testWebAuthnUser is the minimal webauthn.User implementation needed to
// drive BeginRegistration/BeginLogin in tests.
type testWebAuthnUser struct {
	id   []byte
	name string
}

func (u testWebAuthnUser) WebAuthnID() []byte          { return u.id }
func (u testWebAuthnUser) WebAuthnName() string        { return u.name }
func (u testWebAuthnUser) WebAuthnDisplayName() string { return u.name }

// WebAuthnCredentials returns one fake, already-registered credential:
// BeginLogin (a non-discoverable ceremony) needs at least one credential to
// build its allow-list from, and this test is only checking that the
// options carry the expected RP ID, not exercising a real registration.
func (u testWebAuthnUser) WebAuthnCredentials() []webauthn.Credential {
	return []webauthn.Credential{{ID: []byte("fake-credential-id")}}
}

// D-04 (docs/ARCHITECTURE.md Section 12) is still open, so an unconfigured
// verifier must refuse everything, not silently fall back to a guessed
// domain.
func TestUnconfiguredWebAuthnRefusesHard(t *testing.T) {
	v := NewWebAuthnVerifier("", "ClaudeCode Remote", nil)
	if v.Configured() {
		t.Fatal("verifier reports configured with an empty RP ID")
	}

	user := testWebAuthnUser{id: []byte("user-1"), name: "owner"}

	if err := v.Verify([]byte("anything")); !errors.Is(err, ErrRelyingPartyNotConfigured) || !errors.Is(err, ErrFactorNotImplemented) {
		t.Fatalf("Verify on unconfigured verifier = %v, want an error wrapping both ErrRelyingPartyNotConfigured and ErrFactorNotImplemented", err)
	}
	if _, _, err := v.BeginRegistration(user); !errors.Is(err, ErrRelyingPartyNotConfigured) {
		t.Fatalf("BeginRegistration on unconfigured verifier = %v, want ErrRelyingPartyNotConfigured", err)
	}
	if _, _, err := v.BeginLogin(user); !errors.Is(err, ErrRelyingPartyNotConfigured) {
		t.Fatalf("BeginLogin on unconfigured verifier = %v, want ErrRelyingPartyNotConfigured", err)
	}
}

func TestConfiguredWebAuthnBuildsOptionsWithExpectedRPID(t *testing.T) {
	const rpID = "remote.example.test"
	v := NewWebAuthnVerifier(rpID, "ClaudeCode Remote", []string{"https://" + rpID})
	if !v.Configured() {
		t.Fatal("verifier reports unconfigured with a valid RP ID")
	}

	user := testWebAuthnUser{id: []byte("user-1"), name: "owner"}

	creation, session, err := v.BeginRegistration(user)
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	if session == nil {
		t.Fatal("BeginRegistration returned a nil session")
	}
	if got := creation.Response.RelyingParty.ID; got != rpID {
		t.Fatalf("registration RP ID = %q, want %q", got, rpID)
	}

	assertion, loginSession, err := v.BeginLogin(user)
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	if loginSession == nil {
		t.Fatal("BeginLogin returned a nil session")
	}
	if got := assertion.Response.RelyingPartyID; got != rpID {
		t.Fatalf("login RP ID = %q, want %q", got, rpID)
	}
}

func TestWebAuthnVerifyBeforeBindSession(t *testing.T) {
	const rpID = "remote.example.test"
	v := NewWebAuthnVerifier(rpID, "ClaudeCode Remote", []string{"https://" + rpID})

	if err := v.Verify([]byte("anything")); !errors.Is(err, ErrPasskeyNotBound) {
		t.Fatalf("Verify before BindSession = %v, want ErrPasskeyNotBound", err)
	}
}
