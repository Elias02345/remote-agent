package terminal

import (
	"strings"
	"testing"
)

// The daemon's own environment carries the owner's Argon2id password hash and
// the raw TOTP secret. tmux is how a coding agent's shell is created, and the
// first tmux invocation starts a tmux *server* that keeps whatever environment
// it was handed — so passing os.Environ() through does not leak the secrets to
// one process, it seeds them into every terminal opened for the life of that
// server. An agent could then read the second factor out of its own shell.
func TestChildEnvDoesNotForwardDaemonSecrets(t *testing.T) {
	t.Setenv("CCR_OWNER_TOTP_SECRET", "JBSWY3DPEHPK3PXP")
	t.Setenv("CCR_OWNER_PASSWORD_HASH", "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA")
	t.Setenv("CCR_OWNER_EMAIL", "owner@example.com")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "not-ours-but-still-not-tmux-business")

	joined := strings.Join(childEnv(), "\n")

	for _, secret := range []string{
		"JBSWY3DPEHPK3PXP",
		"$argon2id$",
		"owner@example.com",
		"not-ours-but-still-not-tmux-business",
	} {
		if strings.Contains(joined, secret) {
			t.Errorf("childEnv() forwards %q to tmux", secret)
		}
	}
}

// The allowlist has to be an allowlist, not an empty environment: a shell with
// no PATH or HOME is a broken terminal, which is a worse bug than the one this
// is fixing.
func TestChildEnvKeepsWhatATerminalActuallyNeeds(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HOME", "/home/agent")

	got := map[string]string{}
	for _, kv := range childEnv() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			got[k] = v
		}
	}

	if got["TERM"] != Term {
		t.Errorf("TERM = %q, want %q", got["TERM"], Term)
	}
	if got["PATH"] != "/usr/bin:/bin" {
		t.Errorf("PATH = %q, want it preserved", got["PATH"])
	}
	if got["HOME"] != "/home/agent" {
		t.Errorf("HOME = %q, want it preserved", got["HOME"])
	}
}
