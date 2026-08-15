package identity

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// Rate limiting for the pairing/login endpoints (Section 10.2): after 5
// failed attempts, an exponentially increasing lockout, tracked
// independently per IP and per account. Both counters are checked on every
// attempt and either one tripping blocks it: locking only the account lets
// an attacker spray many accounts from one IP, and locking only the IP lets
// a botnet grind one account from many IPs.
const (
	// FailureThreshold is how many failures in a row trigger a lockout.
	FailureThreshold = 5
	// baseLockout is the duration of the first lockout for an identity.
	baseLockout = 1 * time.Minute
	// maxLockout caps the exponential growth. Uncapped backoff turns a
	// handful of failures (fat fingers, a forgotten password) into a
	// lockout that outlives the incident that caused it; an hour still
	// meaningfully slows a sustained brute force while keeping the account
	// recoverable within the same working day.
	maxLockout = 1 * time.Hour
)

// ErrRateLimited is returned while an identity (an IP address or an
// account) is locked out.
var ErrRateLimited = errors.New("too many failed attempts, locked out")

// lockState is the failure/lockout bookkeeping for one key (one IP, or one
// account).
type lockState struct {
	failures    int
	lockCount   int
	lockedUntil time.Time
}

// RateLimiter tracks failures per IP and per account independently. Safe
// for concurrent use.
type RateLimiter struct {
	mu      sync.Mutex
	nowFunc func() time.Time
	byIP    map[string]*lockState
	byAcct  map[string]*lockState
}

// NewRateLimiter builds an empty limiter.
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		nowFunc: time.Now,
		byIP:    map[string]*lockState{},
		byAcct:  map[string]*lockState{},
	}
}

// SetClock overrides the clock, for tests.
func (r *RateLimiter) SetClock(f func() time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nowFunc = f
}

// Allow reports whether ip and account are both currently permitted to
// attempt authentication. Either one being locked refuses the attempt.
func (r *RateLimiter) Allow(ip, account string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.nowFunc()
	if s, ok := r.byIP[ip]; ok && now.Before(s.lockedUntil) {
		return fmt.Errorf("%w: ip locked until %s", ErrRateLimited, s.lockedUntil.Format(time.RFC3339))
	}
	if s, ok := r.byAcct[account]; ok && now.Before(s.lockedUntil) {
		return fmt.Errorf("%w: account locked until %s", ErrRateLimited, s.lockedUntil.Format(time.RFC3339))
	}
	return nil
}

// RecordFailure registers one failed attempt against both ip and account.
//
// Call this only for evidence that was actually checked against the account —
// a wrong password, a wrong TOTP code, a failed passkey assertion. For a
// request that never got that far, use RecordIPFailure: see its doc comment for
// why the distinction is the difference between a lockout and a denial of
// service.
func (r *RateLimiter) RecordFailure(ip, account string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.nowFunc()
	recordFailure(r.byIP, ip, now)
	recordFailure(r.byAcct, account, now)
}

// RecordIPFailure registers a failure against the IP alone.
//
// This exists because the account counter is a weapon pointed at the owner. It
// is the only account on the system, so "five failures locks the account" means
// five unauthenticated requests lock the owner out — and if a wrong *email*
// counts, the attacker does not even need to know which email is right. A
// public daemon plus a five-strike account lockout is a one-line denial of
// service against the person who owns the machine.
//
// The rule: guesses that were checked against the account count against the
// account. Everything else costs the IP only.
func (r *RateLimiter) RecordIPFailure(ip string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	recordFailure(r.byIP, ip, r.nowFunc())
}

func recordFailure(m map[string]*lockState, key string, now time.Time) {
	s, ok := m[key]
	if !ok {
		s = &lockState{}
		m[key] = s
	}
	s.failures++
	if s.failures < FailureThreshold {
		return
	}
	s.failures = 0
	s.lockCount++
	s.lockedUntil = now.Add(backoff(s.lockCount))
}

// backoff doubles the lockout duration on each successive lockout for the
// same identity, capped at maxLockout.
func backoff(lockCount int) time.Duration {
	d := baseLockout
	for i := 1; i < lockCount; i++ {
		if d >= maxLockout {
			return maxLockout
		}
		d *= 2
	}
	if d > maxLockout {
		return maxLockout
	}
	return d
}

// RecordSuccess clears both counters for ip and account. A successful
// authentication forgives whatever failures led up to it.
//
// "A successful authentication" means the whole thing, not one step of it.
// Calling this after each satisfied pairing factor would make the factors
// defend each other's counters instead of their own: anyone holding the
// password could satisfy that factor, wipe the counters, then spend five fresh
// guesses on the TOTP code, and repeat — unlimited attempts at a six-digit
// number, with the lockout never firing. Reset once, when the pairing actually
// completes.
func (r *RateLimiter) RecordSuccess(ip, account string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byIP, ip)
	delete(r.byAcct, account)
}

// LimitedVerifier implements Verifier by wrapping another Verifier with the
// lockout above: either counter tripping refuses the attempt before the
// wrapped verifier is even consulted, so a locked-out caller cannot burn
// further guesses against it. limiter is expected to be shared across
// requests so its counters persist between calls.
type LimitedVerifier struct {
	inner   Verifier
	limiter *RateLimiter
	ip      string
	account string
}

// NewLimitedVerifier builds a rate-limited wrapper around inner for one
// (ip, account) pair.
func NewLimitedVerifier(inner Verifier, limiter *RateLimiter, ip, account string) *LimitedVerifier {
	return &LimitedVerifier{inner: inner, limiter: limiter, ip: ip, account: account}
}

// Verify enforces the lockout, then delegates to inner and updates the
// counters from the result.
func (l *LimitedVerifier) Verify(evidence []byte) error {
	if err := l.limiter.Allow(l.ip, l.account); err != nil {
		return err
	}
	if err := l.inner.Verify(evidence); err != nil {
		l.limiter.RecordFailure(l.ip, l.account)
		return err
	}
	l.limiter.RecordSuccess(l.ip, l.account)
	return nil
}
