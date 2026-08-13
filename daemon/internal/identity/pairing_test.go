package identity

import (
	"errors"
	"testing"
	"time"
)

// alwaysOK stands in for a factor Phase 7 will implement for real.
type alwaysOK struct{}

func (alwaysOK) Verify([]byte) error { return nil }

type alwaysFails struct{}

func (alwaysFails) Verify([]byte) error { return errors.New("wrong credentials") }

func allImplemented() map[Factor]Verifier {
	return map[Factor]Verifier{
		FactorPassword: alwaysOK{},
		FactorTOTP:     alwaysOK{},
		FactorPasskey:  alwaysOK{},
	}
}

// The whole point of building this before the factors exist: an unfinished
// Phase 7 must not be able to pair anything.
func TestUnimplementedFactorsCannotBeSatisfied(t *testing.T) {
	a := NewAttempt("code-1", "pubkey", nil)

	for _, f := range RequiredFactors {
		err := a.Satisfy(f, []byte("anything"))
		if !errors.Is(err, ErrFactorNotImplemented) {
			t.Errorf("Satisfy(%s) = %v, want ErrFactorNotImplemented", f, err)
		}
	}
	if err := a.Complete(); !errors.Is(err, ErrPairingIncomplete) {
		t.Fatalf("Complete with nothing satisfied = %v, want ErrPairingIncomplete", err)
	}
	if a.Completed() {
		t.Fatal("attempt reports itself completed after a refused Complete")
	}
}

// Two out of three is not enough. This is the shortcut CLAUDE.md forbids, so
// it gets an explicit test rather than relying on the implementation reading
// correctly.
func TestPartialChainCannotComplete(t *testing.T) {
	a := NewAttempt("code-2", "pubkey", allImplemented())

	if err := a.Satisfy(FactorPassword, nil); err != nil {
		t.Fatalf("password: %v", err)
	}
	if err := a.Satisfy(FactorTOTP, nil); err != nil {
		t.Fatalf("totp: %v", err)
	}

	err := a.Complete()
	if !errors.Is(err, ErrPairingIncomplete) {
		t.Fatalf("Complete with 2 of 3 factors = %v, want ErrPairingIncomplete", err)
	}
	if outstanding := a.Outstanding(); len(outstanding) != 1 || outstanding[0] != FactorPasskey {
		t.Fatalf("Outstanding = %v, want [passkey]", outstanding)
	}
}

func TestFullChainCompletes(t *testing.T) {
	a := NewAttempt("code-3", "pubkey", allImplemented())

	for _, f := range RequiredFactors {
		if err := a.Satisfy(f, nil); err != nil {
			t.Fatalf("Satisfy(%s): %v", f, err)
		}
	}
	if err := a.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !a.Completed() {
		t.Fatal("Completed() is false after a successful Complete")
	}
}

// A pairing code observed once must not be usable to pair a second device.
func TestPairingCodeIsSingleUse(t *testing.T) {
	a := NewAttempt("code-4", "pubkey", allImplemented())
	for _, f := range RequiredFactors {
		if err := a.Satisfy(f, nil); err != nil {
			t.Fatalf("Satisfy(%s): %v", f, err)
		}
	}
	if err := a.Complete(); err != nil {
		t.Fatalf("first Complete: %v", err)
	}
	if err := a.Complete(); !errors.Is(err, ErrPairingConsumed) {
		t.Fatalf("second Complete = %v, want ErrPairingConsumed", err)
	}
	if err := a.Satisfy(FactorPassword, nil); !errors.Is(err, ErrPairingConsumed) {
		t.Fatalf("Satisfy after completion = %v, want ErrPairingConsumed", err)
	}
}

func TestWrongEvidenceDoesNotSatisfy(t *testing.T) {
	a := NewAttempt("code-5", "pubkey", map[Factor]Verifier{
		FactorPassword: alwaysFails{},
		FactorTOTP:     alwaysOK{},
		FactorPasskey:  alwaysOK{},
	})

	if err := a.Satisfy(FactorPassword, []byte("wrong")); err == nil {
		t.Fatal("a failing verifier reported success")
	}
	if len(a.Outstanding()) != 3 {
		t.Fatalf("a failed factor was recorded as satisfied: outstanding = %v", a.Outstanding())
	}
}

// An expired attempt must be rejected before the factor is even checked,
// otherwise "expired" would only be advisory.
func TestExpiredAttemptRefusesEverything(t *testing.T) {
	a := NewAttempt("code-6", "pubkey", allImplemented())
	if err := a.Satisfy(FactorPassword, nil); err != nil {
		t.Fatalf("password: %v", err)
	}

	future := time.Now().Add(PairingTTL + time.Minute)
	a.SetClock(func() time.Time { return future })

	if err := a.Satisfy(FactorTOTP, nil); !errors.Is(err, ErrPairingExpired) {
		t.Fatalf("Satisfy on expired attempt = %v, want ErrPairingExpired", err)
	}
	if err := a.Complete(); !errors.Is(err, ErrPairingExpired) {
		t.Fatalf("Complete on expired attempt = %v, want ErrPairingExpired", err)
	}
}

func TestUnknownFactorRejected(t *testing.T) {
	a := NewAttempt("code-7", "pubkey", allImplemented())
	if err := a.Satisfy(Factor("sms"), nil); !errors.Is(err, ErrUnknownFactor) {
		t.Fatalf("Satisfy(sms) = %v, want ErrUnknownFactor", err)
	}
}

// D-07: no reduced tier. If someone shortens this list, these tests are the
// tripwire.
func TestRequiredFactorsAreAllThree(t *testing.T) {
	if len(RequiredFactors) != 3 {
		t.Fatalf("RequiredFactors has %d entries, want 3 (D-07: no reduced tier)", len(RequiredFactors))
	}
	want := map[Factor]bool{FactorPassword: true, FactorTOTP: true, FactorPasskey: true}
	for _, f := range RequiredFactors {
		if !want[f] {
			t.Errorf("unexpected factor %q in the required chain", f)
		}
		delete(want, f)
	}
	if len(want) != 0 {
		t.Errorf("missing factors from the required chain: %v", want)
	}
}
