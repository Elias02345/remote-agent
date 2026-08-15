package identity

import (
	"bytes"
	"errors"
	"fmt"
	"sync"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// ErrRelyingPartyNotConfigured is returned by WebAuthnVerifier when the
// relying-party ID has not been set.
var ErrRelyingPartyNotConfigured = errors.New("webauthn relying party ID is not configured (CCR_PUBLIC_DOMAIN / --public-domain unset for this installation, see D-04 in ROADMAP.md)")

// ErrPasskeyNotBound is returned by a pairing attempt's passkey verifier
// (boundPasskeyVerifier, below) when /owner/pair/passkey/begin has not been
// called for that attempt yet — there is no session to validate an
// assertion against.
var ErrPasskeyNotBound = errors.New("passkey verifier is not bound to a pairing attempt")

// WebAuthnVerifier implements the WebAuthn/passkey ceremony (Section 10.2:
// registration and the pairing chain's third factor). Construction never
// fails — mirroring notImplemented in pairing.go, an unconfigured verifier
// still exists as a value, it simply refuses every call until it has a real
// RP ID, which keeps the whole pairing chain fail-closed instead of
// panicking or being skipped.
//
// WebAuthnVerifier itself holds no per-ceremony state (no user, no
// challenge) — every method takes what it needs as an argument and returns
// what the caller needs to keep. That statelessness is deliberate: an
// earlier version stored a single in-flight user+session on the verifier
// itself (BindSession/Verify, since removed), which meant two pairing
// attempts — or a pairing attempt overlapping a passkey registration — could
// overwrite each other's session before either had validated its ceremony.
// Per-ceremony state now lives with whoever owns that ceremony: one
// *webauthn.SessionData held by owner.go's pairingRecord or regSession, and
// one boundPasskeyVerifier per pairing attempt (below) to adapt that into
// the Verifier interface pairing.go expects.
type WebAuthnVerifier struct {
	wa *webauthn.WebAuthn // nil until a non-empty, valid RP ID configures it
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

// FinishRegistration validates a registration ceremony's raw response body
// (the JSON navigator.credentials.create() returned) against session and
// returns the credential to store.
func (v *WebAuthnVerifier) FinishRegistration(user webauthn.User, session webauthn.SessionData, body []byte) (*webauthn.Credential, error) {
	if v.wa == nil {
		return nil, v.notConfiguredErr()
	}
	parsed, err := protocol.ParseCredentialCreationResponseBody(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse passkey registration response: %w", err)
	}
	cred, err := v.wa.CreateCredential(user, session, parsed)
	if err != nil {
		return nil, fmt.Errorf("create passkey credential: %w", err)
	}
	return cred, nil
}

// VerifyAssertion validates a login ceremony's raw response body (the JSON
// navigator.credentials.get() returned) against session and returns the
// credential that was used, so the caller can inspect and persist its
// updated sign counter.
func (v *WebAuthnVerifier) VerifyAssertion(user webauthn.User, session webauthn.SessionData, body []byte) (*webauthn.Credential, error) {
	if v.wa == nil {
		return nil, v.notConfiguredErr()
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse passkey response: %w", err)
	}
	cred, err := v.wa.ValidateLogin(user, session, parsed)
	if err != nil {
		return nil, fmt.Errorf("validate passkey: %w", err)
	}
	return cred, nil
}

// boundPasskeyVerifier adapts one pairing attempt's WebAuthn login
// ceremony to the Verifier interface (pairing.go: Verify(evidence []byte)
// error).
//
// One instance per pairing attempt, created in handlePairStart and carried
// in that attempt's pairingRecord (owner.go) — never shared between
// attempts. That is what fixes the overlapping-attempt problem the old
// BindSession design had: nothing here is shared between two attempts
// except the stateless *WebAuthnVerifier underneath, so attempt A's
// assertion can never be validated against attempt B's challenge.
//
// user/session start nil and are set exactly once, by bind
// (/owner/pair/passkey/begin, called after BeginLogin has already
// succeeded). Verify refuses with ErrPasskeyNotBound for as long as they
// are unset — the same contract the old BindSession/Verify pair had, just
// scoped to one attempt instead of to the whole verifier.
type boundPasskeyVerifier struct {
	wa *WebAuthnVerifier

	mu      sync.Mutex
	user    webauthn.User
	session *webauthn.SessionData

	// onValidated is called after VerifyAssertion succeeds, before Verify
	// returns, so the caller (owner.go's recordPasskeyUse) can persist the
	// credential's updated sign counter and refuse a cloned authenticator
	// by returning an error of its own — which Verify then reports as this
	// attempt's failure, exactly like a bad signature would be.
	onValidated func(*webauthn.Credential) error
}

func newBoundPasskeyVerifier(wa *WebAuthnVerifier) *boundPasskeyVerifier {
	return &boundPasskeyVerifier{wa: wa}
}

// setOnValidated wires the persistence callback. Called once, at
// construction time (handlePairStart), before the adapter is ever handed to
// NewAttempt.
func (b *boundPasskeyVerifier) setOnValidated(fn func(*webauthn.Credential) error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onValidated = fn
}

// bind attaches the user and challenge for this attempt's passkey ceremony.
// Called once, by /owner/pair/passkey/begin, strictly after BeginLogin has
// already succeeded.
func (b *boundPasskeyVerifier) bind(user webauthn.User, session *webauthn.SessionData) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.user = user
	b.session = session
}

// Verify checks evidence — the raw JSON body returned by the browser's
// navigator.credentials.get() — against whatever bind attached, if
// anything.
func (b *boundPasskeyVerifier) Verify(evidence []byte) error {
	b.mu.Lock()
	user, session, onValidated := b.user, b.session, b.onValidated
	b.mu.Unlock()

	if user == nil || session == nil {
		return ErrPasskeyNotBound
	}
	cred, err := b.wa.VerifyAssertion(user, *session, evidence)
	if err != nil {
		return err
	}
	if onValidated != nil {
		if err := onValidated(cred); err != nil {
			return err
		}
	}
	return nil
}
