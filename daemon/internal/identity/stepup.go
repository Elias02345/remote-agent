// stepup.go implements step-up authentication for sensitive single actions
// (docs/ARCHITECTURE.md Section 10.1): revoking a device, pairing a new one,
// deleting a backup, or changing security settings. An already-paired device
// clears these with the everyday Ed25519 check from device.go for ordinary
// use, but these specific actions demand a fresh, narrow re-authentication on
// top of that — otherwise a stolen already-authenticated session is enough to
// do anything the owner could do.
package identity

import (
	"errors"
	"sync"
	"time"
)

// SensitiveAction identifies one of the actions Section 10.1 calls out as
// needing step-up even from an already-paired device.
type SensitiveAction string

const (
	// ActionRevokeDevice guards removing a paired device's access.
	ActionRevokeDevice SensitiveAction = "revoke_device"
	// ActionPairDevice guards approving a new device pairing.
	ActionPairDevice SensitiveAction = "pair_device"
	// ActionDeleteBackup guards deleting a backup archive.
	ActionDeleteBackup SensitiveAction = "delete_backup"
	// ActionChangeSecuritySettings guards changing security configuration
	// (e.g. TOTP secret, passkeys, rate-limit policy).
	ActionChangeSecuritySettings SensitiveAction = "change_security_settings"
)

// stepUpRegistry is the source of truth for which actions require step-up.
// Every SensitiveAction constant above is registered true. It exists as a
// map, rather than "everything requires step-up, no exceptions", because
// RequiresStepUp's default-deny behavior for an *absent* key only means
// something if a present key can legitimately say false — otherwise the map
// would be indistinguishable from a hardcoded "always true".
var stepUpRegistry = map[SensitiveAction]bool{
	ActionRevokeDevice:           true,
	ActionPairDevice:             true,
	ActionDeleteBackup:           true,
	ActionChangeSecuritySettings: true,
}

// RequiresStepUp reports whether action needs a fresh step-up grant before
// it may proceed.
//
// Default deny: an action that is not in stepUpRegistry at all is treated as
// requiring step-up, not the other way round. A future sensitive endpoint
// that someone forgets to register therefore ships broken (refuses until
// registered) rather than shipping unprotected (accepts until someone
// notices) — the failure mode CLAUDE.md and Section 10.1 both call for.
func RequiresStepUp(action SensitiveAction) bool {
	required, ok := stepUpRegistry[action]
	if !ok {
		return true
	}
	return required
}

// StepUpTTL bounds how long a step-up grant stays usable after it is issued.
// A few minutes covers the gap between completing the passkey ceremony and
// the client making the one follow-up request it was for, without becoming
// long enough to function as a session-wide bypass — which is exactly the
// shortcut Section 10.1 is designed to avoid.
const StepUpTTL = 5 * time.Minute

var (
	// ErrStepUpRequired covers "no grant" in every sense that matters to a
	// caller: never granted, wrong device, wrong action, or already
	// consumed. These are deliberately not distinguished — a grant is a
	// capability, and a capability that leaked information about *why* it
	// failed would help an attacker narrow down what to try next.
	ErrStepUpRequired = errors.New("step-up authentication required")
	// ErrStepUpExpired is reported separately because, like
	// ErrChallengeExpired in device.go, it describes a grant's own lifetime
	// rather than anything about the device or action.
	ErrStepUpExpired = errors.New("step-up grant expired")
)

// stepUpGrant is one narrow, single-use authorization: this device, this
// action, until this time.
type stepUpGrant struct {
	expiresAt time.Time
}

// StepUpGate tracks step-up grants. A grant is bound to exactly one device
// and one action — a grant for revoking a device does not authorize deleting
// a backup, and a grant issued to one device does not authorize another.
// This narrowness is the point: Section 10.1 warns that stacking auth
// factors "on every action" is unusable, but the fix is a narrow grant, not
// a broad one that quietly becomes a single prompt per session.
//
// Safe for concurrent use.
type StepUpGate struct {
	mu      sync.Mutex
	grants  map[string]*stepUpGrant // key: deviceID + "\x00" + action
	nowFunc func() time.Time
}

// NewStepUpGate returns an empty gate.
func NewStepUpGate() *StepUpGate {
	return &StepUpGate{
		grants:  make(map[string]*stepUpGrant),
		nowFunc: time.Now,
	}
}

// SetClock overrides the clock, for tests.
func (g *StepUpGate) SetClock(f func() time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.nowFunc = f
}

func (g *StepUpGate) now() time.Time {
	if g.nowFunc == nil {
		return time.Now()
	}
	return g.nowFunc()
}

func grantKey(deviceID string, action SensitiveAction) string {
	return deviceID + "\x00" + string(action)
}

// Grant records that deviceID has just completed the real step-up ceremony
// (passkey/TOTP/etc, performed elsewhere) for action. StepUpGate does not
// perform that authentication itself — it only tracks the resulting
// permission slip and enforces that it is narrow and single-use.
func (g *StepUpGate) Grant(deviceID string, action SensitiveAction) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.grants[grantKey(deviceID, action)] = &stepUpGrant{
		expiresAt: g.now().Add(StepUpTTL),
	}
}

// Consume checks for a valid, unused grant for exactly this device and
// action, and consumes it if found. It does not consult RequiresStepUp —
// that decides *whether* an action needs a grant at all; Consume only ever
// answers "was there one".
//
// The grant is deleted here regardless of whether it turns out to be
// expired, so a stale grant cannot be retried into validity and an expired
// grant cannot be reused for its error message alone.
func (g *StepUpGate) Consume(deviceID string, action SensitiveAction) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := grantKey(deviceID, action)
	grant, ok := g.grants[key]
	if !ok {
		return ErrStepUpRequired
	}
	delete(g.grants, key)
	if g.now().After(grant.expiresAt) {
		return ErrStepUpExpired
	}
	return nil
}
