package identity

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestRateLimiterLocksAfterFiveFailures(t *testing.T) {
	now := time.Now()
	r := NewRateLimiter()
	r.SetClock(func() time.Time { return now })

	for i := 0; i < FailureThreshold-1; i++ {
		if err := r.Allow("1.2.3.4", "owner"); err != nil {
			t.Fatalf("Allow before threshold (failure %d) = %v, want nil", i, err)
		}
		r.RecordFailure("1.2.3.4", "owner")
	}
	// One more failure crosses the threshold.
	r.RecordFailure("1.2.3.4", "owner")

	if err := r.Allow("1.2.3.4", "owner"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Allow after %d failures = %v, want ErrRateLimited", FailureThreshold, err)
	}
}

func TestRateLimiterLockoutGrowsOnRepeatedLockouts(t *testing.T) {
	now := time.Now()
	r := NewRateLimiter()
	r.SetClock(func() time.Time { return now })

	lockUntilAfter := func() time.Time {
		for i := 0; i < FailureThreshold; i++ {
			r.RecordFailure("1.2.3.4", "owner")
		}
		s := r.byIP["1.2.3.4"]
		return s.lockedUntil
	}

	firstLockUntil := lockUntilAfter()
	firstDuration := firstLockUntil.Sub(now)

	// Expire the first lockout, then trip a second one.
	now = firstLockUntil.Add(time.Second)
	secondLockUntil := lockUntilAfter()
	secondDuration := secondLockUntil.Sub(now)

	if secondDuration <= firstDuration {
		t.Fatalf("second lockout (%s) did not grow past the first (%s)", secondDuration, firstDuration)
	}
}

func TestRateLimiterLockoutExpires(t *testing.T) {
	now := time.Now()
	r := NewRateLimiter()
	r.SetClock(func() time.Time { return now })

	for i := 0; i < FailureThreshold; i++ {
		r.RecordFailure("1.2.3.4", "owner")
	}
	if err := r.Allow("1.2.3.4", "owner"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Allow while locked = %v, want ErrRateLimited", err)
	}

	now = r.byIP["1.2.3.4"].lockedUntil.Add(time.Second)
	if err := r.Allow("1.2.3.4", "owner"); err != nil {
		t.Fatalf("Allow after lockout expired = %v, want nil", err)
	}
}

func TestRateLimiterSuccessResets(t *testing.T) {
	now := time.Now()
	r := NewRateLimiter()
	r.SetClock(func() time.Time { return now })

	for i := 0; i < FailureThreshold-1; i++ {
		r.RecordFailure("1.2.3.4", "owner")
	}
	r.RecordSuccess("1.2.3.4", "owner")

	// One more failure should not trip the lock: success cleared history.
	r.RecordFailure("1.2.3.4", "owner")
	if err := r.Allow("1.2.3.4", "owner"); err != nil {
		t.Fatalf("Allow after reset = %v, want nil", err)
	}
}

func TestRateLimiterIPAndAccountAreIndependent(t *testing.T) {
	now := time.Now()
	r := NewRateLimiter()
	r.SetClock(func() time.Time { return now })

	// Lock out the account by hammering it from many different IPs.
	for i := 0; i < FailureThreshold; i++ {
		r.RecordFailure(string(rune('a'+i)), "victim-account")
	}
	if err := r.Allow("fresh-ip", "victim-account"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Allow(fresh ip, locked account) = %v, want ErrRateLimited", err)
	}
	if err := r.Allow("fresh-ip", "other-account"); err != nil {
		t.Fatalf("Allow(fresh ip, unrelated account) = %v, want nil (account lock leaked into IP)", err)
	}

	// Lock out one IP by hammering many different accounts from it.
	for i := 0; i < FailureThreshold; i++ {
		r.RecordFailure("attacker-ip", string(rune('a'+i)))
	}
	if err := r.Allow("attacker-ip", "fresh-account"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Allow(locked ip, fresh account) = %v, want ErrRateLimited", err)
	}
	if err := r.Allow("other-ip", "fresh-account"); err != nil {
		t.Fatalf("Allow(unrelated ip, fresh account) = %v, want nil (ip lock leaked into account)", err)
	}
}

func TestRateLimiterConcurrentUse(t *testing.T) {
	r := NewRateLimiter()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ip := "10.0.0.1"
			account := "owner"
			r.RecordFailure(ip, account)
			_ = r.Allow(ip, account)
			r.RecordSuccess(ip, account)
		}(i)
	}
	wg.Wait()
}

func TestLimitedVerifierBlocksAfterLockout(t *testing.T) {
	r := NewRateLimiter()
	lv := NewLimitedVerifier(alwaysFails{}, r, "1.2.3.4", "owner")

	for i := 0; i < FailureThreshold; i++ {
		if err := lv.Verify(nil); err == nil {
			t.Fatal("alwaysFails verifier reported success")
		}
	}
	err := lv.Verify(nil)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Verify after threshold = %v, want ErrRateLimited", err)
	}
}

func TestLimitedVerifierSuccessResetsCounter(t *testing.T) {
	r := NewRateLimiter()
	failing := NewLimitedVerifier(alwaysFails{}, r, "1.2.3.4", "owner")
	for i := 0; i < FailureThreshold-1; i++ {
		_ = failing.Verify(nil)
	}

	ok := NewLimitedVerifier(alwaysOK{}, r, "1.2.3.4", "owner")
	if err := ok.Verify(nil); err != nil {
		t.Fatalf("Verify(alwaysOK) = %v, want nil", err)
	}

	// The reset means one more failure alone should not trip the lock: the
	// call still fails (alwaysFails always fails), but not with
	// ErrRateLimited — the pre-reset failures must not still be counted.
	if err := failing.Verify(nil); errors.Is(err, ErrRateLimited) {
		t.Fatalf("Verify after success reset = %v, want the inner failure, not ErrRateLimited", err)
	}
}
