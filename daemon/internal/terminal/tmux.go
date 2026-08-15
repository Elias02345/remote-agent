// Package terminal owns tmux session lifecycle and the raw PTY passthrough
// (docs/ARCHITECTURE.md Section 5.1).
//
// The daemon never interprets terminal output. It starts `tmux attach-session`
// inside a PTY and moves bytes between that PTY and the WebSocket unchanged.
// All emulation happens client-side in xterm.dart / xterm.js. tmux control
// mode (-CC) is deliberately not used: it is an iTerm2-specific structured
// protocol for pane layout, not a byte stream, and full-screen TUIs like
// Claude Code would not survive it.
package terminal

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/creack/pty"
)

// Term is the terminal type announced to programs inside the session.
// tmux-256color is what Section 5.1 asks for; it carries 256 colours,
// alternate-screen support and the capabilities full-screen TUIs expect.
const Term = "tmux-256color"

// Tmux runs tmux commands. The binary is configurable so tests can point at a
// stub, but the default is whatever is on PATH.
type Tmux struct {
	Bin string
}

// NewTmux returns a Tmux using the tmux binary on PATH.
func NewTmux() *Tmux { return &Tmux{Bin: "tmux"} }

func (t *Tmux) bin() string {
	if t.Bin == "" {
		return "tmux"
	}
	return t.Bin
}

func (t *Tmux) run(args ...string) ([]byte, error) {
	cmd := exec.Command(t.bin(), args...)
	cmd.Env = append(os.Environ(), "TERM="+Term)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// SetGlobalWindowSize applies `set -g window-size latest`.
//
// Without it tmux shrinks a session to the smallest attached client, so having
// the same session open on a phone and a desktop at once would squash the
// layout to phone width. "latest" makes the session follow whichever device
// most recently sent input instead.
//
// Failing here is not fatal: it usually just means no tmux server is running
// yet, and the option is re-applied per session at creation time.
func (t *Tmux) SetGlobalWindowSize() error {
	_, err := t.run("set-option", "-g", "window-size", "latest")
	return err
}

// EnableTruecolor tells tmux that attached clients can render 24-bit colour.
//
// Without this, tmux quantises truecolor escapes down to the 256-colour
// palette before they ever reach the client — verified against a live session:
// `\033[38;2;98;168;232m` arrived as palette index 74 instead of #62A8E8.
// Section 5.1 lists truecolor as a requirement, so the default is wrong for us.
//
// It appends rather than replacing, because tmux ships useful defaults in this
// option (xterm clipboard support, cursor styles) that a bare `set` would drop.
// It checks first so repeated calls cannot grow the value without bound.
//
// **Capabilities are decided when a client attaches**, so this must run before
// any `attach-session` — setting it afterwards has no effect on clients that
// are already connected.
func (t *Tmux) EnableTruecolor() error {
	out, err := t.run("show-options", "-g", "terminal-features")
	if err == nil && strings.Contains(string(out), "RGB") {
		return nil
	}
	_, err = t.run("set-option", "-ags", "terminal-features", ",*:RGB")
	return err
}

// HasSession reports whether a tmux session with that name exists.
func (t *Tmux) HasSession(name string) bool {
	cmd := exec.Command(t.bin(), "has-session", "-t", name)
	return cmd.Run() == nil
}

// NewSession creates a detached tmux session. cwd and shell may be empty, in
// which case tmux picks its defaults.
func (t *Tmux) NewSession(name, cwd, shell string) error {
	args := []string{"new-session", "-d", "-s", name}
	if cwd != "" {
		args = append(args, "-c", cwd)
	}
	if shell != "" {
		args = append(args, shell)
	}
	if _, err := t.run(args...); err != nil {
		return err
	}
	// Per-session as well as global: the global option only affects sessions
	// created after it was set, and the server may have been started by
	// something else entirely.
	if _, err := t.run("set-option", "-t", name, "window-size", "latest"); err != nil {
		return err
	}

	// Creating a session may have started the tmux server, so this is the first
	// point where the server option can reliably be set — and it has to happen
	// before the first attach, since client capabilities are fixed at attach
	// time. Not fatal: losing truecolor degrades to 256 colours, which is worth
	// a warning but not a failed session.
	if err := t.EnableTruecolor(); err != nil {
		return nil //nolint:nilerr // deliberate: see comment above
	}
	return nil
}

// CapturePane returns the current visible screen of a session, escape codes
// included.
//
// This is what a reconnecting client gets before the live stream starts, so it
// sees the current state immediately instead of replaying the whole scrollback.
// The -e flag is what preserves colour and attribute sequences; without it the
// client would render a flat, colourless snapshot.
func (t *Tmux) CapturePane(name string) ([]byte, error) {
	cmd := exec.Command(t.bin(), "capture-pane", "-p", "-e", "-t", name)
	cmd.Env = append(os.Environ(), "TERM="+Term)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("capture-pane for %s: %w", name, err)
	}
	return out, nil
}

// KillSession terminates a tmux session and everything running in it.
func (t *Tmux) KillSession(name string) error {
	_, err := t.run("kill-session", "-t", name)
	return err
}

// Attachment is a live `tmux attach-session` running inside a PTY.
type Attachment struct {
	PTY *os.File
	cmd *exec.Cmd
}

// Attach starts `tmux attach-session -t name` in its own PTY sized cols x rows.
// The caller pumps bytes between a.PTY and the client without inspecting them.
func (t *Tmux) Attach(name string, cols, rows uint16) (*Attachment, error) {
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}

	cmd := exec.Command(t.bin(), "attach-session", "-t", name)
	cmd.Env = append(os.Environ(), "TERM="+Term)

	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, fmt.Errorf("attach to %s: %w", name, err)
	}
	return &Attachment{PTY: f, cmd: cmd}, nil
}

// Resize changes the PTY window size. tmux reacts by reflowing the session,
// which with window-size=latest means this client becomes the one the layout
// follows.
func (a *Attachment) Resize(cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return nil
	}
	return pty.Setsize(a.PTY, &pty.Winsize{Cols: cols, Rows: rows})
}

// Close detaches this client. The tmux session itself keeps running — that is
// the entire point of using tmux as the persistence layer, so a dropped
// connection must never take the session with it.
func (a *Attachment) Close() error {
	if a.PTY != nil {
		a.PTY.Close()
	}
	if a.cmd != nil && a.cmd.Process != nil {
		_ = a.cmd.Process.Kill()
		_, _ = a.cmd.Process.Wait()
	}
	return nil
}
