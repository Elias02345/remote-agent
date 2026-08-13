// Package db owns the daemon's SQLite storage: the schema from
// docs/ARCHITECTURE.md Section 5.3 and the queries over it.
//
// modernc.org/sqlite is used rather than mattn/go-sqlite3 because it is a pure
// Go implementation. Section 5.1 wants a single binary with no runtime
// dependencies, and a cgo-linked SQLite would drag a libc dependency into that
// binary and complicate cross-compilation for no benefit here.
package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Schema is applied on every open. Every statement is IF NOT EXISTS, so this
// doubles as the migration path for now: additive only, never destructive
// (see CLAUDE.md, data safety).
const Schema = `
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    name TEXT,
    tmux_session TEXT NOT NULL,
    cwd TEXT,
    shell TEXT,
    status TEXT CHECK(status IN ('open','closed')) DEFAULT 'open',
    created_at INTEGER,
    last_active_at INTEGER
);

CREATE TABLE IF NOT EXISTS devices (
    id TEXT PRIMARY KEY,
    name TEXT,
    platform TEXT,
    pubkey TEXT NOT NULL,
    paired_at INTEGER,
    last_seen_at INTEGER,
    revoked INTEGER DEFAULT 0
);
`

// DB wraps the SQLite handle.
type DB struct {
	sql *sql.DB
}

// Open opens (or creates) the database at path and applies the schema.
func Open(path string) (*DB, error) {
	// _pragma=busy_timeout keeps concurrent writers from failing instantly;
	// foreign_keys is off by default in SQLite and has to be asked for.
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)", path)

	handle, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := handle.Ping(); err != nil {
		handle.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	if _, err := handle.Exec(Schema); err != nil {
		handle.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &DB{sql: handle}, nil
}

// Close releases the underlying handle.
func (d *DB) Close() error { return d.sql.Close() }

// Session mirrors a row of the sessions table.
type Session struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	TmuxSession  string `json:"tmux_session"`
	Cwd          string `json:"cwd"`
	Shell        string `json:"shell"`
	Status       string `json:"status"`
	CreatedAt    int64  `json:"created_at"`
	LastActiveAt int64  `json:"last_active_at"`
}

// StatusOpen and StatusClosed are the only two legal values. A session becomes
// closed exclusively through an explicit user DELETE (Section 5.3) — never on
// disconnect, never on a timeout. The principle lives in the data model, not
// only in the UI.
const (
	StatusOpen   = "open"
	StatusClosed = "closed"
)

// CreateSession inserts a new open session.
func (d *DB) CreateSession(s Session) error {
	now := time.Now().Unix()
	if s.CreatedAt == 0 {
		s.CreatedAt = now
	}
	if s.LastActiveAt == 0 {
		s.LastActiveAt = now
	}
	if s.Status == "" {
		s.Status = StatusOpen
	}
	_, err := d.sql.Exec(
		`INSERT INTO sessions (id, name, tmux_session, cwd, shell, status, created_at, last_active_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.Name, s.TmuxSession, s.Cwd, s.Shell, s.Status, s.CreatedAt, s.LastActiveAt,
	)
	if err != nil {
		return fmt.Errorf("create session %s: %w", s.ID, err)
	}
	return nil
}

// GetSession returns one session by id. Returns sql.ErrNoRows if absent.
func (d *DB) GetSession(id string) (Session, error) {
	var s Session
	err := d.sql.QueryRow(
		`SELECT id, COALESCE(name,''), tmux_session, COALESCE(cwd,''), COALESCE(shell,''),
		        status, COALESCE(created_at,0), COALESCE(last_active_at,0)
		 FROM sessions WHERE id = ?`, id,
	).Scan(&s.ID, &s.Name, &s.TmuxSession, &s.Cwd, &s.Shell, &s.Status, &s.CreatedAt, &s.LastActiveAt)
	if err != nil {
		return Session{}, err
	}
	return s, nil
}

// ListOpenSessions returns every session that has not been explicitly closed,
// newest activity first.
func (d *DB) ListOpenSessions() ([]Session, error) {
	rows, err := d.sql.Query(
		`SELECT id, COALESCE(name,''), tmux_session, COALESCE(cwd,''), COALESCE(shell,''),
		        status, COALESCE(created_at,0), COALESCE(last_active_at,0)
		 FROM sessions WHERE status = ? ORDER BY last_active_at DESC`, StatusOpen)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	out := []Session{}
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.Name, &s.TmuxSession, &s.Cwd, &s.Shell,
			&s.Status, &s.CreatedAt, &s.LastActiveAt); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RenameSession sets a new display name.
func (d *DB) RenameSession(id, name string) error {
	res, err := d.sql.Exec(`UPDATE sessions SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return fmt.Errorf("rename session %s: %w", id, err)
	}
	return requireOneRow(res, id)
}

// CloseSession marks a session closed. This is the only transition to
// 'closed', and it happens only on an explicit user action.
func (d *DB) CloseSession(id string) error {
	res, err := d.sql.Exec(
		`UPDATE sessions SET status = ?, last_active_at = ? WHERE id = ? AND status = ?`,
		StatusClosed, time.Now().Unix(), id, StatusOpen)
	if err != nil {
		return fmt.Errorf("close session %s: %w", id, err)
	}
	return requireOneRow(res, id)
}

// TouchSession records activity on a session.
func (d *DB) TouchSession(id string) error {
	_, err := d.sql.Exec(`UPDATE sessions SET last_active_at = ? WHERE id = ?`,
		time.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("touch session %s: %w", id, err)
	}
	return nil
}

func requireOneRow(res sql.Result, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected for %s: %w", id, err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
