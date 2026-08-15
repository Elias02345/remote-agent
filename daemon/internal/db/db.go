// Package db owns the daemon's SQLite storage: the schema from
// docs/ARCHITECTURE.md Section 5.3 and the queries over it.
//
// modernc.org/sqlite is used rather than mattn/go-sqlite3 because it is a pure
// Go implementation. Section 5.1 wants a single binary with no runtime
// dependencies, and a cgo-linked SQLite would drag a libc dependency into that
// binary and complicate cross-compilation for no benefit here.
package db

import (
	"crypto/rand"
	"database/sql"
	"errors"
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

CREATE TABLE IF NOT EXISTS owner_identity (
    id INTEGER PRIMARY KEY CHECK(id = 1),
    user_handle BLOB NOT NULL
);

CREATE TABLE IF NOT EXISTS webauthn_credentials (
    id TEXT PRIMARY KEY,
    name TEXT,
    public_key BLOB NOT NULL,
    attestation_type TEXT,
    aaguid BLOB,
    sign_count INTEGER DEFAULT 0,
    backup_eligible INTEGER DEFAULT 0,
    backup_state INTEGER DEFAULT 0,
    transports TEXT,
    created_at INTEGER,
    last_used_at INTEGER
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

// Device mirrors a row of the devices table.
type Device struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Platform   string `json:"platform"`
	PubKey     string `json:"pubkey"`
	PairedAt   int64  `json:"paired_at"`
	LastSeenAt int64  `json:"last_seen_at"`
	Revoked    bool   `json:"revoked"`
}

// PairDevice records a device that has completed the full pairing chain.
//
// Nothing in this package checks that the chain was actually completed — that
// is identity.Attempt's job, and it is the only caller allowed to reach here.
// Keeping the check out of the storage layer avoids two half-enforcements that
// disagree; see internal/identity/pairing.go.
func (d *DB) PairDevice(dev Device) error {
	now := time.Now().Unix()
	if dev.PairedAt == 0 {
		dev.PairedAt = now
	}
	_, err := d.sql.Exec(
		`INSERT INTO devices (id, name, platform, pubkey, paired_at, last_seen_at, revoked)
		 VALUES (?, ?, ?, ?, ?, ?, 0)`,
		dev.ID, dev.Name, dev.Platform, dev.PubKey, dev.PairedAt, dev.PairedAt,
	)
	if err != nil {
		return fmt.Errorf("pair device %s: %w", dev.ID, err)
	}
	return nil
}

// GetDevice returns one device, revoked or not.
//
// Revoked devices are returned rather than hidden: the caller has to be able
// to tell "revoked" from "never existed" internally, even though it must not
// expose that difference to a client.
func (d *DB) GetDevice(id string) (Device, error) {
	var dev Device
	var revoked int
	err := d.sql.QueryRow(
		`SELECT id, COALESCE(name,''), COALESCE(platform,''), pubkey,
		        COALESCE(paired_at,0), COALESCE(last_seen_at,0), revoked
		 FROM devices WHERE id = ?`, id,
	).Scan(&dev.ID, &dev.Name, &dev.Platform, &dev.PubKey,
		&dev.PairedAt, &dev.LastSeenAt, &revoked)
	if err != nil {
		return Device{}, err
	}
	dev.Revoked = revoked != 0
	return dev, nil
}

// ListDevices returns every device, including revoked ones, newest first.
func (d *DB) ListDevices() ([]Device, error) {
	rows, err := d.sql.Query(
		`SELECT id, COALESCE(name,''), COALESCE(platform,''), pubkey,
		        COALESCE(paired_at,0), COALESCE(last_seen_at,0), revoked
		 FROM devices ORDER BY paired_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	defer rows.Close()

	out := []Device{}
	for rows.Next() {
		var dev Device
		var revoked int
		if err := rows.Scan(&dev.ID, &dev.Name, &dev.Platform, &dev.PubKey,
			&dev.PairedAt, &dev.LastSeenAt, &revoked); err != nil {
			return nil, fmt.Errorf("scan device: %w", err)
		}
		dev.Revoked = revoked != 0
		out = append(out, dev)
	}
	return out, rows.Err()
}

// RevokeDevice marks a device revoked.
//
// The row is kept rather than deleted: a deleted device id could be paired
// again and silently inherit the trust of the revoked one, and the audit
// trail of "this device existed and was revoked" is worth more than the row.
func (d *DB) RevokeDevice(id string) error {
	res, err := d.sql.Exec(`UPDATE devices SET revoked = 1 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("revoke device %s: %w", id, err)
	}
	return requireOneRow(res, id)
}

// TouchDevice records that a device authenticated successfully.
func (d *DB) TouchDevice(id string) error {
	_, err := d.sql.Exec(`UPDATE devices SET last_seen_at = ? WHERE id = ?`,
		time.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("touch device %s: %w", id, err)
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

// WebAuthnCredential mirrors a row of the webauthn_credentials table. It
// deliberately uses only primitive types (no go-webauthn types) so this
// package never has to import the WebAuthn library — the identity package's
// Store (internal/identity/store.go) is where the conversion to and from
// webauthn.Credential happens, the same split that already exists for
// Device/DeviceInfo above.
type WebAuthnCredential struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	PublicKey       []byte `json:"public_key"`
	AttestationType string `json:"attestation_type"`
	AAGUID          []byte `json:"aaguid"`
	SignCount       uint32 `json:"sign_count"`
	BackupEligible  bool   `json:"backup_eligible"`
	BackupState     bool   `json:"backup_state"`
	Transports      string `json:"transports"` // comma separated
	CreatedAt       int64  `json:"created_at"`
	LastUsedAt      int64  `json:"last_used_at"`
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// OwnerUserHandle returns the WebAuthn user handle for the single owner
// account, generating and persisting a fresh random one on first call.
//
// This is never the owner's email — see identity.newOwnerWebAuthnUser's doc
// comment for why — and it must stay stable across every ceremony run
// against it: WebAuthn requires the handle to identify the same "user" every
// time, so a new one per call would make every already-registered passkey
// belong to a different user as far as the authenticator is concerned.
//
// INSERT OR IGNORE handles two callers racing to bootstrap the handle at
// once: only one insert wins, and the loser reads back the winner's row
// rather than believing its own unused random bytes are now the handle in
// use.
func (d *DB) OwnerUserHandle() ([]byte, error) {
	var handle []byte
	err := d.sql.QueryRow(`SELECT user_handle FROM owner_identity WHERE id = 1`).Scan(&handle)
	if err == nil {
		return handle, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("read owner user handle: %w", err)
	}

	handle = make([]byte, 32)
	if _, err := rand.Read(handle); err != nil {
		return nil, fmt.Errorf("generate owner user handle: %w", err)
	}
	if _, err := d.sql.Exec(`INSERT OR IGNORE INTO owner_identity (id, user_handle) VALUES (1, ?)`, handle); err != nil {
		return nil, fmt.Errorf("store owner user handle: %w", err)
	}
	if err := d.sql.QueryRow(`SELECT user_handle FROM owner_identity WHERE id = 1`).Scan(&handle); err != nil {
		return nil, fmt.Errorf("read owner user handle after insert: %w", err)
	}
	return handle, nil
}

// AddWebAuthnCredential persists a newly registered passkey.
func (d *DB) AddWebAuthnCredential(c WebAuthnCredential) error {
	now := time.Now().Unix()
	if c.CreatedAt == 0 {
		c.CreatedAt = now
	}
	_, err := d.sql.Exec(
		`INSERT INTO webauthn_credentials
		    (id, name, public_key, attestation_type, aaguid, sign_count, backup_eligible, backup_state, transports, created_at, last_used_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Name, c.PublicKey, c.AttestationType, c.AAGUID, c.SignCount,
		boolToInt(c.BackupEligible), boolToInt(c.BackupState), c.Transports, c.CreatedAt, c.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("add webauthn credential %s: %w", c.ID, err)
	}
	return nil
}

// ListWebAuthnCredentials returns every registered passkey, oldest first.
func (d *DB) ListWebAuthnCredentials() ([]WebAuthnCredential, error) {
	rows, err := d.sql.Query(
		`SELECT id, COALESCE(name,''), public_key, COALESCE(attestation_type,''), aaguid,
		        COALESCE(sign_count,0), COALESCE(backup_eligible,0), COALESCE(backup_state,0),
		        COALESCE(transports,''), COALESCE(created_at,0), COALESCE(last_used_at,0)
		 FROM webauthn_credentials ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list webauthn credentials: %w", err)
	}
	defer rows.Close()

	out := []WebAuthnCredential{}
	for rows.Next() {
		var c WebAuthnCredential
		var backupEligible, backupState int
		if err := rows.Scan(&c.ID, &c.Name, &c.PublicKey, &c.AttestationType, &c.AAGUID,
			&c.SignCount, &backupEligible, &backupState, &c.Transports, &c.CreatedAt, &c.LastUsedAt); err != nil {
			return nil, fmt.Errorf("scan webauthn credential: %w", err)
		}
		c.BackupEligible = backupEligible != 0
		c.BackupState = backupState != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountWebAuthnCredentials reports how many passkeys are registered. This is
// the single source of truth identity.Owner uses to decide whether the
// unauthenticated bootstrap registration window is still open — read fresh
// on every call, never cached, by design (see passkey_http.go).
func (d *DB) CountWebAuthnCredentials() (int, error) {
	var n int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM webauthn_credentials`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count webauthn credentials: %w", err)
	}
	return n, nil
}

// UpdateWebAuthnSignCount persists a credential's updated sign counter after
// a successful assertion.
func (d *DB) UpdateWebAuthnSignCount(id string, count uint32) error {
	res, err := d.sql.Exec(`UPDATE webauthn_credentials SET sign_count = ? WHERE id = ?`, count, id)
	if err != nil {
		return fmt.Errorf("update sign count for credential %s: %w", id, err)
	}
	return requireOneRow(res, id)
}

// TouchWebAuthnCredential records that a passkey just authenticated
// successfully — the WebAuthn equivalent of TouchDevice above.
func (d *DB) TouchWebAuthnCredential(id string) error {
	_, err := d.sql.Exec(`UPDATE webauthn_credentials SET last_used_at = ? WHERE id = ?`,
		time.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("touch webauthn credential %s: %w", id, err)
	}
	return nil
}
