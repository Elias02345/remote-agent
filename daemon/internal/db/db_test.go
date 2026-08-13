package db

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func openTemp(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func mustCreate(t *testing.T, d *DB, id string) {
	t.Helper()
	if err := d.CreateSession(Session{ID: id, TmuxSession: "ccr-" + id, Shell: "/bin/bash"}); err != nil {
		t.Fatalf("CreateSession(%s): %v", id, err)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "twice.db")
	for i := 0; i < 2; i++ {
		d, err := Open(path)
		if err != nil {
			t.Fatalf("Open pass %d: %v", i, err)
		}
		d.Close()
	}
}

func TestCreateAndGetSession(t *testing.T) {
	d := openTemp(t)
	mustCreate(t, d, "s1")

	got, err := d.GetSession("s1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.TmuxSession != "ccr-s1" {
		t.Errorf("tmux_session = %q, want %q", got.TmuxSession, "ccr-s1")
	}
	if got.Status != StatusOpen {
		t.Errorf("new session status = %q, want %q", got.Status, StatusOpen)
	}
	if got.CreatedAt == 0 || got.LastActiveAt == 0 {
		t.Errorf("timestamps not defaulted: created=%d last_active=%d", got.CreatedAt, got.LastActiveAt)
	}
}

func TestListOnlyReturnsOpenSessions(t *testing.T) {
	d := openTemp(t)
	mustCreate(t, d, "open-one")
	mustCreate(t, d, "to-close")

	if err := d.CloseSession("to-close"); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}

	list, err := d.ListOpenSessions()
	if err != nil {
		t.Fatalf("ListOpenSessions: %v", err)
	}
	if len(list) != 1 || list[0].ID != "open-one" {
		t.Fatalf("expected only open-one, got %+v", list)
	}
}

// A session must only ever become 'closed' through an explicit user action,
// so closing an already-closed one is not a silent no-op: it reports that
// there was nothing open to close.
func TestCloseIsOnlyValidFromOpen(t *testing.T) {
	d := openTemp(t)
	mustCreate(t, d, "s1")

	if err := d.CloseSession("s1"); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := d.CloseSession("s1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second close: got %v, want sql.ErrNoRows", err)
	}
}

func TestCloseUnknownSessionReportsNoRows(t *testing.T) {
	d := openTemp(t)
	if err := d.CloseSession("nope"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("got %v, want sql.ErrNoRows", err)
	}
}

func TestRename(t *testing.T) {
	d := openTemp(t)
	mustCreate(t, d, "s1")

	if err := d.RenameSession("s1", "build box"); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	got, err := d.GetSession("s1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Name != "build box" {
		t.Errorf("name = %q, want %q", got.Name, "build box")
	}

	if err := d.RenameSession("nope", "x"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rename unknown: got %v, want sql.ErrNoRows", err)
	}
}

// The CHECK constraint from Section 5.3 is what keeps a typo'd status out of
// the table; without it 'clsoed' would silently hide a session from every
// list query forever.
func TestStatusCheckConstraintRejectsGarbage(t *testing.T) {
	d := openTemp(t)
	err := d.CreateSession(Session{ID: "bad", TmuxSession: "t", Status: "clsoed"})
	if err == nil {
		t.Fatal("expected the CHECK constraint to reject an invalid status")
	}
}

func TestDuplicateIDRejected(t *testing.T) {
	d := openTemp(t)
	mustCreate(t, d, "dup")
	if err := d.CreateSession(Session{ID: "dup", TmuxSession: "x"}); err == nil {
		t.Fatal("expected primary key conflict on duplicate session id")
	}
}
