package identity

import (
	"errors"
	"testing"
	"time"
)

func TestStepUpGate_GrantedActionPassesOnceNotTwice(t *testing.T) {
	g := NewStepUpGate()
	g.Grant("dev-1", ActionRevokeDevice)

	if err := g.Consume("dev-1", ActionRevokeDevice); err != nil {
		t.Fatalf("first Consume: %v", err)
	}
	if err := g.Consume("dev-1", ActionRevokeDevice); !errors.Is(err, ErrStepUpRequired) {
		t.Fatalf("second Consume = %v, want ErrStepUpRequired", err)
	}
}

func TestStepUpGate_GrantDoesNotAuthorizeOtherAction(t *testing.T) {
	g := NewStepUpGate()
	g.Grant("dev-1", ActionRevokeDevice)

	if err := g.Consume("dev-1", ActionDeleteBackup); !errors.Is(err, ErrStepUpRequired) {
		t.Fatalf("Consume for a different action = %v, want ErrStepUpRequired", err)
	}
	// The original grant must still be intact — a rejected Consume for the
	// wrong action must not burn the grant for the right one.
	if err := g.Consume("dev-1", ActionRevokeDevice); err != nil {
		t.Fatalf("Consume for the originally granted action: %v", err)
	}
}

func TestStepUpGate_GrantDoesNotAuthorizeOtherDevice(t *testing.T) {
	g := NewStepUpGate()
	g.Grant("dev-1", ActionRevokeDevice)

	if err := g.Consume("dev-2", ActionRevokeDevice); !errors.Is(err, ErrStepUpRequired) {
		t.Fatalf("Consume for a different device = %v, want ErrStepUpRequired", err)
	}
}

func TestStepUpGate_ExpiredGrantFails(t *testing.T) {
	g := NewStepUpGate()
	start := time.Now()
	g.SetClock(func() time.Time { return start })
	g.Grant("dev-1", ActionRevokeDevice)

	g.SetClock(func() time.Time { return start.Add(StepUpTTL + time.Second) })

	if err := g.Consume("dev-1", ActionRevokeDevice); !errors.Is(err, ErrStepUpExpired) {
		t.Fatalf("Consume on expired grant = %v, want ErrStepUpExpired", err)
	}
}

func TestStepUpGate_NeverGrantedFails(t *testing.T) {
	g := NewStepUpGate()
	if err := g.Consume("dev-1", ActionRevokeDevice); !errors.Is(err, ErrStepUpRequired) {
		t.Fatalf("Consume with no grant = %v, want ErrStepUpRequired", err)
	}
}

// D-style tripwire, mirroring pairing_test.go's approach for D-07: if
// someone removes an action from stepUpRegistry, or adds a new sensitive
// action without registering it, this is the test that should fail.
func TestRequiresStepUp_UnregisteredActionRequiresStepUp(t *testing.T) {
	if !RequiresStepUp(SensitiveAction("delete_everything")) {
		t.Fatal("an unregistered action must default to requiring step-up (fail closed)")
	}
}

func TestRequiresStepUp_RegisteredActionsAllRequireStepUp(t *testing.T) {
	for _, action := range []SensitiveAction{
		ActionRevokeDevice, ActionPairDevice, ActionDeleteBackup, ActionChangeSecuritySettings,
	} {
		if !RequiresStepUp(action) {
			t.Errorf("RequiresStepUp(%s) = false, want true", action)
		}
	}
}
