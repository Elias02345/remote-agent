// Package identity implements device pairing and authentication
// (docs/ARCHITECTURE.md Sections 5.4 and 10).
//
// This file is the pairing state machine. It exists before the individual
// factors do, on purpose: CLAUDE.md forbids silent shortcuts in the
// three-factor chain, and the reliable way to keep that promise is to make
// the machine refuse by default and require each factor to be explicitly
// satisfied. A factor that has not been implemented yet cannot be satisfied,
// so a half-finished Phase 7 cannot accidentally pair a device.
package identity

import (
	"errors"
	"fmt"
	"time"
)

// Factor is one link in the pairing chain (Section 10.2).
type Factor string

const (
	// FactorPassword is email + password, verified against an Argon2id hash.
	FactorPassword Factor = "password"
	// FactorTOTP is an RFC 6238 code.
	FactorTOTP Factor = "totp"
	// FactorPasskey is the WebAuthn ceremony.
	FactorPasskey Factor = "passkey"
)

// RequiredFactors is the full chain. Decision D-07: it applies to every
// pairing without exception — there is no reduced tier for "a trusted
// network", because a reduced tier is exactly the shortcut that gets taken
// once and then never removed.
var RequiredFactors = []Factor{FactorPassword, FactorTOTP, FactorPasskey}

// PairingTTL bounds how long a half-finished pairing stays usable. A pairing
// attempt left open indefinitely is a standing invitation: the window during
// which one more factor completes an already-half-authenticated flow should
// be minutes, not days.
const PairingTTL = 10 * time.Minute

var (
	// ErrFactorNotImplemented is what an unbuilt factor returns. It is
	// deliberately a hard failure rather than a permissive default.
	ErrFactorNotImplemented = errors.New("authentication factor is not implemented yet")
	// ErrPairingExpired is returned once a pairing attempt is past its TTL.
	ErrPairingExpired = errors.New("pairing attempt expired")
	// ErrPairingIncomplete is returned when completion is attempted before
	// every required factor is satisfied.
	ErrPairingIncomplete = errors.New("pairing is not complete: factors still outstanding")
	// ErrPairingConsumed is returned when a pairing code is reused.
	ErrPairingConsumed = errors.New("pairing code has already been used")
	// ErrUnknownFactor is returned for a factor outside the required chain.
	ErrUnknownFactor = errors.New("unknown authentication factor")
)

// Verifier checks one factor. Phase 7 supplies the real implementations; until
// then every factor is absent and therefore unsatisfiable.
type Verifier interface {
	// Verify returns nil only if the presented evidence is valid.
	Verify(evidence []byte) error
}

// notImplemented is the default for every factor. Its presence is what makes
// the chain fail closed while Phase 7 is unfinished.
type notImplemented struct{ factor Factor }

func (n notImplemented) Verify([]byte) error {
	return fmt.Errorf("%w: %s", ErrFactorNotImplemented, n.factor)
}

// Attempt is one in-progress device pairing.
type Attempt struct {
	Code       string
	DevicePub  string
	StartedAt  time.Time
	satisfied  map[Factor]bool
	consumed   bool
	verifiers  map[Factor]Verifier
	nowFunc    func() time.Time
	completedA bool
}

// NewAttempt starts a pairing attempt for a device public key. Verifiers may
// be nil or partially populated; anything missing defaults to a factor that
// cannot be satisfied.
func NewAttempt(code, devicePub string, verifiers map[Factor]Verifier) *Attempt {
	full := map[Factor]Verifier{}
	for _, f := range RequiredFactors {
		if v, ok := verifiers[f]; ok && v != nil {
			full[f] = v
		} else {
			full[f] = notImplemented{factor: f}
		}
	}
	return &Attempt{
		Code:      code,
		DevicePub: devicePub,
		StartedAt: time.Now(),
		satisfied: map[Factor]bool{},
		verifiers: full,
		nowFunc:   time.Now,
	}
}

// SetClock overrides the clock, for tests.
func (a *Attempt) SetClock(f func() time.Time) { a.nowFunc = f }

func (a *Attempt) now() time.Time {
	if a.nowFunc == nil {
		return time.Now()
	}
	return a.nowFunc()
}

// Expired reports whether the attempt is past its TTL.
func (a *Attempt) Expired() bool { return a.now().Sub(a.StartedAt) > PairingTTL }

// Satisfy verifies one factor and records it.
//
// An expired attempt is rejected before the factor is even checked: otherwise
// a stale flow could still be advanced, and "expired" would mean nothing.
func (a *Attempt) Satisfy(f Factor, evidence []byte) error {
	if a.consumed {
		return ErrPairingConsumed
	}
	if a.Expired() {
		return ErrPairingExpired
	}
	v, ok := a.verifiers[f]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownFactor, f)
	}
	if err := v.Verify(evidence); err != nil {
		return err
	}
	a.satisfied[f] = true
	return nil
}

// Outstanding lists the required factors that are not yet satisfied.
func (a *Attempt) Outstanding() []Factor {
	var out []Factor
	for _, f := range RequiredFactors {
		if !a.satisfied[f] {
			out = append(out, f)
		}
	}
	return out
}

// Complete finalises a pairing. It is the only place that may report a device
// as paired, and it succeeds only when every required factor is satisfied and
// the attempt is neither expired nor already used.
//
// A pairing code is single-use: without that, a code observed once could pair
// a second device later.
func (a *Attempt) Complete() error {
	if a.consumed {
		return ErrPairingConsumed
	}
	if a.Expired() {
		return ErrPairingExpired
	}
	if outstanding := a.Outstanding(); len(outstanding) > 0 {
		return fmt.Errorf("%w: %v", ErrPairingIncomplete, outstanding)
	}
	a.consumed = true
	a.completedA = true
	return nil
}

// Completed reports whether this attempt successfully paired a device.
func (a *Attempt) Completed() bool { return a.completedA }
