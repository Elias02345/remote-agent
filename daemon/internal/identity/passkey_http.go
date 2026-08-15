// passkey_http.go implements passkey (WebAuthn) *registration* — enrolling
// a new credential — as distinct from the pairing chain's passkey
// *assertion* step in owner.go (proving possession of an existing
// credential). Section 10.2's pairing chain needs a credential to already
// exist before its third factor can ever be satisfied (BeginLogin and
// ValidateLogin both need at least one stored credential to build an
// allow-list from), so this file is what makes that first credential
// possible to create at all.
//
// The authorisation rule is the point of this file, not the WebAuthn
// plumbing:
//
//   - No credential exists yet (CountWebAuthnCredentials() == 0): the
//     bootstrap window. Registration is reachable unauthenticated, gated
//     instead by email + password + TOTP in the request body, checked
//     against the same verifiers and the same RateLimiter as the pairing
//     chain (D-07: no reduced tier).
//   - At least one credential exists: the door is shut, permanently. The
//     bootstrap body (email/password/TOTP) is no longer accepted at all —
//     not even a correct one — and the request must instead arrive already
//     wrapped in device auth + step-up (ActionChangeSecuritySettings) by
//     the router (cmd/claudecode-remoted/main.go mounts the same handlers
//     a second time, at /owner/passkey/enroll/*, behind that middleware).
//
// The count is read fresh on every request, never cached: caching it would
// leave a stale "still zero" window open after the very first passkey is
// registered by another request.
package identity

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// registrationTTL bounds how long a begun-but-unfinished registration
// session stays usable — the same reasoning as PairingTTL (pairing.go).
const registrationTTL = 5 * time.Minute

// maxRegistrationSessions bounds the in-memory session map, the same way
// maxPairingAttempts bounds o.attempts (owner.go). Bootstrap registration
// is unauthenticated by necessity, so without a cap a flood of
// /owner/passkey/register/begin requests could pin arbitrary memory before
// any credential exists to require authentication against.
const maxRegistrationSessions = 8

// ErrTooManyRegistrationSessions is returned once maxRegistrationSessions
// unexpired sessions already exist.
var ErrTooManyRegistrationSessions = errors.New("too many passkey registrations in progress")

// regSession is one in-progress WebAuthn registration ceremony: the exact
// user and challenge FinishRegistration must validate the browser's
// response against.
type regSession struct {
	user      webauthn.User
	session   *webauthn.SessionData
	expiresAt time.Time
}

// sweepRegistrationsLocked drops every expired registration session.
// Called with o.regMu held, at the top of every handler that touches
// o.regSessions — same pattern as sweepLocked for pairing attempts.
func (o *Owner) sweepRegistrationsLocked() {
	now := time.Now()
	for id, s := range o.regSessions {
		if now.After(s.expiresAt) {
			delete(o.regSessions, id)
		}
	}
}

// HandlePasskeyRegisterBegin starts a WebAuthn registration ceremony.
//
// Mounted twice by cmd/claudecode-remoted/main.go: unauthenticated at
// /owner/passkey/register/begin for the bootstrap window, and behind
// identity.RequireDevice + identity.RequireStepUp(ActionChangeSecuritySettings)
// at /owner/passkey/enroll/begin afterwards. See this file's package
// comment for the authorisation rule; DeviceIDFromContext is how this
// method tells the two mounts apart — never anything in the request body,
// which is attacker-controlled on the unauthenticated mount.
func (o *Owner) HandlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAuthError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Read fresh on every request — see the package comment for why this
	// must never be cached.
	count, err := o.store.CountWebAuthnCredentials()
	if err != nil {
		writeAuthError(w, http.StatusInternalServerError, "failed to check registered passkeys")
		return
	}
	_, hasDevice := DeviceIDFromContext(r.Context())

	switch {
	case count > 0 && !hasDevice:
		// The bootstrap window closes forever the moment one credential
		// exists. Not "closed for now" — closed. Every registration after
		// the first one must arrive already wrapped in device auth +
		// step-up by the router, never email+password+TOTP again.
		writeAuthError(w, http.StatusForbidden, "bootstrap registration window is closed: pair a device and use step-up instead")
		return
	case count == 0 && hasDevice:
		// Cannot happen through a real pairing flow — pairing's own
		// passkey factor requires a credential to already exist before it
		// can ever succeed, so a device could not have gotten paired with
		// zero credentials in the store. Refuse rather than guess which
		// rule an inconsistent request like this was meant to follow.
		writeAuthError(w, http.StatusForbidden, "no bootstrap window available on this route")
		return
	}

	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	ip := o.clientIP(r)
	if count == 0 {
		if err := o.limiter.Allow(ip, o.email); err != nil {
			writeAuthError(w, http.StatusTooManyRequests, "too many failed attempts")
			return
		}
		if o.passwordVerifier == nil || o.totpVerifier == nil || o.email == "" {
			writeAuthError(w, http.StatusUnauthorized, "bootstrap registration is not available: owner credentials are not configured")
			return
		}
		// A wrong email costs the IP only, never the account — same
		// reasoning as handlePairPassword (owner.go): with a single
		// account, charging an unnamed guess to it turns the lockout into
		// a denial of service anyone can run against the owner without
		// knowing the real email.
		if !strings.EqualFold(body.Email, o.email) {
			o.limiter.RecordIPFailure(ip)
			writeAuthError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		// Both factors are checked every time, and the result is one
		// indistinguishable error — returning early on a bad password
		// would say which factor failed.
		passwordErr := o.passwordVerifier.Verify([]byte(body.Password))
		totpErr := o.totpVerifier.Verify([]byte(body.Code))
		if passwordErr != nil || totpErr != nil {
			o.limiter.RecordFailure(ip, o.email)
			writeAuthError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		o.limiter.RecordSuccess(ip, o.email)
	}
	// count > 0: this request already passed RequireDevice and
	// RequireStepUp to reach here (the router wired that, not this
	// handler) — nothing further to check.

	user, err := o.passkeyUser()
	if err != nil {
		writeAuthError(w, http.StatusInternalServerError, "failed to prepare passkey registration")
		return
	}
	creation, session, err := o.passkeyVerifier.BeginRegistration(user)
	if err != nil {
		// Unconfigured RP ID (D-04): fail closed, no fallback.
		writeAuthError(w, http.StatusServiceUnavailable, "passkey registration unavailable")
		return
	}

	regID, err := randomID()
	if err != nil {
		writeAuthError(w, http.StatusInternalServerError, "failed to start registration")
		return
	}

	o.regMu.Lock()
	o.sweepRegistrationsLocked()
	full := len(o.regSessions) >= maxRegistrationSessions
	if !full {
		o.regSessions[regID] = &regSession{user: user, session: session, expiresAt: time.Now().Add(registrationTTL)}
	}
	o.regMu.Unlock()

	if full {
		o.limiter.RecordIPFailure(ip)
		writeAuthError(w, http.StatusTooManyRequests, ErrTooManyRegistrationSessions.Error())
		return
	}

	writeOwnerJSON(w, http.StatusOK, map[string]any{
		"registration_id": regID,
		"options":         creation,
	})
}

// HandlePasskeyRegisterFinish completes a WebAuthn registration ceremony
// started by HandlePasskeyRegisterBegin and stores the resulting
// credential.
//
// Mounted twice, exactly like Begin (main.go) — but unlike Begin, Finish
// itself performs no bootstrap-vs-enroll check and no additional step-up.
// The registration_id alone (crypto/rand, single-use, TTL-bounded) is what
// authorizes finishing this specific ceremony: it was only ever handed out
// by a Begin call that already cleared the right gate, so possession of it
// already proves that. Requiring a second step-up grant here as well would
// hit the exact bug the /owner/devices list-vs-revoke split (main.go)
// exists to avoid — step-up grants are single-use, so Begin consuming one
// would leave Finish with nothing to consume of its own.
func (o *Owner) HandlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAuthError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		RegistrationID string          `json:"registration_id"`
		Name           string          `json:"name"`
		Attestation    json.RawMessage `json:"attestation"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(body.Name) > maxDeviceNameLen {
		// Same reasoning as handlePairStart's device_name check
		// (owner.go): truncating silently would be worse than refusing —
		// this label is how the owner recognises the passkey later.
		writeAuthError(w, http.StatusBadRequest, "name is too long")
		return
	}

	o.regMu.Lock()
	o.sweepRegistrationsLocked()
	sess, ok := o.regSessions[body.RegistrationID]
	if ok {
		// Single-use: consumed here regardless of what happens next, so a
		// captured or replayed registration id is worthless after one
		// attempt, success or failure — same reasoning as
		// DeviceAuthenticator.Verify consuming a challenge unconditionally
		// (device.go).
		delete(o.regSessions, body.RegistrationID)
	}
	o.regMu.Unlock()

	if !ok {
		writeAuthError(w, http.StatusNotFound, "unknown or expired registration")
		return
	}

	cred, err := o.passkeyVerifier.FinishRegistration(sess.user, *sess.session, body.Attestation)
	if err != nil {
		writeAuthError(w, http.StatusUnauthorized, "passkey registration failed")
		return
	}

	if err := o.store.AddWebAuthnCredential(body.Name, cred); err != nil {
		writeAuthError(w, http.StatusInternalServerError, "failed to store passkey")
		return
	}

	log.Printf("identity: registered passkey %q", body.Name)
	writeOwnerJSON(w, http.StatusOK, map[string]any{"registered": true})
}
