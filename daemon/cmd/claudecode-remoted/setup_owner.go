package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/Elias02345/remote-agent/daemon/internal/identity"
)

// runSetupOwner generates the owner credentials the pairing flow needs and
// prints them for the operator to put in the service's environment file.
//
// Without this, the daemon is fail-closed to the point of being unusable: the
// password and TOTP factors are unsatisfiable while their flags are empty, and
// there is otherwise no way to produce a valid Argon2id hash.
//
// The password is read from **stdin**, never taken as a flag. A flag value
// lands in the process table where any local user can read it with `ps`, and
// in the operator's shell history — for the one secret that gates every
// pairing, that is the wrong default.
func runSetupOwner(in io.Reader, out io.Writer, email string) error {
	reader := bufio.NewReader(in)

	fmt.Fprintln(out, "Owner credential setup")
	fmt.Fprintln(out, "Enter the owner password, then press Enter.")
	fmt.Fprint(out, "password: ")

	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return fmt.Errorf("read password: %w", err)
	}
	password := strings.TrimRight(line, "\r\n")
	if password == "" {
		return fmt.Errorf("password must not be empty")
	}

	hash, err := identity.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	accountName := email
	if accountName == "" {
		accountName = "owner"
	}
	secret, uri, err := identity.GenerateTOTPSecret("ClaudeCode Remote", accountName)
	if err != nil {
		return fmt.Errorf("generate TOTP secret: %w", err)
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Put these in /etc/claudecode-remote/.env — never in the repo,")
	fmt.Fprintln(out, "and never on the command line, where ps would expose them:")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "CCR_OWNER_EMAIL=%s\n", accountName)
	fmt.Fprintf(out, "CCR_OWNER_PASSWORD_HASH=%s\n", hash)
	fmt.Fprintf(out, "CCR_OWNER_TOTP_SECRET=%s\n", secret)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Add this to your authenticator app now — the secret is not shown again:")
	fmt.Fprintf(out, "%s\n", uri)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "The passkey factor stays unsatisfiable until the WebAuthn relying-party")
	fmt.Fprintln(out, "domain is decided (D-04). Pairing cannot complete before then, by design.")

	return nil
}
