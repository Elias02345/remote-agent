package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Elias02345/remote-agent/daemon/internal/identity"
)

func TestSetupOwnerProducesUsableCredentials(t *testing.T) {
	var out bytes.Buffer
	if err := runSetupOwner(strings.NewReader("correct horse battery staple\n"), &out, "owner@example.test"); err != nil {
		t.Fatalf("runSetupOwner: %v", err)
	}

	text := out.String()

	hash := extractValue(t, text, "CCR_OWNER_PASSWORD_HASH=")
	// The generated hash has to actually verify, or setup silently hands the
	// operator a credential that can never authenticate.
	if err := identity.ComparePassword("correct horse battery staple", hash); err != nil {
		t.Fatalf("generated hash does not verify its own password: %v", err)
	}
	if err := identity.ComparePassword("wrong password", hash); err == nil {
		t.Fatal("generated hash accepted the wrong password")
	}

	if secret := extractValue(t, text, "CCR_OWNER_TOTP_SECRET="); secret == "" {
		t.Fatal("no TOTP secret was printed")
	}
	if !strings.Contains(text, "otpauth://") {
		t.Error("no provisioning URI printed; the owner cannot add this to an authenticator")
	}

	// The password itself must never be echoed back.
	if strings.Contains(text, "correct horse battery staple") {
		t.Error("the password was printed back to the terminal")
	}
}

func TestSetupOwnerRejectsEmptyPassword(t *testing.T) {
	var out bytes.Buffer
	if err := runSetupOwner(strings.NewReader("\n"), &out, "owner@example.test"); err == nil {
		t.Fatal("expected an empty password to be refused")
	}
}

func extractValue(t *testing.T, text, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	t.Fatalf("no line starting with %q in output:\n%s", prefix, text)
	return ""
}
