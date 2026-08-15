package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testDevicePubKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return base64.StdEncoding.EncodeToString(pub)
}

// The account lockout is meant to make guessing the owner's credentials
// expensive. It must not make denying the owner service cheap.
//
// There is exactly one account, so five failures against it lock the only way
// in. If a wrong *email* counted against that account, anyone who could reach
// the daemon could lock the owner out of their own machine with five requests —
// without knowing the owner's address, the password, or a pairing id.
func TestWrongEmailDoesNotLockTheOwnerAccount(t *testing.T) {
	owner, _, _ := testOwner(t)

	// Exactly the threshold: the lockout trips on the fifth recorded failure,
	// so all five requests are still let through to be refused on their merits.
	for i := 0; i < FailureThreshold; i++ {
		rec, _ := doJSON(t, owner.handlePairPassword, http.MethodPost, "/owner/pair/password", map[string]any{
			"pairing_id": "irrelevant",
			"email":      "stranger@example.invalid",
			"password":   "whatever",
		})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i, rec.Code)
		}
	}

	// The account must still be usable. A different IP stands in for the owner
	// picking up their phone: the stranger's own address is legitimately
	// locked, and that is the point.
	if err := owner.limiter.Allow("10.0.0.9", testOwnerEmail); err != nil {
		t.Fatalf("owner account locked out by a stranger's wrong-email guesses: %v", err)
	}
}

// A wrong password for the RIGHT email is a real guess against the account and
// must still count — otherwise the previous test's fix would have removed the
// lockout instead of narrowing it.
func TestWrongPasswordForTheRealEmailStillLocksTheAccount(t *testing.T) {
	owner, _, _ := testOwner(t)
	pairingID := startPairing(t, owner)

	for i := 0; i < FailureThreshold; i++ {
		doJSON(t, owner.handlePairPassword, http.MethodPost, "/owner/pair/password", map[string]any{
			"pairing_id": pairingID, "email": testOwnerEmail, "password": "wrong",
		})
	}

	if err := owner.limiter.Allow("10.0.0.9", testOwnerEmail); err == nil {
		t.Fatal("five wrong passwords for the real email did not lock the account")
	}
}

// Resetting the counters after each satisfied factor let the factors launder
// each other's budget: satisfy the password, counters wiped, five fresh guesses
// at the six-digit TOTP, satisfy the password again, five more — unlimited
// attempts with the lockout never firing.
func TestSatisfyingOneFactorDoesNotForgiveEarlierFailures(t *testing.T) {
	owner, _, _ := testOwner(t)
	pairingID := startPairing(t, owner)

	// Four wrong TOTP codes: one short of the lockout.
	for i := 0; i < FailureThreshold-1; i++ {
		doJSON(t, owner.handlePairTOTP, http.MethodPost, "/owner/pair/totp", map[string]any{
			"pairing_id": pairingID, "code": "000000",
		})
	}

	// Now satisfy a different factor correctly.
	rec, _ := doJSON(t, owner.handlePairPassword, http.MethodPost, "/owner/pair/password", map[string]any{
		"pairing_id": pairingID, "email": testOwnerEmail, "password": testOwnerPassword,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("correct password: status = %d, want 200", rec.Code)
	}

	// The fifth wrong code must still trip the lockout. If the password
	// success wiped the counter, this one starts from zero and nothing locks.
	doJSON(t, owner.handlePairTOTP, http.MethodPost, "/owner/pair/totp", map[string]any{
		"pairing_id": pairingID, "code": "000000",
	})

	if err := owner.limiter.Allow("10.0.0.9", testOwnerEmail); err == nil {
		t.Fatal("satisfying the password factor forgave the TOTP failures; the guess budget is unbounded")
	}
}

// /owner/pair/start is unauthenticated by necessity, so everything it accepts
// is attacker-controlled and retained for PairingTTL.
func TestPairStartRejectsOversizedMetadata(t *testing.T) {
	owner, _, _ := testOwner(t)

	for _, tc := range []struct{ field, value string }{
		{"device_name", strings.Repeat("n", maxDeviceNameLen+1)},
		{"platform", strings.Repeat("p", maxPlatformLen+1)},
	} {
		body := map[string]any{"device_pubkey": testDevicePubKey(t), "device_name": "phone", "platform": "android"}
		body[tc.field] = tc.value

		rec, _ := doJSON(t, owner.handlePairStart, http.MethodPost, "/owner/pair/start", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("oversized %s: status = %d, want 400", tc.field, rec.Code)
		}
	}

	owner.mu.Lock()
	n := len(owner.attempts)
	owner.mu.Unlock()
	if n != 0 {
		t.Errorf("%d attempts retained after rejected requests, want 0", n)
	}
}

func TestPairStartBoundsConcurrentAttempts(t *testing.T) {
	owner, _, _ := testOwner(t)

	for i := 0; i < maxPairingAttempts; i++ {
		rec, _ := doJSON(t, owner.handlePairStart, http.MethodPost, "/owner/pair/start", map[string]any{
			"device_pubkey": testDevicePubKey(t),
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("attempt %d: status = %d, want 200", i, rec.Code)
		}
	}

	rec, _ := doJSON(t, owner.handlePairStart, http.MethodPost, "/owner/pair/start", map[string]any{
		"device_pubkey": testDevicePubKey(t),
	})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt past the cap: status = %d, want 429", rec.Code)
	}

	owner.mu.Lock()
	n := len(owner.attempts)
	owner.mu.Unlock()
	// Refused, not evicted: a flood must not be able to cancel the owner's own
	// half-finished ceremony.
	if n != maxPairingAttempts {
		t.Errorf("retained %d attempts, want exactly the cap (%d)", n, maxPairingAttempts)
	}
}

// Behind CloudGate every request arrives from the tunnel's address, so
// RemoteAddr alone puts the whole internet in one rate-limit bucket. Trusting
// the header unconditionally would be worse: then a caller picks their own
// bucket and never trips a lockout at all.
func TestClientIPResolver(t *testing.T) {
	req := func(remote, xff, cf string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/owner/pair/password", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		if cf != "" {
			r.Header.Set("CF-Connecting-IP", cf)
		}
		return r
	}

	untrusting := NewClientIPResolver(nil)
	if got := untrusting.IP(req("203.0.113.7:5555", "1.2.3.4", "5.6.7.8")); got != "203.0.113.7" {
		t.Errorf("with no trusted proxies, IP = %q, want the peer address 203.0.113.7", got)
	}

	trusting := NewClientIPResolver([]string{"10.8.0.0/24"})
	if got := trusting.IP(req("10.8.0.3:4444", "", "198.51.100.5")); got != "198.51.100.5" {
		t.Errorf("CF-Connecting-IP from a trusted proxy: IP = %q, want 198.51.100.5", got)
	}
	// Rightmost untrusted hop, not leftmost: the left end is whatever the
	// client typed into the header themselves.
	if got := trusting.IP(req("10.8.0.3:44444", "9.9.9.9, 198.51.100.5, 10.8.0.3", "")); got != "198.51.100.5" {
		t.Errorf("X-Forwarded-For chain: IP = %q, want 198.51.100.5", got)
	}
	// A spoofed header from an untrusted peer changes nothing.
	if got := trusting.IP(req("203.0.113.7:5555", "198.51.100.5", "")); got != "203.0.113.7" {
		t.Errorf("spoofed header from an untrusted peer: IP = %q, want 203.0.113.7", got)
	}
}
