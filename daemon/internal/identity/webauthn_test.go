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
// domain — every method, not just some of them.
func TestUnconfiguredWebAuthnRefusesHard(t *testing.T) {
	v := NewWebAuthnVerifier("", "ClaudeCode Remote", nil)
	if v.Configured() {
		t.Fatal("verifier reports configured with an empty RP ID")
	}

	user := testWebAuthnUser{id: []byte("user-1"), name: "owner"}

	if _, _, err := v.BeginRegistration(user); !errors.Is(err, ErrRelyingPartyNotConfigured) || !errors.Is(err, ErrFactorNotImplemented) {
		t.Fatalf("BeginRegistration on unconfigured verifier = %v, want an error wrapping both ErrRelyingPartyNotConfigured and ErrFactorNotImplemented", err)
	}
	if _, _, err := v.BeginLogin(user); !errors.Is(err, ErrRelyingPartyNotConfigured) || !errors.Is(err, ErrFactorNotImplemented) {
		t.Fatalf("BeginLogin on unconfigured verifier = %v, want an error wrapping both ErrRelyingPartyNotConfigured and ErrFactorNotImplemented", err)
	}
	if _, err := v.FinishRegistration(user, webauthn.SessionData{}, []byte("anything")); !errors.Is(err, ErrRelyingPartyNotConfigured) || !errors.Is(err, ErrFactorNotImplemented) {
		t.Fatalf("FinishRegistration on unconfigured verifier = %v, want an error wrapping both ErrRelyingPartyNotConfigured and ErrFactorNotImplemented", err)
	}
	if _, err := v.VerifyAssertion(user, webauthn.SessionData{}, []byte("anything")); !errors.Is(err, ErrRelyingPartyNotConfigured) || !errors.Is(err, ErrFactorNotImplemented) {
		t.Fatalf("VerifyAssertion on unconfigured verifier = %v, want an error wrapping both ErrRelyingPartyNotConfigured and ErrFactorNotImplemented", err)
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

// A malformed registration response body must fail to parse rather than
// being handed to go-webauthn's validator as something that might pass.
func TestFinishRegistrationRejectsMalformedBody(t *testing.T) {
	const rpID = "remote.example.test"
	v := NewWebAuthnVerifier(rpID, "ClaudeCode Remote", []string{"https://" + rpID})
	user := testWebAuthnUser{id: []byte("user-1"), name: "owner"}

	_, session, err := v.BeginRegistration(user)
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	if _, err := v.FinishRegistration(user, *session, []byte("not json")); err == nil {
		t.Fatal("FinishRegistration accepted a malformed body")
	}
}

// Same as above, for the login/assertion side.
func TestVerifyAssertionRejectsMalformedBody(t *testing.T) {
	const rpID = "remote.example.test"
	v := NewWebAuthnVerifier(rpID, "ClaudeCode Remote", []string{"https://" + rpID})
	user := testWebAuthnUser{id: []byte("user-1"), name: "owner"}

	_, session, err := v.BeginLogin(user)
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	if _, err := v.VerifyAssertion(user, *session, []byte("not json")); err == nil {
		t.Fatal("VerifyAssertion accepted a malformed body")
	}
}

// A pairing attempt's passkey adapter must refuse every assertion until
// /owner/pair/passkey/begin has bound it to a real session — this is the
// per-attempt replacement for the old shared-verifier BindSession contract,
// and it must hold the same guarantee: no session, no pass.
func TestBoundPasskeyVerifierRefusesBeforeBind(t *testing.T) {
	v := NewWebAuthnVerifier("remote.example.test", "ClaudeCode Remote", []string{"https://remote.example.test"})
	adapter := newBoundPasskeyVerifier(v)

	if err := adapter.Verify([]byte("anything")); !errors.Is(err, ErrPasskeyNotBound) {
		t.Fatalf("Verify before bind = %v, want ErrPasskeyNotBound", err)
	}
}

// Once bound, the adapter must actually delegate to VerifyAssertion rather
// than, say, always refusing or always passing — proven here by a
// malformed body failing for a parse reason, not ErrPasskeyNotBound.
func TestBoundPasskeyVerifierDelegatesOnceBound(t *testing.T) {
	const rpID = "remote.example.test"
	v := NewWebAuthnVerifier(rpID, "ClaudeCode Remote", []string{"https://" + rpID})
	user := testWebAuthnUser{id: []byte("user-1"), name: "owner"}

	_, session, err := v.BeginLogin(user)
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}

	adapter := newBoundPasskeyVerifier(v)
	adapter.bind(user, session)

	err = adapter.Verify([]byte("not json"))
	if err == nil {
		t.Fatal("Verify with a garbage body succeeded")
	}
	if errors.Is(err, ErrPasskeyNotBound) {
		t.Fatal("Verify still reports unbound after bind was called")
	}
}
