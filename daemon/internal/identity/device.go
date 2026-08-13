// device.go implements the everyday device authentication from
// docs/ARCHITECTURE.md Section 5.4: an already-paired device signs a
// server-issued random challenge with the Ed25519 key it generated at
// pairing time. No secret ever crosses the wire, and — unlike the pairing
// chain in pairing.go — no password or TOTP step is involved, because this
// runs on every connection and has to stay fast.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ChallengeSize is the number of random bytes in an issued challenge. 32
// bytes (256 bits) matches Ed25519's own security level, which is far beyond
// anything a brute-force or birthday-collision attack could reach before the
// challenge expires anyway.
const ChallengeSize = 32

// ChallengeTTL bounds how long an issued challenge stays valid. Two minutes
// is generous enough for a client on a slow mobile connection to sign and
// respond, and short enough that a challenge intercepted in transit is
// useless to a replay attempt by the time anyone could act on it.
const ChallengeTTL = 2 * time.Minute

var (
	// ErrDeviceAuthFailed covers every device-authentication failure that
	// must be indistinguishable from the outside: unknown device id, revoked
	// device, wrong signature, or no outstanding challenge at all. Giving
	// any one of these its own error or its own timing would turn this
	// endpoint into an oracle for which device ids are actually paired.
	ErrDeviceAuthFailed = errors.New("device authentication failed")
	// ErrChallengeExpired is reported on its own because it is about the
	// challenge's lifetime, not about device identity — knowing a challenge
	// expired doesn't tell an attacker anything about which devices exist.
	ErrChallengeExpired = errors.New("challenge expired")
)

// DeviceLookup resolves a paired device id to its stored Ed25519 public key
// and revocation flag. It is an interface rather than a direct dependency on
// package db so that this package stays free of the sqlite driver and tests
// can supply an in-memory fake; the daemon wires a real implementation over
// the `devices` table (db.go) at startup.
type DeviceLookup interface {
	// Device returns the device's public key and revoked flag. ok is false
	// if no device with this id has ever been paired.
	Device(id string) (pubKey ed25519.PublicKey, revoked bool, ok bool)
}

// dummyKey stands in for an unknown device's public key. Verifying against
// it costs the same as verifying against a real key, so refusing an unknown
// device id takes exactly as long as refusing a known one with a bad
// signature — an early return here would otherwise be a timing side channel
// that leaks device existence even though the error message doesn't.
var dummyKey = func() ed25519.PublicKey {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		// crypto/rand failing is not a recoverable condition anywhere in
		// this package; every other key generation in the daemon would be
		// broken too.
		panic("identity: generate dummy key: " + err.Error())
	}
	return pub
}()

// pendingChallenge is one issued-but-not-yet-verified challenge.
type pendingChallenge struct {
	nonce     []byte
	expiresAt time.Time
}

// DeviceAuthenticator issues and verifies the per-connection Ed25519
// challenge-response for already-paired devices. It never handles a private
// key — only ever a signature, checked against the public key DeviceLookup
// returns.
//
// Safe for concurrent use.
type DeviceAuthenticator struct {
	lookup DeviceLookup

	mu      sync.Mutex
	pending map[string]*pendingChallenge // deviceID -> its one outstanding challenge

	nowFunc func() time.Time
}

// NewDeviceAuthenticator builds an authenticator backed by lookup.
func NewDeviceAuthenticator(lookup DeviceLookup) *DeviceAuthenticator {
	return &DeviceAuthenticator{
		lookup:  lookup,
		pending: make(map[string]*pendingChallenge),
		nowFunc: time.Now,
	}
}

// SetClock overrides the clock, for tests.
func (a *DeviceAuthenticator) SetClock(f func() time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nowFunc = f
}

func (a *DeviceAuthenticator) now() time.Time {
	if a.nowFunc == nil {
		return time.Now()
	}
	return a.nowFunc()
}

// IssueChallenge creates a fresh random nonce for deviceID and returns it for
// the caller to send to the device to sign.
//
// A device has at most one outstanding challenge at a time; issuing a new one
// silently discards any prior unconsumed challenge.
//
// ponytail: one outstanding challenge per device, not a set of them. A
// device signs and responds inline in a single request/response cycle —
// nothing in this codebase needs several challenges in flight for the same
// device at once, and tracking a set would be unused flexibility. Add a
// per-challenge-id keyed map if a future flow needs concurrent challenges.
func (a *DeviceAuthenticator) IssueChallenge(deviceID string) ([]byte, error) {
	nonce := make([]byte, ChallengeSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate challenge for device %s: %w", deviceID, err)
	}
	a.mu.Lock()
	a.pending[deviceID] = &pendingChallenge{
		nonce:     nonce,
		expiresAt: a.now().Add(ChallengeTTL),
	}
	a.mu.Unlock()
	return nonce, nil
}

// Verify checks signature against deviceID's outstanding challenge.
//
// The challenge is consumed here unconditionally — on success or failure —
// before the signature is even checked, so a challenge can never be
// verified twice: a captured signature is worthless the moment it has been
// used (or attempted) once, whether or not that attempt succeeded.
//
// An expired challenge is refused before any lookup happens. An unknown
// device id, a revoked device, and a bad signature all return the same
// ErrDeviceAuthFailed and all pay for a real ed25519.Verify call (against a
// dummy key when there is no real one to check), so neither the error nor
// the response time reveals which of those three actually happened.
func (a *DeviceAuthenticator) Verify(deviceID string, signature []byte) error {
	a.mu.Lock()
	pc, hadPending := a.pending[deviceID]
	if hadPending {
		delete(a.pending, deviceID)
	}
	now := a.now()
	a.mu.Unlock()

	if !hadPending {
		// No outstanding challenge — never issued, already consumed, or
		// issued for a device id that was mistyped. Still spend a Verify
		// call so this path costs the same as every other rejection.
		ed25519.Verify(dummyKey, []byte("no outstanding challenge"), signature)
		return ErrDeviceAuthFailed
	}
	if now.After(pc.expiresAt) {
		return ErrChallengeExpired
	}

	pubKey, revoked, ok := a.lookup.Device(deviceID)
	if !ok {
		ed25519.Verify(dummyKey, pc.nonce, signature)
		return ErrDeviceAuthFailed
	}
	if revoked {
		// Verify anyway: a revoked device must not be able to tell, from
		// response time, that its signature would otherwise have passed.
		ed25519.Verify(pubKey, pc.nonce, signature)
		return ErrDeviceAuthFailed
	}
	if !ed25519.Verify(pubKey, pc.nonce, signature) {
		return ErrDeviceAuthFailed
	}
	return nil
}
