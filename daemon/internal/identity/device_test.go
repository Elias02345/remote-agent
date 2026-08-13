package identity

import (
	"crypto/ed25519"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeLookup is an in-memory DeviceLookup for tests, so device_test.go
// doesn't need package db (and its sqlite driver) at all.
type fakeLookup struct {
	mu      sync.Mutex
	devices map[string]struct {
		pub     ed25519.PublicKey
		revoked bool
	}
}

func newFakeLookup() *fakeLookup {
	return &fakeLookup{devices: make(map[string]struct {
		pub     ed25519.PublicKey
		revoked bool
	})}
}

func (f *fakeLookup) add(id string, pub ed25519.PublicKey, revoked bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.devices[id] = struct {
		pub     ed25519.PublicKey
		revoked bool
	}{pub, revoked}
}

func (f *fakeLookup) Device(id string) (ed25519.PublicKey, bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.devices[id]
	return d.pub, d.revoked, ok
}

func TestDeviceVerify_ValidSignaturePasses(t *testing.T) {
	lookup := newFakeLookup()
	pub, priv, _ := ed25519.GenerateKey(nil)
	lookup.add("dev-1", pub, false)

	auth := NewDeviceAuthenticator(lookup)
	nonce, err := auth.IssueChallenge("dev-1")
	if err != nil {
		t.Fatalf("IssueChallenge: %v", err)
	}
	sig := ed25519.Sign(priv, nonce)

	if err := auth.Verify("dev-1", sig); err != nil {
		t.Fatalf("Verify with a valid signature = %v, want nil", err)
	}
}

func TestDeviceVerify_WrongKeyFails(t *testing.T) {
	lookup := newFakeLookup()
	pub, _, _ := ed25519.GenerateKey(nil)
	lookup.add("dev-1", pub, false)
	_, otherPriv, _ := ed25519.GenerateKey(nil)

	auth := NewDeviceAuthenticator(lookup)
	nonce, err := auth.IssueChallenge("dev-1")
	if err != nil {
		t.Fatalf("IssueChallenge: %v", err)
	}
	sig := ed25519.Sign(otherPriv, nonce) // signed with the wrong private key

	if err := auth.Verify("dev-1", sig); !errors.Is(err, ErrDeviceAuthFailed) {
		t.Fatalf("Verify with wrong key = %v, want ErrDeviceAuthFailed", err)
	}
}

func TestDeviceVerify_ChallengeCannotBeReused(t *testing.T) {
	lookup := newFakeLookup()
	pub, priv, _ := ed25519.GenerateKey(nil)
	lookup.add("dev-1", pub, false)

	auth := NewDeviceAuthenticator(lookup)
	nonce, err := auth.IssueChallenge("dev-1")
	if err != nil {
		t.Fatalf("IssueChallenge: %v", err)
	}
	sig := ed25519.Sign(priv, nonce)

	if err := auth.Verify("dev-1", sig); err != nil {
		t.Fatalf("first Verify: %v", err)
	}
	// Same valid signature, replayed. The challenge was already consumed by
	// the first attempt, win or lose, so this must fail even though the
	// signature itself is perfectly valid.
	if err := auth.Verify("dev-1", sig); !errors.Is(err, ErrDeviceAuthFailed) {
		t.Fatalf("replayed Verify = %v, want ErrDeviceAuthFailed", err)
	}
}

func TestDeviceVerify_ExpiredChallengeFails(t *testing.T) {
	lookup := newFakeLookup()
	pub, priv, _ := ed25519.GenerateKey(nil)
	lookup.add("dev-1", pub, false)

	auth := NewDeviceAuthenticator(lookup)
	start := time.Now()
	auth.SetClock(func() time.Time { return start })

	nonce, err := auth.IssueChallenge("dev-1")
	if err != nil {
		t.Fatalf("IssueChallenge: %v", err)
	}
	sig := ed25519.Sign(priv, nonce)

	auth.SetClock(func() time.Time { return start.Add(ChallengeTTL + time.Second) })

	if err := auth.Verify("dev-1", sig); !errors.Is(err, ErrChallengeExpired) {
		t.Fatalf("Verify on expired challenge = %v, want ErrChallengeExpired", err)
	}
}

func TestDeviceVerify_RevokedDeviceFailsWithCorrectSignature(t *testing.T) {
	lookup := newFakeLookup()
	pub, priv, _ := ed25519.GenerateKey(nil)
	lookup.add("dev-1", pub, true) // revoked

	auth := NewDeviceAuthenticator(lookup)
	nonce, err := auth.IssueChallenge("dev-1")
	if err != nil {
		t.Fatalf("IssueChallenge: %v", err)
	}
	sig := ed25519.Sign(priv, nonce) // a perfectly correct signature

	if err := auth.Verify("dev-1", sig); !errors.Is(err, ErrDeviceAuthFailed) {
		t.Fatalf("Verify for revoked device = %v, want ErrDeviceAuthFailed", err)
	}
}

func TestDeviceVerify_UnknownDeviceFails(t *testing.T) {
	lookup := newFakeLookup() // "dev-1" was never added
	_, priv, _ := ed25519.GenerateKey(nil)

	auth := NewDeviceAuthenticator(lookup)
	nonce, err := auth.IssueChallenge("dev-1")
	if err != nil {
		t.Fatalf("IssueChallenge: %v", err)
	}
	sig := ed25519.Sign(priv, nonce)

	err = auth.Verify("dev-1", sig)
	if !errors.Is(err, ErrDeviceAuthFailed) {
		t.Fatalf("Verify for unknown device = %v, want ErrDeviceAuthFailed", err)
	}
}

func TestDeviceVerify_UnknownDeviceSameErrorAsRevokedOrBadSignature(t *testing.T) {
	// The three failure modes that must not be distinguishable from outside
	// this package: unknown device, revoked device, bad signature.
	lookup := newFakeLookup()
	pub, priv, _ := ed25519.GenerateKey(nil)
	lookup.add("known-revoked", pub, true)
	lookup.add("known-active", pub, false)
	_, wrongPriv, _ := ed25519.GenerateKey(nil)

	auth := NewDeviceAuthenticator(lookup)

	cases := []struct {
		name   string
		device string
		sign   func(nonce []byte) []byte
	}{
		{"unknown device", "never-paired", func(n []byte) []byte { return ed25519.Sign(priv, n) }},
		{"revoked device", "known-revoked", func(n []byte) []byte { return ed25519.Sign(priv, n) }},
		{"bad signature", "known-active", func(n []byte) []byte { return ed25519.Sign(wrongPriv, n) }},
	}
	for _, c := range cases {
		nonce, err := auth.IssueChallenge(c.device)
		if err != nil {
			t.Fatalf("%s: IssueChallenge: %v", c.name, err)
		}
		err = auth.Verify(c.device, c.sign(nonce))
		if !errors.Is(err, ErrDeviceAuthFailed) {
			t.Errorf("%s: Verify = %v, want ErrDeviceAuthFailed", c.name, err)
		}
	}
}

func TestDeviceVerify_GarbageSignatureDoesNotPanic(t *testing.T) {
	lookup := newFakeLookup()
	pub, _, _ := ed25519.GenerateKey(nil)
	lookup.add("dev-1", pub, false)

	auth := NewDeviceAuthenticator(lookup)

	garbage := [][]byte{nil, {}, []byte("not a signature"), make([]byte, 4096)}
	for i, sig := range garbage {
		if _, err := auth.IssueChallenge("dev-1"); err != nil {
			t.Fatalf("case %d: IssueChallenge: %v", i, err)
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("case %d: Verify panicked: %v", i, r)
				}
			}()
			if err := auth.Verify("dev-1", sig); !errors.Is(err, ErrDeviceAuthFailed) {
				t.Errorf("case %d: Verify = %v, want ErrDeviceAuthFailed", i, err)
			}
		}()
	}
}

func TestDeviceVerify_NoOutstandingChallengeFails(t *testing.T) {
	lookup := newFakeLookup()
	pub, priv, _ := ed25519.GenerateKey(nil)
	lookup.add("dev-1", pub, false)

	auth := NewDeviceAuthenticator(lookup)
	sig := ed25519.Sign(priv, []byte("whatever, nothing was issued"))

	if err := auth.Verify("dev-1", sig); !errors.Is(err, ErrDeviceAuthFailed) {
		t.Fatalf("Verify with no issued challenge = %v, want ErrDeviceAuthFailed", err)
	}
}

// TestDeviceAuthenticator_ConcurrentUse exercises IssueChallenge and Verify
// from many goroutines at once across distinct devices. Run with -race; the
// point of this test is what `go test -race` reports, not its assertions.
func TestDeviceAuthenticator_ConcurrentUse(t *testing.T) {
	lookup := newFakeLookup()
	auth := NewDeviceAuthenticator(lookup)

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := deviceIDFor(i)
			pub, priv, _ := ed25519.GenerateKey(nil)
			lookup.add(id, pub, false)

			nonce, err := auth.IssueChallenge(id)
			if err != nil {
				t.Errorf("device %s: IssueChallenge: %v", id, err)
				return
			}
			sig := ed25519.Sign(priv, nonce)
			if err := auth.Verify(id, sig); err != nil {
				t.Errorf("device %s: Verify: %v", id, err)
			}
		}(i)
	}
	wg.Wait()
}

func deviceIDFor(i int) string {
	return "dev-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
}
