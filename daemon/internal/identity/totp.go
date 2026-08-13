package identity

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// totpPeriod is the RFC 6238 default step size.
const totpPeriod = 30 * time.Second

// totpSkew is how many periods before or after "now" a code still
// validates, to tolerate a phone or server clock that has drifted a little.
// It stays at 1 (so +-30s) deliberately: each extra period doubles the
// number of codes that validate alongside the current one, and that window
// is exactly what a shoulder-surfed or intercepted code needs to still
// work. A wider skew trades security margin for clock tolerance that a
// modern phone rarely needs.
const totpSkew = 1

var (
	// ErrTOTPCodeInvalid is returned for a code that does not validate
	// against the secret within the skew window, or is empty/malformed.
	ErrTOTPCodeInvalid = errors.New("totp code invalid")
	// ErrTOTPCodeReused is returned when a code that was already accepted is
	// presented again. Without this, a code observed once (shoulder surf,
	// screen share, compromised clipboard) stays usable for its entire
	// validity window.
	ErrTOTPCodeReused = errors.New("totp code already used")
)

// GenerateTOTPSecret creates a new RFC 6238 secret for accountName and
// returns both the raw secret (store this, encrypted, per Section 10.2) and
// the otpauth:// provisioning URI (show this once, as a QR code, so the
// owner can add it to an authenticator app).
func GenerateTOTPSecret(issuer, accountName string) (secret, provisioningURI string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: accountName,
	})
	if err != nil {
		return "", "", fmt.Errorf("generate totp secret: %w", err)
	}
	return key.Secret(), key.URL(), nil
}

// TOTPVerifier implements Verifier for the pairing chain's second factor
// (Section 10.2: an RFC 6238 code). One instance guards one account's
// secret and remembers the last code it accepted, so that code cannot be
// replayed a second time inside its own validity window.
type TOTPVerifier struct {
	secret  string
	nowFunc func() time.Time

	mu           sync.Mutex
	lastAccepted string
}

// NewTOTPVerifier builds a verifier for a previously generated secret.
func NewTOTPVerifier(secret string) *TOTPVerifier {
	return &TOTPVerifier{secret: secret, nowFunc: time.Now}
}

// SetClock overrides the clock, for tests.
func (t *TOTPVerifier) SetClock(f func() time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nowFunc = f
}

// Verify reports nil only if evidence, interpreted as a decimal TOTP code,
// validates against the stored secret within the skew window and has not
// already been accepted.
func (t *TOTPVerifier) Verify(evidence []byte) error {
	code := string(evidence)
	if code == "" {
		return fmt.Errorf("%w: empty code", ErrTOTPCodeInvalid)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// Constant-time compare against the last accepted code: same reasoning
	// as the password hash comparison, this sits directly on an auth path.
	// ConstantTimeCompare returns 0 (not a panic) on a length mismatch, so
	// no separate length check is needed first.
	if t.lastAccepted != "" && subtle.ConstantTimeCompare([]byte(code), []byte(t.lastAccepted)) == 1 {
		return ErrTOTPCodeReused
	}

	valid, err := totp.ValidateCustom(code, t.secret, t.nowFunc(), totp.ValidateOpts{
		Period:    uint(totpPeriod.Seconds()),
		Skew:      totpSkew,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTOTPCodeInvalid, err)
	}
	if !valid {
		return ErrTOTPCodeInvalid
	}

	t.lastAccepted = code
	return nil
}
