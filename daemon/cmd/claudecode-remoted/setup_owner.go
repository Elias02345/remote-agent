package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/Elias02345/remote-agent/daemon/internal/identity"
)

// isTerminal reports whether r is an interactive terminal.
//
// stdlib only, on purpose: os.ModeCharDevice is what distinguishes a tty from a
// pipe or a file, and reaching for a dependency to learn that would be a new
// module in go.mod for one bit of information.
func isTerminal(r io.Reader) (*os.File, bool) {
	f, ok := r.(*os.File)
	if !ok {
		return nil, false
	}
	info, err := f.Stat()
	if err != nil {
		return nil, false
	}
	return f, info.Mode()&os.ModeCharDevice != 0
}

// withEchoOff runs fn with terminal echo disabled, restoring it afterwards.
//
// `stty` rather than raw termios via x/sys: this is a one-shot interactive
// setup command on a Linux server that certainly has coreutils, and the
// alternative is platform-specific syscall code plus a new direct dependency
// for a path that runs once per installation.
//
// The restore is deferred, so echo comes back even if fn returns an error or
// the read is interrupted — leaving an operator with an invisible shell would
// be a worse bug than the one this fixes.
func withEchoOff(fn func() error) error {
	stty := func(arg string) error {
		cmd := exec.Command("stty", arg)
		cmd.Stdin = os.Stdin
		return cmd.Run()
	}
	if err := stty("-echo"); err != nil {
		return fmt.Errorf("could not disable terminal echo, refusing to read the password in the clear: %w", err)
	}
	defer func() { _ = stty("echo") }()
	return fn()
}

// readPassword reads the owner password without echoing it.
//
// The previous version read it with bufio.ReadString, which leaves the terminal
// in its default echoing mode — so the one secret that gates every pairing was
// typed in plain view and then sat in the scrollback of whatever shell ran it.
// On this machine that shell is very often a tmux pane this same daemon serves,
// which means the password would be sitting in a buffer any reattaching client
// can scroll back through.
//
// A non-TTY stdin (a pipe, a test, a provisioning script) has no echo to turn
// off, so it falls through to a plain read rather than failing — automation has
// to keep working.
func readPassword(in io.Reader, out io.Writer) (string, error) {
	f, tty := isTerminal(in)
	if !tty {
		fmt.Fprintln(out, "Enter the owner password, then press Enter.")
		fmt.Fprint(out, "password: ")
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("read password: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	}

	var first, second string
	err := withEchoOff(func() error {
		reader := bufio.NewReader(f)

		fmt.Fprint(out, "Owner password (not shown): ")
		line, err := reader.ReadString('\n')
		fmt.Fprintln(out)
		if err != nil && err != io.EOF {
			return fmt.Errorf("read password: %w", err)
		}
		first = strings.TrimRight(line, "\r\n")

		fmt.Fprint(out, "Repeat it: ")
		line, err = reader.ReadString('\n')
		fmt.Fprintln(out)
		if err != nil && err != io.EOF {
			return fmt.Errorf("read password confirmation: %w", err)
		}
		second = strings.TrimRight(line, "\r\n")
		return nil
	})
	if err != nil {
		return "", err
	}
	// Confirmation matters more than usual here: a typo produces a perfectly
	// valid Argon2id hash of the wrong string, and nothing notices until
	// pairing fails with "invalid credentials" months later.
	if first != second {
		return "", fmt.Errorf("the two passwords do not match")
	}
	return first, nil
}

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
	fmt.Fprintln(out, "Owner credential setup")

	password, err := readPassword(in, out)
	if err != nil {
		return err
	}
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
