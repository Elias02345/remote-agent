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
	"log"
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

// DeviceToucher is an optional extension of DeviceLookup: a lookup that also
// knows how to record when a device was last seen. Verify type-asserts for it
// rather than requiring it, so the in-memory fakes the tests use stay two lines
// long and do not have to implement storage they have no use for.
type DeviceToucher interface {
	TouchDevice(id string) error
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
// A challenge is only ever *stored* for a device that actually exists and is
// not revoked. An unknown device still gets a perfectly ordinary-looking nonce
// back — it simply will not verify later.
//
// That asymmetry does two jobs at once:
//
//   - It keeps the endpoint from leaking which device ids exist. Returning an
//     error for an unknown device would turn /auth/challenge into an
//     enumeration oracle, which is exactly what Verify goes to some trouble to
//     avoid.
//   - It bounds the map by the number of paired devices instead of by the
//     number of requests. /auth/challenge is unauthenticated by necessity, so
//     without this an attacker could POST a few million random device ids and
//     grow `pending` until the daemon is killed for memory. Sweeping alone
//     would not help: entries live for ChallengeTTL, and an attacker can
//     create them faster than they expire.
func (a *DeviceAuthenticator) IssueChallenge(deviceID string) ([]byte, error) {
	nonce := make([]byte, ChallengeSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate challenge for device %s: %w", deviceID, err)
	}

	_, revoked, known := a.lookup.Device(deviceID)
	if !known || revoked {
		return nonce, nil
	}

	a.mu.Lock()
	a.sweepLocked()
	a.pending[deviceID] = &pendingChallenge{
		nonce:     nonce,
		expiresAt: a.now().Add(ChallengeTTL),
	}
	a.mu.Unlock()
	return nonce, nil
}

// sweepLocked drops expired challenges. Called with a.mu held.
//
// The bound above already keeps the map small, but an expired entry is dead
// weight either way and this is the natural place to notice it.
func (a *DeviceAuthenticator) sweepLocked() {
	now := a.now()
	for id, c := range a.pending {
		if now.After(c.expiresAt) {
			delete(a.pending, id)
		}
	}
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

	// Best-effort: a successful authentication must not be turned into a
	// failure because a bookkeeping write did not land. The owner losing one
	// last-seen update is a cosmetic problem; a device being refused because
	// the disk was briefly busy is not.
	if toucher, ok := a.lookup.(DeviceToucher); ok {
		if err := toucher.TouchDevice(deviceID); err != nil {
			log.Printf("update last_seen_at for device %s: %v", deviceID, err)
		}
	}
	return nil
}
