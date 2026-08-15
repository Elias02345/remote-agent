// store.go is the one file in this package allowed to import package db.
// device.go keeps DeviceLookup an interface specifically so the
// challenge-response logic stays free of the sqlite driver; owner.go keeps
// its own storage need behind a small interface for the same reason. Store
// is the adapter that satisfies both against the real `devices` table.
package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/Elias02345/remote-agent/daemon/internal/db"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// Store adapts *db.DB to this package's storage needs: DeviceLookup for the
// per-connection challenge-response (device.go), and the pairing/device
// management operations for the /owner routes (owner.go).
type Store struct {
	db *db.DB
}

// NewStore wraps database.
func NewStore(database *db.DB) *Store {
	return &Store{db: database}
}

// Device implements DeviceLookup.
//
// pubkey column encoding: standard base64 (encoding/base64.StdEncoding) —
// the same alphabet RequireDevice expects on the X-Device-Signature header,
// so one encoding is used end to end instead of two.
//
// A row whose pubkey does not decode, or decodes to anything other than
// ed25519.PublicKeySize bytes, is reported as absent (ok=false) rather than
// as an error or a partial key: a malformed key must fail authentication
// exactly like an unknown device, never be handed to ed25519.Verify as
// something that might match. A corrupted or truncated DB row must never
// become a bypass.
func (s *Store) Device(id string) (ed25519.PublicKey, bool, bool) {
	dev, err := s.db.GetDevice(id)
	if err != nil {
		return nil, false, false
	}
	raw, err := base64.StdEncoding.DecodeString(dev.PubKey)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, false, false
	}
	return ed25519.PublicKey(raw), dev.Revoked, true
}

// TouchDevice records that a device just authenticated, satisfying
// DeviceToucher (device.go). Without a caller, `last_seen_at` stayed at its
// pairing value forever and the device list — the thing an owner reads to
// decide what to revoke — showed every device as last seen the day it was
// paired.
func (s *Store) TouchDevice(id string) error {
	return s.db.TouchDevice(id)
}

// DeviceInfo mirrors a devices row for the /owner/devices response, without
// exposing db.Device (and therefore package db) to owner.go.
type DeviceInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Platform   string `json:"platform"`
	PubKey     string `json:"pubkey"`
	PairedAt   int64  `json:"paired_at"`
	LastSeenAt int64  `json:"last_seen_at"`
	Revoked    bool   `json:"revoked"`
}

// PairDevice records a device that has completed the full three-factor
// pairing chain. pubKeyB64 is standard-base64-encoded, matching Device
// above.
//
// Nothing here checks that the chain was actually completed — that is
// identity.Attempt.Complete's job (pairing.go), and owner.go is the only
// caller allowed to reach this method. Keeping the check out of the
// storage layer avoids two half-enforcements that could disagree.
func (s *Store) PairDevice(id, name, platform, pubKeyB64 string) error {
	return s.db.PairDevice(db.Device{ID: id, Name: name, Platform: platform, PubKey: pubKeyB64})
}

// ListDevices returns every paired device, revoked or not — same
// visibility rule as db.DB.ListDevices, for the same reason (an operator
// managing devices needs to see revoked ones too, even though a client
// authenticating as one must never learn the difference).
func (s *Store) ListDevices() ([]DeviceInfo, error) {
	devices, err := s.db.ListDevices()
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	out := make([]DeviceInfo, 0, len(devices))
	for _, d := range devices {
		out = append(out, DeviceInfo{
			ID:         d.ID,
			Name:       d.Name,
			Platform:   d.Platform,
			PubKey:     d.PubKey,
			PairedAt:   d.PairedAt,
			LastSeenAt: d.LastSeenAt,
			Revoked:    d.Revoked,
		})
	}
	return out, nil
}

// RevokeDevice marks a device revoked. Returns sql.ErrNoRows, unwrapped
// (via db.DB.RevokeDevice), if id is not a known device, so callers can
// tell "no such device" apart from a real storage error.
func (s *Store) RevokeDevice(id string) error {
	return s.db.RevokeDevice(id)
}

// OwnerUserHandle returns the WebAuthn user handle for the single owner
// account (see identity.newOwnerWebAuthnUser and db.DB.OwnerUserHandle for
// why it is random and never the email).
func (s *Store) OwnerUserHandle() ([]byte, error) {
	handle, err := s.db.OwnerUserHandle()
	if err != nil {
		return nil, fmt.Errorf("owner user handle: %w", err)
	}
	return handle, nil
}

// AddWebAuthnCredential persists a newly registered passkey. name is the
// caller-supplied label the owner sees later (DeviceInfo.Name's
// counterpart for passkeys); everything else is exactly what go-webauthn
// already validated during the registration ceremony.
func (s *Store) AddWebAuthnCredential(name string, cred *webauthn.Credential) error {
	transports := make([]string, len(cred.Transport))
	for i, t := range cred.Transport {
		transports[i] = string(t)
	}
	return s.db.AddWebAuthnCredential(db.WebAuthnCredential{
		ID:              base64.RawURLEncoding.EncodeToString(cred.ID),
		Name:            name,
		PublicKey:       cred.PublicKey,
		AttestationType: cred.AttestationType,
		AAGUID:          cred.Authenticator.AAGUID,
		SignCount:       cred.Authenticator.SignCount,
		BackupEligible:  cred.Flags.BackupEligible,
		BackupState:     cred.Flags.BackupState,
		Transports:      strings.Join(transports, ","),
	})
}

// ListWebAuthnCredentials returns every stored passkey, decoded back into
// go-webauthn's own Credential type so BeginLogin/ValidateLogin can use
// them directly.
//
// A row whose id does not decode as base64url is skipped rather than
// handed to go-webauthn as a corrupted credential id it might partially
// match against — same reasoning as Store.Device's malformed-key handling
// above: a corrupted DB row must never become a bypass or a crash.
func (s *Store) ListWebAuthnCredentials() ([]webauthn.Credential, error) {
	rows, err := s.db.ListWebAuthnCredentials()
	if err != nil {
		return nil, fmt.Errorf("list webauthn credentials: %w", err)
	}
	out := make([]webauthn.Credential, 0, len(rows))
	for _, r := range rows {
		id, err := base64.RawURLEncoding.DecodeString(r.ID)
		if err != nil {
			continue
		}
		var transports []protocol.AuthenticatorTransport
		if r.Transports != "" {
			for _, t := range strings.Split(r.Transports, ",") {
				transports = append(transports, protocol.AuthenticatorTransport(t))
			}
		}
		out = append(out, webauthn.Credential{
			ID:              id,
			PublicKey:       r.PublicKey,
			AttestationType: r.AttestationType,
			Transport:       transports,
			Flags: webauthn.CredentialFlags{
				BackupEligible: r.BackupEligible,
				BackupState:    r.BackupState,
			},
			Authenticator: webauthn.Authenticator{
				AAGUID:    r.AAGUID,
				SignCount: r.SignCount,
			},
		})
	}
	return out, nil
}

// CountWebAuthnCredentials reports how many passkeys are registered — the
// single source of truth passkey_http.go uses to decide whether the
// bootstrap registration window is still open.
func (s *Store) CountWebAuthnCredentials() (int, error) {
	n, err := s.db.CountWebAuthnCredentials()
	if err != nil {
		return 0, fmt.Errorf("count webauthn credentials: %w", err)
	}
	return n, nil
}

// UpdateWebAuthnSignCount persists a credential's updated sign counter
// after a successful assertion. Returns sql.ErrNoRows, unwrapped, if id is
// not a known credential — same convention as RevokeDevice above.
func (s *Store) UpdateWebAuthnSignCount(id string, count uint32) error {
	return s.db.UpdateWebAuthnSignCount(id, count)
}

// TouchWebAuthnCredential records that a passkey just authenticated
// successfully — the WebAuthn equivalent of TouchDevice above.
func (s *Store) TouchWebAuthnCredential(id string) error {
	return s.db.TouchWebAuthnCredential(id)
}
