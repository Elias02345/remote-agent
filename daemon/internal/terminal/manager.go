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
// Order matters: the tmux session is created first, because a database row
// pointing at a tmux session that does not exist would show up in the session
// list as a terminal the user can never attach to.
func (m *Manager) Create(name, cwd, shell string) (db.Session, error) {
	id, err := NewID()
	if err != nil {
		return db.Session{}, err
	}
	tmuxName := "ccr-" + id

	if err := m.Tmux.NewSession(tmuxName, cwd, shell); err != nil {
		return db.Session{}, err
	}

	s := db.Session{ID: id, Name: name, TmuxSession: tmuxName, Cwd: cwd, Shell: shell}
	if err := m.DB.CreateSession(s); err != nil {
		// Roll the tmux session back so a failed create does not leak a
		// detached session nobody has a handle to any more.
		_ = m.Tmux.KillSession(tmuxName)
		return db.Session{}, err
	}

	if err := m.Locks.Acquire(id); err != nil {
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
	if err := m.DB.CloseSession(id); err != nil {
		return err
	}

	killErr := m.Tmux.KillSession(s.TmuxSession)

	// Release even when killing tmux failed: the session is closed as far as
	// the user and the database are concerned, and a lock left behind for a
	// session nobody can reopen would block system updates forever.
	if err := m.Locks.Release(id); err != nil {
		return err
	}
	return killErr
}
