// owner.go implements the /owner pairing routes (D-05: a route in this
// daemon, not a CloudGate module — see docs/ARCHITECTURE.md Section 10.2
// and ROADMAP.md's decision log) plus the paired-device management routes
// that live alongside them.
//
// Every pairing decision flows through identity.Attempt (pairing.go).
// Owner never keeps its own "which factors are done" bookkeeping — Attempt
// is the single source of truth, and Attempt.Complete is the only thing
// that may report a device as paired. Two half-enforcements that could
// disagree is exactly the failure mode CLAUDE.md rules out for this chain.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/go-webauthn/webauthn/webauthn"
)

// ownerDeviceStore is everything Owner needs to persist and manage paired
// devices and registered passkeys. Defined here, by the consumer, rather
// than in store.go, the implementer — idiomatic Go, and it keeps this file
// from needing to know Store is backed by *db.DB at all. *Store satisfies
// it.
type ownerDeviceStore interface {
	PairDevice(id, name, platform, pubKeyB64 string) error
	ListDevices() ([]DeviceInfo, error)
	RevokeDevice(id string) error

	OwnerUserHandle() ([]byte, error)
	AddWebAuthnCredential(name string, cred *webauthn.Credential) error
	ListWebAuthnCredentials() ([]webauthn.Credential, error)
	CountWebAuthnCredentials() (int, error)
	UpdateWebAuthnSignCount(id string, count uint32) error
	TouchWebAuthnCredential(id string) error
}

// OwnerConfig configures the single owner account devices pair against.
// Any zero-valued factor field intentionally leaves that factor
// unsatisfiable — the same fail-closed default NewAttempt already applies
// to a factor nobody supplied a Verifier for (pairing.go). There is no
// bootstrap-time bypass here, only "not configured yet".
type OwnerConfig struct {
	// Email identifies the single owner account. It is also the account
	// key the rate limiter locks out (Section 10.2: per IP *and* per
	// account).
	Email string
	// PasswordHash is an Argon2id hash produced by HashPassword. Empty
	// leaves the password factor permanently unsatisfiable.
	PasswordHash string
	// TOTPSecret is the raw RFC 6238 secret from GenerateTOTPSecret. Empty
	// leaves the TOTP factor permanently unsatisfiable.
	TOTPSecret string
	// WebAuthnRPID is the WebAuthn relying-party ID. D-04: this is a
	// per-installation value (CCR_PUBLIC_DOMAIN / --public-domain in
	// daemon/cmd/claudecode-remoted), never a default this repo invents —
	// leave it empty until the operator has actually set that. See
	// NewWebAuthnVerifier's doc comment for why.
	WebAuthnRPID        string
	WebAuthnDisplayName string
	WebAuthnOrigins     []string
	// TrustedProxies lists CIDRs allowed to set forwarded-for headers. Empty
	// means every request is rate-limited against its own RemoteAddr — see
	// ClientIPResolver for why that is the right default and the wrong answer
	// behind CloudGate.
	TrustedProxies []string
}

// Limits on one in-progress pairing attempt.
//
// Pairing is unauthenticated by necessity — a device has no credentials until
// it pairs — so everything /owner/pair/start accepts is attacker-controlled and
// retained for PairingTTL. Without a cap on the metadata and on the number of
// attempts, a caller with no credentials can pin arbitrary memory in the
// daemon: the request body limit is a megabyte, the attempts map was unbounded,
// and each entry survives ten minutes.
//
// The numbers are deliberately far above any real client: a device name is a
// phone model, a platform is "android".
const (
	maxDeviceNameLen = 128
	maxPlatformLen   = 64
	// maxPairingAttempts bounds the whole map. A single owner pairing a
	// handful of devices never approaches this; a flood hits it immediately.
	maxPairingAttempts = 32
)

// ErrTooManyPairingAttempts is returned once maxPairingAttempts unexpired
// attempts already exist.
var ErrTooManyPairingAttempts = errors.New("too many pairing attempts in progress")

// pairingRecord is one in-progress pairing attempt plus the device
// metadata that arrived with it at /owner/pair/start. Attempt itself
// (pairing.go) has no Name/Platform fields — it only tracks the pairing
// chain — so that metadata is carried alongside it here until Complete.
type pairingRecord struct {
	attempt    *Attempt
	deviceName string
	platform   string

	// passkeyAdap is this attempt's FactorPasskey verifier — one instance
	// per attempt (webauthn.go's boundPasskeyVerifier doc comment explains
	// why it cannot be shared). handlePairStart creates and registers it;
	// /owner/pair/passkey/begin binds it once BeginLogin succeeds;
	// /owner/pair/passkey/finish drives it indirectly through
	// Attempt.Satisfy, via satisfyFactor.
	passkeyAdap *boundPasskeyVerifier
}

// Owner drives the /owner/pair/* and /owner/devices* routes.
//
// Attempt (pairing.go) documents no concurrency guarantee of its own, so
// every read or mutation of a pairingRecord's Attempt happens with mu held
// for the whole operation, not just around the map lookup. Pairing is a
// rare, single-owner, low-traffic flow — holding one coarse lock across a
// Satisfy/Complete call is not a bottleneck here the way it would be on
// the terminal WebSocket.
//
// ponytail: one global mutex, not one lock per attempt. Add per-attempt
// locking only if pairing traffic ever stops being "a handful of requests
// while the owner is standing in front of the app."
type Owner struct {
	email string

	passwordVerifier *PasswordVerifier // nil if not configured
	totpVerifier     *TOTPVerifier     // nil if not configured
	passkeyVerifier  *WebAuthnVerifier // never nil; Configured() may be false

	store    ownerDeviceStore
	limiter  *RateLimiter
	resolver *ClientIPResolver

	mu       sync.Mutex
	attempts map[string]*pairingRecord

	// regMu/regSessions track in-progress WebAuthn *registration*
	// ceremonies (passkey_http.go) — a separate concern and a separate
	// lock from pairing attempts above: registration can happen before any
	// device is paired at all (the bootstrap window) and again afterwards
	// per enrolled passkey, on its own lifecycle.
	regMu       sync.Mutex
	regSessions map[string]*regSession
}

// NewOwner builds the pairing/device-management handlers for cfg, backed by
// store and limiter. limiter is expected to be shared with the rest of the
// daemon's auth surface — an account-keyed lockout means one counter per
// account, not one per call site.
func NewOwner(cfg OwnerConfig, store ownerDeviceStore, limiter *RateLimiter) *Owner {
	o := &Owner{
		email:           cfg.Email,
		store:           store,
		limiter:         limiter,
		resolver:        NewClientIPResolver(cfg.TrustedProxies),
		attempts:        make(map[string]*pairingRecord),
		regSessions:     make(map[string]*regSession),
		passkeyVerifier: NewWebAuthnVerifier(cfg.WebAuthnRPID, cfg.WebAuthnDisplayName, cfg.WebAuthnOrigins),
	}
	if cfg.PasswordHash != "" {
		o.passwordVerifier = NewPasswordVerifier(cfg.PasswordHash)
	}
	if cfg.TOTPSecret != "" {
		o.totpVerifier = NewTOTPVerifier(cfg.TOTPSecret)
	}
	return o
}

// verifiers builds the password/TOTP half of the map NewAttempt expects. A
// factor whose verifier is nil is simply omitted — NewAttempt's own
// fallback (pairing.go) then substitutes the fail-closed "not implemented"
// default, so there is exactly one place in the codebase that decides what
// an absent factor does, and this file does not duplicate it.
//
// FactorPasskey is deliberately NOT set here: it needs one fresh
// *boundPasskeyVerifier per attempt (webauthn.go), so handlePairStart adds
// it itself after calling this.
func (o *Owner) verifiers() map[Factor]Verifier {
	m := map[Factor]Verifier{}
	if o.passwordVerifier != nil {
		m[FactorPassword] = o.passwordVerifier
	}
	if o.totpVerifier != nil {
		m[FactorTOTP] = o.totpVerifier
	}
	return m
}

// sweepLocked drops every expired attempt. Called with mu held, at the top
// of every handler that touches o.attempts, so the map stays bounded by
// PairingTTL instead of growing forever with abandoned pairing attempts.
//
// ponytail: sweeping on access, not on a ticker. The bound is the same
// either way (PairingTTL), and a ticker is one more goroutine to start,
// stop, and reason about for a map that is never large in the first place
// (a handful of concurrent pairing attempts on a single-owner system).
func (o *Owner) sweepLocked() {
	for id, rec := range o.attempts {
		if rec.attempt.Expired() {
			delete(o.attempts, id)
		}
	}
}

func randomID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func (o *Owner) clientIP(r *http.Request) string { return o.resolver.IP(r) }

func writeOwnerJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// RegisterPairing mounts the five pairing routes. They must stay
// unauthenticated: pairing is how a device becomes authenticated in the
// first place, so there is nothing to check device credentials against
// yet.
func (o *Owner) RegisterPairing(mux *http.ServeMux) {
	mux.HandleFunc("/owner/pair/start", o.handlePairStart)
	mux.HandleFunc("/owner/pair/password", o.handlePairPassword)
	mux.HandleFunc("/owner/pair/totp", o.handlePairTOTP)
	mux.HandleFunc("/owner/pair/passkey/begin", o.handlePairPasskeyBegin)
	mux.HandleFunc("/owner/pair/passkey", o.handlePairPasskey)
	mux.HandleFunc("/owner/pair/complete", o.handlePairComplete)
	mux.HandleFunc("/owner/pair/status", o.handlePairStatus)
}

// RegisterDeviceManagement mounts /owner/devices and /owner/devices/revoke.
// The caller (cmd/claudecode-remoted/main.go) is responsible for wrapping
// whatever it mounts these on with device auth and a step-up requirement —
// Section 10.1 calls these out as needing step-up even from an
// already-paired device, and that is wiring, not something owner.go can
// enforce on itself without knowing the daemon's transport.
func (o *Owner) RegisterDeviceManagement(mux *http.ServeMux) {
	mux.HandleFunc("/owner/devices", o.handleDevices)
	mux.HandleFunc("/owner/devices/revoke", o.handleDevicesRevoke)
}

func (o *Owner) handlePairStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAuthError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		DevicePubKey string `json:"device_pubkey"`
		DeviceName   string `json:"device_name"`
		Platform     string `json:"platform"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Reject a malformed device key at the door, the same way Store.Device
	// does for one already in the database (store.go): a key that isn't
	// even the right shape must never be allowed to start a pairing
	// attempt that could later be completed with it.
	raw, err := base64.StdEncoding.DecodeString(body.DevicePubKey)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		writeAuthError(w, http.StatusBadRequest, "device_pubkey must be a base64-encoded ed25519 public key")
		return
	}

	// Metadata is attacker-supplied and retained for PairingTTL, so it is
	// capped rather than trusted. Truncating silently would be worse than
	// refusing: the name shown next to a paired device is how the owner
	// recognises it later, and quietly storing half of it invites confusion
	// exactly where confusion is expensive.
	if len(body.DeviceName) > maxDeviceNameLen {
		writeAuthError(w, http.StatusBadRequest, "device_name is too long")
		return
	}
	if len(body.Platform) > maxPlatformLen {
		writeAuthError(w, http.StatusBadRequest, "platform is too long")
		return
	}

	id, err := randomID()
	if err != nil {
		writeAuthError(w, http.StatusInternalServerError, "failed to start pairing")
		return
	}

	// One passkey adapter per attempt (webauthn.go's boundPasskeyVerifier
	// doc comment explains why a shared one is wrong). Its callback is
	// wired once, here, rather than per-request later — recordPasskeyUse
	// only needs o.store, which never changes for the lifetime of Owner.
	passkeyAdap := newBoundPasskeyVerifier(o.passkeyVerifier)
	passkeyAdap.setOnValidated(o.recordPasskeyUse)

	verifiers := o.verifiers()
	verifiers[FactorPasskey] = passkeyAdap
	attempt := NewAttempt(id, body.DevicePubKey, verifiers)

	o.mu.Lock()
	o.sweepLocked()
	full := len(o.attempts) >= maxPairingAttempts
	if !full {
		o.attempts[id] = &pairingRecord{
			attempt: attempt, deviceName: body.DeviceName, platform: body.Platform,
			passkeyAdap: passkeyAdap,
		}
	}
	o.mu.Unlock()

	if full {
		// Refusing the *new* attempt rather than evicting an old one is the
		// safe direction: eviction would let a flood push out the owner's
		// genuine half-finished pairing, turning a memory bound into a way to
		// cancel someone else's ceremony.
		o.limiter.RecordIPFailure(o.clientIP(r))
		writeAuthError(w, http.StatusTooManyRequests, ErrTooManyPairingAttempts.Error())
		return
	}

	writeOwnerJSON(w, http.StatusOK, map[string]any{
		"pairing_id":  id,
		"outstanding": attempt.Outstanding(),
	})
}

func (o *Owner) handlePairPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAuthError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		PairingID string `json:"pairing_id"`
		Email     string `json:"email"`
		Password  string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	ip := o.clientIP(r)
	if err := o.limiter.Allow(ip, o.email); err != nil {
		writeAuthError(w, http.StatusTooManyRequests, "too many failed attempts")
		return
	}
	if o.email == "" || !strings.EqualFold(body.Email, o.email) {
		// The response is identical to a wrong password — this endpoint must
		// not let a caller tell "wrong email" from "wrong password" — but the
		// *accounting* is not.
		//
		// Charging a mismatched email to the account counter meant anyone
		// could lock the owner out of their own machine with five requests
		// carrying a made-up address, without knowing the real one. The
		// lockout is supposed to make guessing the owner's credentials
		// expensive; it must not make denying the owner service cheap. A
		// caller who has not named the account has not made an attempt
		// against it, so this costs the IP only.
		o.limiter.RecordIPFailure(ip)
		writeAuthError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	o.satisfyFactor(w, ip, body.PairingID, FactorPassword, []byte(body.Password))
}

func (o *Owner) handlePairTOTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAuthError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		PairingID string `json:"pairing_id"`
		Code      string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	ip := o.clientIP(r)
	if err := o.limiter.Allow(ip, o.email); err != nil {
		writeAuthError(w, http.StatusTooManyRequests, "too many failed attempts")
		return
	}
	o.satisfyFactor(w, ip, body.PairingID, FactorTOTP, []byte(body.Code))
}

// handlePairPasskeyBegin starts the pairing chain's third-factor WebAuthn
// assertion ceremony. It refuses unless FactorPassword and FactorTOTP are
// already satisfied for this exact attempt — the passkey step must be
// last, so an unauthenticated caller cannot spend a WebAuthn challenge (or
// learn anything about the RP's configuration) before proving the first
// two factors. On success it binds the attempt's per-attempt adapter
// (pairingRecord.passkeyAdap) to the session BeginLogin returned, so the
// later assertion in handlePairPasskey has something to validate against.
func (o *Owner) handlePairPasskeyBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAuthError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		PairingID string `json:"pairing_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	ip := o.clientIP(r)
	if err := o.limiter.Allow(ip, o.email); err != nil {
		writeAuthError(w, http.StatusTooManyRequests, "too many failed attempts")
		return
	}

	o.mu.Lock()
	o.sweepLocked()
	rec, ok := o.attempts[body.PairingID]
	var ready bool
	if ok {
		ready = rec.attempt.Satisfied(FactorPassword) && rec.attempt.Satisfied(FactorTOTP)
	}
	o.mu.Unlock()

	if !ok {
		writeAuthError(w, http.StatusNotFound, "unknown pairing attempt")
		return
	}
	if !ready {
		writeAuthError(w, http.StatusConflict, "password and totp must be satisfied before the passkey step")
		return
	}

	user, err := o.passkeyUser()
	if err != nil {
		writeAuthError(w, http.StatusInternalServerError, "failed to prepare passkey ceremony")
		return
	}
	assertion, session, err := o.passkeyVerifier.BeginLogin(user)
	if err != nil {
		// Unconfigured RP ID (D-04), or no credential registered yet to
		// build an allow-list from: fail closed, no fallback.
		writeAuthError(w, http.StatusServiceUnavailable, "passkey ceremony unavailable")
		return
	}

	o.mu.Lock()
	rec, ok = o.attempts[body.PairingID]
	if ok {
		rec.passkeyAdap.bind(user, session)
	}
	o.mu.Unlock()
	if !ok {
		// Expired or completed by another request in the time BeginLogin
		// took to run.
		writeAuthError(w, http.StatusGone, "pairing attempt expired")
		return
	}

	writeOwnerJSON(w, http.StatusOK, assertion)
}

// handlePairPasskey completes the pairing chain's third factor: the
// browser's navigator.credentials.get() response, validated against
// whatever handlePairPasskeyBegin bound this attempt's adapter to.
// satisfyFactor already does everything factor-specific handling needs —
// including, via the adapter's onValidated callback, persisting the
// credential's updated sign counter and refusing a cloned authenticator
// (recordPasskeyUse, below) — so this handler is no different in shape
// from handlePairPassword or handlePairTOTP.
func (o *Owner) handlePairPasskey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAuthError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		PairingID string          `json:"pairing_id"`
		Assertion json.RawMessage `json:"assertion"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	ip := o.clientIP(r)
	if err := o.limiter.Allow(ip, o.email); err != nil {
		writeAuthError(w, http.StatusTooManyRequests, "too many failed attempts")
		return
	}
	// No bypass, no dev flag: an attempt whose adapter was never bound
	// (handlePairPasskeyBegin never called, or it failed — e.g. D-04 still
	// open) fails with ErrPasskeyNotBound (webauthn.go), which
	// satisfyFactor's generic failure branch handles exactly like a wrong
	// password. That is correct and deliberate, not a gap to fill in
	// later.
	o.satisfyFactor(w, ip, body.PairingID, FactorPasskey, body.Assertion)
}

// handlePairStatus reports one pairing attempt's progress without
// authenticating the caller — the pairing_id itself, an unguessable token
// handed out by /owner/pair/start, is what authorizes reading it. The
// response deliberately excludes the device id and public key: nothing
// about the attempt beyond "these factors remain" and "is it done" is
// this endpoint's business, and a pairing id that does not exist gets
// exactly 404, nothing more specific.
func (o *Owner) handlePairStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAuthError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	pairingID := r.URL.Query().Get("pairing_id")
	if pairingID == "" {
		writeAuthError(w, http.StatusBadRequest, "pairing_id is required")
		return
	}

	o.mu.Lock()
	o.sweepLocked()
	rec, ok := o.attempts[pairingID]
	var outstanding []Factor
	var completed bool
	if ok {
		outstanding = rec.attempt.Outstanding()
		completed = rec.attempt.Completed()
	}
	o.mu.Unlock()

	if !ok {
		writeAuthError(w, http.StatusNotFound, "unknown pairing attempt")
		return
	}
	writeOwnerJSON(w, http.StatusOK, map[string]any{
		"outstanding": outstanding,
		"completed":   completed,
	})
}

// passkeyUser builds the webauthn.User the single owner account presents
// to every WebAuthn ceremony — registration and login alike: the same
// stable handle from OwnerUserHandle, the same identity, backed by
// whatever credentials are actually in the store right now
// (ownerWebAuthnUser.WebAuthnCredentials reads fresh every call — see its
// doc comment in webauthn_user.go).
func (o *Owner) passkeyUser() (webauthn.User, error) {
	handle, err := o.store.OwnerUserHandle()
	if err != nil {
		return nil, fmt.Errorf("load owner user handle: %w", err)
	}
	return newOwnerWebAuthnUser(handle, o.email, o.email, o.store), nil
}

// recordPasskeyUse is the callback every pairing attempt's
// *boundPasskeyVerifier is wired to (handlePairStart). It persists a
// validated assertion's updated sign counter and last-used timestamp, and
// turns a cloned authenticator into a factor failure instead of a silent
// pass.
//
// A sign count that fails to increase past what was last stored — while
// the stored count is itself nonzero — means two authenticators are
// presenting the same credential ID: the textbook signal for a cloned
// passkey. Zero-to-zero is excluded on purpose: many platform
// authenticators never implement a counter at all and report 0 on every
// assertion, and treating that as a clone would permanently break every
// passkey from one of those authenticators after its very first
// successful use.
//
// ponytail: looks up the stored sign count by listing every credential and
// scanning for a matching id, rather than adding a dedicated single-row
// lookup method. The owner has a handful of passkeys at most; add a
// GetWebAuthnCredential if this ever needs to scale past that.
func (o *Owner) recordPasskeyUse(cred *webauthn.Credential) error {
	id := base64.RawURLEncoding.EncodeToString(cred.ID)

	creds, err := o.store.ListWebAuthnCredentials()
	if err != nil {
		return fmt.Errorf("load stored passkeys: %w", err)
	}
	var storedCount uint32
	found := false
	for _, c := range creds {
		if base64.RawURLEncoding.EncodeToString(c.ID) == id {
			storedCount = c.Authenticator.SignCount
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("passkey credential is not registered")
	}

	newCount := cred.Authenticator.SignCount
	if storedCount != 0 && newCount <= storedCount {
		log.Printf("identity: refusing passkey %s: sign count did not increase (stored=%d, presented=%d) - possible cloned authenticator",
			id, storedCount, newCount)
		return fmt.Errorf("passkey sign count did not increase: possible cloned authenticator")
	}

	if err := o.store.UpdateWebAuthnSignCount(id, newCount); err != nil {
		return fmt.Errorf("update sign count: %w", err)
	}
	if err := o.store.TouchWebAuthnCredential(id); err != nil {
		return fmt.Errorf("touch credential: %w", err)
	}
	return nil
}

// satisfyFactor drives Attempt.Satisfy for one factor and reports the
// result. It is the only place that calls Satisfy, so every factor
// endpoint goes through identical bookkeeping: same lookup, same
// rate-limiter accounting, same generic failure response.
func (o *Owner) satisfyFactor(w http.ResponseWriter, ip, pairingID string, factor Factor, evidence []byte) {
	o.mu.Lock()
	o.sweepLocked()
	rec, ok := o.attempts[pairingID]
	var satisfyErr error
	var outstanding []Factor
	if ok {
		satisfyErr = rec.attempt.Satisfy(factor, evidence)
		outstanding = rec.attempt.Outstanding()
	}
	o.mu.Unlock()

	if !ok {
		// Not a credential guess — the pairing_id is a random token handed
		// out by /owner/pair/start, not a username, so telling this apart
		// from "wrong evidence" does not create an enumeration oracle the
		// way a distinct "unknown account" response would.
		writeAuthError(w, http.StatusNotFound, "unknown pairing attempt")
		return
	}

	switch {
	case satisfyErr == nil:
		// Deliberately does NOT clear the failure counters. One satisfied
		// factor is not a completed authentication, and resetting here let
		// the factors launder each other's budget: satisfy the password,
		// counters wiped, five fresh guesses at the TOTP, satisfy the
		// password again, five more. The reset happens once, in
		// handlePairComplete.
		writeOwnerJSON(w, http.StatusOK, map[string]any{"outstanding": outstanding})
	case errors.Is(satisfyErr, ErrPairingExpired):
		writeAuthError(w, http.StatusGone, "pairing attempt expired")
	case errors.Is(satisfyErr, ErrPairingConsumed):
		writeAuthError(w, http.StatusConflict, "pairing attempt already completed")
	case errors.Is(satisfyErr, ErrUnknownFactor):
		writeAuthError(w, http.StatusBadRequest, "unknown factor")
	default:
		// Everything else is evidence that failed to verify: wrong
		// password, wrong or reused TOTP code, or an unconfigured/failed
		// passkey ceremony. One status, one message, for all of them — a
		// wrong password and a wrong TOTP code must be indistinguishable
		// from the outside (same reasoning as ErrDeviceAuthFailed in
		// device.go) — and each one counts against the shared IP+account
		// lockout.
		o.limiter.RecordFailure(ip, o.email)
		writeAuthError(w, http.StatusUnauthorized, "invalid credentials")
	}
}

func (o *Owner) handlePairComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAuthError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		PairingID string `json:"pairing_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	o.mu.Lock()
	o.sweepLocked()
	rec, ok := o.attempts[body.PairingID]
	var completeErr error
	var outstanding []Factor
	var deviceName, platform, devicePub string
	if ok {
		// Complete is the only place that may pair a device (pairing.go).
		// This handler drives it and adds nothing of its own — no second
		// "is it actually ready" check that could ever disagree with
		// Attempt's answer.
		completeErr = rec.attempt.Complete()
		outstanding = rec.attempt.Outstanding()
		deviceName, platform, devicePub = rec.deviceName, rec.platform, rec.attempt.DevicePub
		if completeErr == nil {
			// Remove immediately on success rather than waiting for the
			// next sweep: a completed attempt is consumed and can never
			// succeed again, so there is nothing left to keep it for.
			delete(o.attempts, body.PairingID)
		}
	}
	o.mu.Unlock()

	if !ok {
		writeAuthError(w, http.StatusNotFound, "unknown pairing attempt")
		return
	}
	if completeErr != nil {
		switch {
		case errors.Is(completeErr, ErrPairingIncomplete):
			writeOwnerJSON(w, http.StatusConflict, map[string]any{
				"error":       "pairing is not complete",
				"outstanding": outstanding,
			})
		case errors.Is(completeErr, ErrPairingExpired):
			writeAuthError(w, http.StatusGone, "pairing attempt expired")
		case errors.Is(completeErr, ErrPairingConsumed):
			writeAuthError(w, http.StatusConflict, "pairing attempt already completed")
		default:
			writeAuthError(w, http.StatusInternalServerError, "failed to complete pairing")
		}
		return
	}

	deviceID, err := randomID()
	if err != nil {
		writeAuthError(w, http.StatusInternalServerError, "failed to complete pairing")
		return
	}
	// ponytail: if this write fails, the in-memory attempt was already
	// consumed and removed above, so the client has to restart pairing
	// from scratch rather than retry /owner/pair/complete. A local SQLite
	// write failing after an in-process Complete() succeeded is rare
	// enough (and pairing rare enough) that a two-phase-commit-style retry
	// path is not worth building for this phase; revisit if that ever
	// actually happens in practice.
	if err := o.store.PairDevice(deviceID, deviceName, platform, devicePub); err != nil {
		writeAuthError(w, http.StatusInternalServerError, "failed to store paired device")
		return
	}

	// The one place the counters are forgiven: a device just proved all three
	// factors, so whatever failed guesses preceded it were the owner fumbling,
	// not an attacker making progress.
	o.limiter.RecordSuccess(o.clientIP(r), o.email)

	writeOwnerJSON(w, http.StatusOK, map[string]any{"device_id": deviceID})
}

func (o *Owner) handleDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAuthError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	devices, err := o.store.ListDevices()
	if err != nil {
		writeAuthError(w, http.StatusInternalServerError, "failed to list devices")
		return
	}
	writeOwnerJSON(w, http.StatusOK, devices)
}

func (o *Owner) handleDevicesRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAuthError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.DeviceID == "" {
		writeAuthError(w, http.StatusBadRequest, "device_id is required")
		return
	}
	if err := o.store.RevokeDevice(body.DeviceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAuthError(w, http.StatusNotFound, "no such device")
			return
		}
		writeAuthError(w, http.StatusInternalServerError, "failed to revoke device")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
