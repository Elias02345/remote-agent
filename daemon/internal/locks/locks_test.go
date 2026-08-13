package locks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTemp(t *testing.T) *Manager {
	t.Helper()
	m, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

func TestAcquireCreatesTerminalPrefixedFile(t *testing.T) {
	m := newTemp(t)
	if err := m.Acquire("abc123"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	name := filepath.Base(m.Path("abc123"))
	// The updater's stale cleanup matches only "agent-*". A terminal lock that
	// did not carry this prefix would be swept away after 6 h, which is exactly
	// what must never happen.
	if !strings.HasPrefix(name, "terminal-") {
		t.Fatalf("lock file %q does not carry the terminal- prefix", name)
	}
	if !m.Held("abc123") {
		t.Fatal("Held returned false right after Acquire")
	}
}

func TestAcquireIsIdempotent(t *testing.T) {
	m := newTemp(t)
	for i := 0; i < 3; i++ {
		if err := m.Acquire("s1"); err != nil {
			t.Fatalf("Acquire pass %d: %v", i, err)
		}
	}
	list, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected exactly one lock after repeated Acquire, got %v", list)
	}
}

func TestReleaseRemovesLock(t *testing.T) {
	m := newTemp(t)
	if err := m.Acquire("s1"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := m.Release("s1"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if m.Held("s1") {
		t.Fatal("lock still held after Release")
	}
}

func TestReleaseOfAbsentLockIsNotAnError(t *testing.T) {
	m := newTemp(t)
	if err := m.Release("never-existed"); err != nil {
		t.Fatalf("Release of absent lock: %v", err)
	}
}

func TestList(t *testing.T) {
	m := newTemp(t)
	for _, id := range []string{"a", "b"} {
		if err := m.Acquire(id); err != nil {
			t.Fatalf("Acquire(%s): %v", id, err)
		}
	}
	// An agent lock in the same directory must not be reported as a terminal
	// session — the two types share a directory but nothing else.
	if err := os.WriteFile(filepath.Join(m.Dir(), "agent-4242"), nil, 0o644); err != nil {
		t.Fatalf("write agent lock: %v", err)
	}

	list, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 terminal locks, got %v", list)
	}
	for _, id := range list {
		if id != "a" && id != "b" {
			t.Errorf("unexpected id %q in list", id)
		}
	}
}

// A lock path built from unvalidated input is a path traversal waiting for
// someone to add a user-supplied session id.
func TestPathCannotEscapeTheLockDirectory(t *testing.T) {
	m := newTemp(t)
	p := m.Path("../../etc/passwd")
	if filepath.Dir(p) != m.Dir() {
		t.Fatalf("lock path %q escaped the lock directory %q", p, m.Dir())
	}
	if strings.Contains(filepath.Base(p), "/") || strings.Contains(filepath.Base(p), "..") {
		t.Fatalf("lock file name %q still contains traversal characters", filepath.Base(p))
	}
}
