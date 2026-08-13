# Client app

> **Status:** implemented for Linux, Windows and Web (Phases 8 and 9), covered
> by `flutter analyze` and 36 tests in CI. The Android platform folder does not
> exist yet — `flutter create` has never been run against this repo.

One Flutter codebase for Android, Windows, Linux and Web, per architecture
Section 6. Terminal rendering is `xterm.dart`; the daemon never interprets
terminal output, so all emulation happens here.

## The design system is a contract, not a suggestion

Every colour, size and spacing value lives in
[`app/lib/design/tokens.dart`](https://github.com/Elias02345/remote-agent/blob/main/app/lib/design/tokens.dart).
There are no literals outside it. Each colour carries the contrast ratio it was
measured at, in a comment next to the value, so a later edit can tell whether
it is still inside the budget.

### The terminal palette

The sixteen ANSI colours are designed, not inherited from xterm. Three
properties hold that a default palette does not:

- **Every colour is readable on the terminal background.** Default xterm blue
  (`#0000EE`) scores 1.14:1 there — effectively invisible, and every `ls`
  directory listing is made of it. Here it is 7.39:1.
- **Green and yellow separate under deuteranopia.** Green is pushed toward mint
  and yellow toward amber, because terminal output uses red and green to mean
  pass and fail constantly.
- **None of the sixteen collides with the app accent.** Otherwise UI chrome and
  program output become visually ambiguous inside the same window.

Two smaller decisions worth knowing: the terminal background is never pure
black (pure black on OLED smears scrolling text and removes the depth step that
bounds the rect), and the cursor is deliberately *not* the accent colour — a
terracotta cursor would be the one accent pixel inside the terminal.

## Decisions that shape the interface

**Disconnected is the brightest thing on screen, and it is not red.** The
session keeps running on the server; only the transport blinked. Colouring that
as an error would be a lie the user pays for in worry. The banner also sits
*above* the terminal and pushes it down — covering output someone is reading in
order to tell them about the network is the wrong trade every time.

**The session list has no close control, at any breakpoint.** Closing is
permanent and is the only way a session ends. Putting it one stray tap from
"open" is how a long-running agent run gets lost.

**The close dialog is biased towards keeping the session.** "Keep open" is the
filled, focused default, and both Escape and Enter keep it open. Only an
explicit click on the outlined action closes anything.

**Status is never carried by colour alone.** The status dot changes shape as
well as hue — filled circle, hollow ring, square, dash.

## The detail that took the longest to get right

`xterm.dart` takes a `String`, but PTY output arrives as arbitrary byte chunks
over the WebSocket. A multi-byte character — a box-drawing glyph in a TUI frame,
say — is routinely split across two frames. Decoding each chunk on its own turns
both halves into U+FFFD, and the terminal draws mojibake exactly along the frame
lines.

The client uses a *chunked* UTF-8 decoder that keeps the partial sequence in its
own state, with `allowMalformed` enabled so a single stray byte cannot end the
stream. Both behaviours have tests; the second one caught a real bug on its
first CI run.

There is also no `clear()` anywhere in the reconnect path. The daemon replays
the current screen with `tmux capture-pane -e` the moment a client attaches, so
clearing would blank the terminal and immediately redraw it.

## Mobile

A control bar above the keyboard carries Esc, Tab, a **latching** Ctrl, arrows,
pipe, slash and paste. While Ctrl is latched the second row swaps the arrows for
C, D, Z and L — Ctrl+arrow has no byte to send, and Ctrl+C is otherwise
unreachable on a touch keyboard. Keys are sized from the touch density token
rather than a literal, because a key that looks right in a screenshot but sits
under 48dp of tappable area is unusable.

Sharing a file from another app uploads it into `to-agent/` and then **offers**
it, with an "insert into terminal" button. It is never typed into the input
stream: the agent may be mid-keystroke, and Section 8.3 is explicit about this.

## Not built

- **FIDO2 hardware keys.** A passkey binds to a relying-party domain, and that
  domain is undecided (D-04). There is nothing to register against, so this is
  absent rather than stubbed.
- **Client-side push.** The daemon already sends through ntfy; duplicating the
  channel in the app buys nothing yet.
- **The Android platform folder.** Run `flutter create --platforms=android .`
  once; the manifest and Kotlin glue are documented in `app/README.md`.
