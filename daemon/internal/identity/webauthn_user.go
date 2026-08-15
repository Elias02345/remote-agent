// webauthn_user.go implements webauthn.User for the single owner account
// (Section 10.2's third factor). One account, one identity, presented
// identically to every WebAuthn ceremony — registration and login alike.
package identity

import "github.com/go-webauthn/webauthn/webauthn"

// credentialLister loads the owner's stored passkeys. Defined here, by the
// consumer, in the same style as DeviceLookup (device.go) and
// ownerDeviceStore (owner.go) — *Store satisfies it against the real
// webauthn_credentials table.
type credentialLister interface {
	ListWebAuthnCredentials() ([]webauthn.Credential, error)
}

// ownerWebAuthnUser implements webauthn.User for the single owner account.
type ownerWebAuthnUser struct {
	handle      []byte
	name        string
	displayName string
	creds       credentialLister
}

// newOwnerWebAuthnUser builds the identity every WebAuthn ceremony —
// registration and login alike — presents for the owner account.
//
// handle must be the random value from owner_identity
// (db.DB.OwnerUserHandle), never the email: a user handle is transmitted to
// the authenticator and stored on it, so an opaque handle is the whole
// point of using one instead of a username — putting the owner's email
// address on every registered authenticator would defeat it.
func newOwnerWebAuthnUser(handle []byte, name, displayName string, creds credentialLister) *ownerWebAuthnUser {
	return &ownerWebAuthnUser{handle: handle, name: name, displayName: displayName, creds: creds}
}

func (u *ownerWebAuthnUser) WebAuthnID() []byte   { return u.handle }
func (u *ownerWebAuthnUser) WebAuthnName() string { return u.name }

func (u *ownerWebAuthnUser) WebAuthnDisplayName() string {
	if u.displayName != "" {
		return u.displayName
	}
	return u.name
}

// WebAuthnCredentials loads the owner's stored passkeys fresh on every
// call rather than caching them alongside the user value: the credential
// set changes over the account's lifetime (none during bootstrap, one right
// after registration, more later), and a stale copy here would make
// BeginLogin build an allow-list, or ValidateLogin match against a list,
// that no longer reflects what is actually stored.
func (u *ownerWebAuthnUser) WebAuthnCredentials() []webauthn.Credential {
	if u.creds == nil {
		return nil
	}
	creds, err := u.creds.ListWebAuthnCredentials()
	if err != nil {
		// The interface this satisfies (webauthn.User) has no error
		// return, so a storage failure here can only degrade to "no
		// credentials" — which fails the ceremony closed (an empty
		// allow-list, or nothing to validate an assertion against) rather
		// than silently succeeding against a list this call could not
		// actually confirm.
		return nil
	}
	return creds
}
