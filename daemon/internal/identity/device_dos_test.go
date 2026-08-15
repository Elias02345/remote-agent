package identity

import (
	"crypto/ed25519"
	"fmt"
	"testing"
	"time"
)

// boundedLookup knows exactly one device, like a real installation with one
// paired phone.
type boundedLookup struct {
	id  string
	pub ed25519.PublicKey
}

func (b boundedLookup) Device(id string) (ed25519.PublicKey, bool, bool) {
	if id == b.id {
		return b.pub, false, true
	}
	return nil, false, false
}

// /auth/challenge is unauthenticated by necessity — a device has nothing to
// authenticate with at that point. Before this was bounded, every request
// inserted a map entry keyed by whatever device id the caller sent, so a few
// million random ids would grow `pending` until the daemon was killed for
// memory. Sweeping alone would not have helped: entries live for ChallengeTTL,
// and an attacker creates them faster than they expire.
func TestIssueChallengeDoesNotGrowForUnknownDevices(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	a := NewDeviceAuthenticator(boundedLookup{id: "real-device", pub: pub})

	for i := 0; i < 5000; i++ {
		if _, err := a.IssueChallenge(fmt.Sprintf("attacker-%d", i)); err != nil {
			t.Fatalf("IssueChallenge: %v", err)
		}
	}

	a.mu.Lock()
	size := len(a.pending)
	a.mu.Unlock()

	if size != 0 {
		t.Fatalf("pending map grew to %d entries from unknown device ids; want 0", size)
	}
}

// The nonce must still come back for an unknown device, or the endpoint
// becomes an enumeration oracle: "error" would mean "no such device" and
// "nonce" would mean "this device exists".
func TestIssueChallengeStillAnswersForUnknownDevices(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	a := NewDeviceAuthenticator(boundedLookup{id: "real-device", pub: pub})

	known, err := a.IssueChallenge("real-device")
	if err != nil {
		t.Fatalf("IssueChallenge(known): %v", err)
	}
	unknown, err := a.IssueChallenge("does-not-exist")
	if err != nil {
		t.Fatalf("IssueChallenge(unknown): %v", err)
	}

	if len(known) != len(unknown) {
		t.Errorf("nonce length differs by device existence: %d vs %d — that is an oracle",
			len(known), len(unknown))
	}
	if len(unknown) != ChallengeSize {
		t.Errorf("unknown-device nonce is %d bytes, want %d", len(unknown), ChallengeSize)
	}
}

func TestIssueChallengeStoresForKnownDevice(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	a := NewDeviceAuthenticator(boundedLookup{id: "real-device", pub: pub})

	nonce, err := a.IssueChallenge("real-device")
	if err != nil {
		t.Fatalf("IssueChallenge: %v", err)
	}
	if err := a.Verify("real-device", ed25519.Sign(priv, nonce)); err != nil {
		t.Fatalf("a challenge issued to a known device must verify: %v", err)
	}
}

// A revoked device must not even get an entry: revocation that only takes
// effect at the next step is not revocation.
func TestIssueChallengeIgnoresRevokedDevices(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	a := NewDeviceAuthenticator(revokedLookup{pub: pub})

	nonce, err := a.IssueChallenge("revoked-device")
	if err != nil {
		t.Fatalf("IssueChallenge: %v", err)
	}
	if err := a.Verify("revoked-device", ed25519.Sign(priv, nonce)); err == nil {
		t.Fatal("a revoked device authenticated with a correctly signed challenge")
	}
}

type revokedLookup struct{ pub ed25519.PublicKey }

func (r revokedLookup) Device(id string) (ed25519.PublicKey, bool, bool) {
	return r.pub, true, true
}

func TestSweepDropsExpiredChallenges(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	a := NewDeviceAuthenticator(boundedLookup{id: "real-device", pub: pub})

	if _, err := a.IssueChallenge("real-device"); err != nil {
		t.Fatalf("IssueChallenge: %v", err)
	}

	future := time.Now().Add(ChallengeTTL + time.Minute)
	a.SetClock(func() time.Time { return future })

	// Issuing again runs the sweep, which should drop the now-expired entry
	// before inserting the new one.
	if _, err := a.IssueChallenge("real-device"); err != nil {
		t.Fatalf("second IssueChallenge: %v", err)
	}

	a.mu.Lock()
	size := len(a.pending)
	a.mu.Unlock()
	if size != 1 {
		t.Fatalf("pending has %d entries after sweep, want 1", size)
	}
}
