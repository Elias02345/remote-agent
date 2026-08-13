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
