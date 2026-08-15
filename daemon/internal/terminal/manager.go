package terminal

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/Elias02345/remote-agent/daemon/internal/db"
	"github.com/Elias02345/remote-agent/daemon/internal/locks"
)

// Manager ties the three things a session consists of: a row in the database,
// a tmux session, and a terminal lock the idle updater can see.
type Manager struct {
	DB    *db.DB
	Locks *locks.Manager
	Tmux  *Tmux
}

// NewManager wires a Manager and applies the global window-size option.
func NewManager(database *db.DB, lockMgr *locks.Manager, tmux *Tmux) *Manager {
	m := &Manager{DB: database, Locks: lockMgr, Tmux: tmux}
	// Best effort: both usually fail only because no tmux server is running
	// yet, and NewSession re-applies them once one exists.
	_ = tmux.SetGlobalWindowSize()
	_ = tmux.EnableTruecolor()
	return m
}

// NewID returns a random session identifier.
func NewID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Create starts a new terminal session.
//
// A session is three things — a tmux session, a database row, and a terminal
// lock — and they have to end up either all present or all absent. The lock is
// the one that matters most: without it the idle updater believes the machine
// is idle and will restart services and install packages underneath a terminal
// the user has open. That is exactly the failure the lock exists to prevent, so
// a session that exists without one is worse than no session at all.
//
// The lock is therefore taken FIRST, before anything durable is created. It is
// the only step here that can fail for a reason unrelated to this request — a
// permissions problem on /run/claudecode-locks, a full tmpfs — and finding that
// out after tmux and the database already have state means unwinding two things
// instead of zero.
func (m *Manager) Create(name, cwd, shell string) (db.Session, error) {
	id, err := NewID()
	if err != nil {
		return db.Session{}, err
	}
	tmuxName := "ccr-" + id

	if err := m.Locks.Acquire(id); err != nil {
		return db.Session{}, err
	}

	if err := m.Tmux.NewSession(tmuxName, cwd, shell); err != nil {
		_ = m.Locks.Release(id)
		return db.Session{}, err
	}

	s := db.Session{ID: id, Name: name, TmuxSession: tmuxName, Cwd: cwd, Shell: shell}
	if err := m.DB.CreateSession(s); err != nil {
		// Unwind both, so a failed create does not leave a detached tmux
		// session nobody has a handle to and a lock nothing will ever release.
		_ = m.Tmux.KillSession(tmuxName)
		_ = m.Locks.Release(id)
		return db.Session{}, err
	}

	return m.DB.GetSession(id)
}

// List returns all sessions that have not been explicitly closed.
func (m *Manager) List() ([]db.Session, error) { return m.DB.ListOpenSessions() }

// Get returns one session.
func (m *Manager) Get(id string) (db.Session, error) { return m.DB.GetSession(id) }

// Rename changes a session's display name.
func (m *Manager) Rename(id, name string) error { return m.DB.RenameSession(id, name) }

// Close ends a session for good. This is the only path that sets status
// 'closed' and the only path that removes a terminal lock — a disconnect,
// a daemon restart or a timeout must never end up here.
func (m *Manager) Close(id string) error {
	s, err := m.DB.GetSession(id)
	if err != nil {
		return err
	}

	// tmux first, and a failure here stops the whole close.
	//
	// The previous order marked the row closed, then killed tmux, then released
	// the lock regardless of whether the kill worked — so a failed kill left a
	// live tmux session, with the user's shell and whatever it was running
	// still in it, marked closed in the database, invisible in the session
	// list, and no longer holding an update lock. The user could not reach it
	// again and the updater no longer knew it was there. Retrying was
	// impossible too: the row was already closed, so the retry got ErrNoRows.
	//
	// Refusing to proceed leaves the session exactly as it was, which is a
	// state the user can see and try again from.
	if err := m.Tmux.KillSession(s.TmuxSession); err != nil {
		return fmt.Errorf("close session %s: %w", id, err)
	}

	if err := m.DB.CloseSession(id); err != nil {
		return err
	}

	// Last, and only once the session really is gone. A lock outliving a
	// terminal blocks every future system update; a lock released before the
	// terminal is gone lets an update run underneath it. Doing this after the
	// kill and the row update means neither can happen.
	return m.Locks.Release(id)
}
