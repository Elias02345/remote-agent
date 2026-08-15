package identity

import (
	"errors"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

func genCodeAt(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	code, err := totp.GenerateCodeCustom(secret, at, totp.ValidateOpts{
		Period:    uint(totpPeriod.Seconds()),
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		t.Fatalf("GenerateCodeCustom: %v", err)
	}
	return code
}

func TestTOTPValidCodeForNow(t *testing.T) {
	secret, _, err := GenerateTOTPSecret("ClaudeCode Remote", "owner")
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	now := time.Now()
	v := NewTOTPVerifier(secret)
	v.SetClock(func() time.Time { return now })

	code := genCodeAt(t, secret, now)
	if err := v.Verify([]byte(code)); err != nil {
		t.Fatalf("Verify(code for now) = %v, want nil", err)
	}
}

// Remembering only the most recently accepted code left a replay window one
// code wide: accept A, accept B a period later, and A is usable again — it is
// still inside its own ±30s skew window and the single slot now holds B.
// "Already used" has to mean used, not used-most-recently.
func TestTOTPCodeCannotBeReplayedAfterALaterCodeIsAccepted(t *testing.T) {
	secret, _, err := GenerateTOTPSecret("ClaudeCode Remote", "owner")
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}

	base := time.Now()
	now := base
	v := NewTOTPVerifier(secret)
	v.SetClock(func() time.Time { return now })

	codeA := genCodeAt(t, secret, base)
	if err := v.Verify([]byte(codeA)); err != nil {
		t.Fatalf("first use of code A = %v, want nil", err)
	}

	// One period later, so a different code is current. Code A is still
	// inside the skew window and must stay unusable.
	now = base.Add(totpPeriod)
	codeB := genCodeAt(t, secret, now)
	if codeB == codeA {
		t.Skip("consecutive periods produced the same code; nothing to prove here")
	}
	if err := v.Verify([]byte(codeB)); err != nil {
		t.Fatalf("first use of code B = %v, want nil", err)
	}

	if err := v.Verify([]byte(codeA)); !errors.Is(err, ErrTOTPCodeReused) {
		t.Fatalf("replay of code A after code B = %v, want ErrTOTPCodeReused", err)
	}
}

// The memory must not grow forever either: once a code can no longer validate,
// remembering it protects nothing.
func TestTOTPForgetsCodesOnceTheyCanNoLongerValidate(t *testing.T) {
	secret, _, err := GenerateTOTPSecret("ClaudeCode Remote", "owner")
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}

	base := time.Now()
	now := base
	v := NewTOTPVerifier(secret)
	v.SetClock(func() time.Time { return now })

	if err := v.Verify([]byte(genCodeAt(t, secret, base))); err != nil {
		t.Fatalf("first use = %v, want nil", err)
	}

	now = base.Add(10 * time.Minute)
	// Any Verify sweeps; the outcome of this one does not matter.
	_ = v.Verify([]byte(genCodeAt(t, secret, now)))

	v.mu.Lock()
	n := len(v.accepted)
	v.mu.Unlock()
	if n > 1 {
		t.Errorf("accepted-code set holds %d entries long after they expired, want at most 1", n)
	}
}

func TestTOTPCodeFarOutsideWindowRejected(t *testing.T) {
	secret, _, err := GenerateTOTPSecret("ClaudeCode Remote", "owner")
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	now := time.Now()
	v := NewTOTPVerifier(secret)
	v.SetClock(func() time.Time { return now })

	// Well outside the +-1 period skew window.
	staleCode := genCodeAt(t, secret, now.Add(-1*time.Hour))
	if err := v.Verify([]byte(staleCode)); !errors.Is(err, ErrTOTPCodeInvalid) {
		t.Fatalf("Verify(stale code) = %v, want ErrTOTPCodeInvalid", err)
	}
}

func TestTOTPCodeCannotBeReplayed(t *testing.T) {
	secret, _, err := GenerateTOTPSecret("ClaudeCode Remote", "owner")
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	now := time.Now()
	v := NewTOTPVerifier(secret)
	v.SetClock(func() time.Time { return now })

	code := genCodeAt(t, secret, now)
	if err := v.Verify([]byte(code)); err != nil {
		t.Fatalf("first Verify: %v", err)
	}
	if err := v.Verify([]byte(code)); !errors.Is(err, ErrTOTPCodeReused) {
		t.Fatalf("second Verify(same code) = %v, want ErrTOTPCodeReused", err)
	}
}

func TestTOTPMalformedOrEmptyCodeRejected(t *testing.T) {
	secret, _, err := GenerateTOTPSecret("ClaudeCode Remote", "owner")
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	v := NewTOTPVerifier(secret)

	if err := v.Verify([]byte("")); !errors.Is(err, ErrTOTPCodeInvalid) {
		t.Fatalf("Verify(empty) = %v, want ErrTOTPCodeInvalid", err)
	}
	if err := v.Verify([]byte("not-a-code")); !errors.Is(err, ErrTOTPCodeInvalid) {
		t.Fatalf("Verify(malformed) = %v, want ErrTOTPCodeInvalid", err)
	}
}
