package identity

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// ErrRelyingPartyNotConfigured is returned by WebAuthnVerifier when the
// relying-party ID has not been set.
var ErrRelyingPartyNotConfigured = errors.New("webauthn relying party ID is not configured (CCR_PUBLIC_DOMAIN / --public-domain unset for this installation, see D-04 in ROADMAP.md)")

// ErrPasskeyNotBound is returned by Verify when BindSession has not yet
// attached a user and challenge for the current pairing attempt.
var ErrPasskeyNotBound = errors.New("passkey verifier is not bound to a pairing attempt")

// WebAuthnVerifier implements Verifier for the pairing chain's third factor
// (Section 10.2: the WebAuthn/passkey ceremony). Construction never fails —
// mirroring notImplemented in pairing.go, an unconfigured verifier still
// exists as a value, it simply refuses every call until it has a real RP
// ID, which keeps the whole pairing chain fail-closed instead of panicking
// or being skipped.
type WebAuthnVerifier struct {
	wa *webauthn.WebAuthn // nil until a non-empty, valid RP ID configures it

	user    webauthn.User
	session *webauthn.SessionData
}

// NewWebAuthnVerifier builds a passkey verifier for the given relying-party
// ID, display name and origin(s).
//
// rpID and origins must be supplied explicitly, and this constructor must
// never invent a default (not the daemon's Tailscale name, not
// "localhost") when rpID is empty. Which domain is the WebAuthn relying
// party is Decision D-04 in docs/ARCHITECTURE.md Section 12: every
// installation supplies its own via CCR_PUBLIC_DOMAIN / --public-domain,
// and this repo ships no default — a guessed one here could end up live in
// production, and per the paragraph below, a live RP ID cannot be quietly
// swapped out later. With an empty rpID (the operator hasn't set that yet),
// Configured reports false and every other method returns an error
// wrapping ErrRelyingPartyNotConfigured (and ErrFactorNotImplemented), so
// the passkey factor stays unsatisfiable — and pairing therefore stays
// impossible — until the domain is actually configured.
//
// Once real passkeys have been registered against an RP ID, that ID must
// never change: WebAuthn credentials are cryptographically bound to the RP
// ID (and origin) they were created under, so changing it invalidates
// every passkey already registered. Set it once, from the decided domain,
// and never again.
func NewWebAuthnVerifier(rpID, rpDisplayName string, origins []string) *WebAuthnVerifier {
	v := &WebAuthnVerifier{}
	if rpID == "" {
		return v
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          rpID,
		RPDisplayName: rpDisplayName,
		RPOrigins:     origins,
	})
	if err != nil {
		// The config itself was rejected (e.g. malformed origins). Stays
		// unconfigured — same fail-closed path as an empty RP ID.
		return v
	}
	v.wa = wa
	return v
}

// Configured reports whether a usable RP ID has been set.
func (v *WebAuthnVerifier) Configured() bool { return v.wa != nil }

func (v *WebAuthnVerifier) notConfiguredErr() error {
	return fmt.Errorf("%w: %w", ErrFactorNotImplemented, ErrRelyingPartyNotConfigured)
}

// BeginRegistration starts a passkey registration ceremony for user and
// returns the options to send to the client plus the session data the
// caller must keep server-side until FinishRegistration.
func (v *WebAuthnVerifier) BeginRegistration(user webauthn.User) (*protocol.CredentialCreation, *webauthn.SessionData, error) {
	if v.wa == nil {
		return nil, nil, v.notConfiguredErr()
	}
	return v.wa.BeginRegistration(user)
}

// BeginLogin starts a passkey login (assertion) ceremony for user.
func (v *WebAuthnVerifier) BeginLogin(user webauthn.User) (*protocol.CredentialAssertion, *webauthn.SessionData, error) {
	if v.wa == nil {
		return nil, nil, v.notConfiguredErr()
	}
	return v.wa.BeginLogin(user)
}

// BindSession attaches the user and challenge for one pairing attempt, so a
// later Verify call can validate the assertion response against them. Call
// this after BeginLogin and before Attempt.Satisfy(FactorPasskey, ...).
func (v *WebAuthnVerifier) BindSession(user webauthn.User, session *webauthn.SessionData) {
	v.user = user
	v.session = session
}

// Verify checks evidence — the raw JSON body returned by the browser's
// navigator.credentials.get() — against the session attached by
// BindSession.
func (v *WebAuthnVerifier) Verify(evidence []byte) error {
	if v.wa == nil {
		return v.notConfiguredErr()
	}
	if v.user == nil || v.session == nil {
		return ErrPasskeyNotBound
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(evidence))
	if err != nil {
		return fmt.Errorf("parse passkey response: %w", err)
	}
	if _, err := v.wa.ValidateLogin(v.user, *v.session, parsed); err != nil {
		return fmt.Errorf("validate passkey: %w", err)
	}
	return nil
}
