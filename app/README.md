# app

The cross-platform client (architecture Section 6): Android, Windows, Linux
and Web from one Flutter codebase, `xterm.dart` for terminal emulation.

> **Status: Phase 9 (Android & polish) in progress.** The mobile control bar,
> the resumable upload client, the share-sheet suggestion overlay and the
> Android platform project are built and tested.
>
> **This client cannot yet pair with a daemon that has authentication
> enabled.** There is no key generation, no key storage, no challenge signing
> and no pairing screen, so every authenticated request is refused. Running it
> against a daemon means running that daemon with `--insecure-no-auth`, which
> only works on a loopback address. See
> [What's not built](#whats-not-built-and-why).

## Running it

All four targets have platform projects. Android additionally needs a signing
key for release builds (see below).

```bash
cd app
flutter pub get
flutter run -d linux    # or -d windows, -d chrome, or an Android device
```

Point it at a running daemon (`cd daemon && go run ./cmd/claudecode-remoted
--db /tmp/ccr.db --lock-dir /tmp/ccr-locks`, see `daemon/README.md`) — the
default base URI is `http://127.0.0.1:8080`, matching the daemon's default
listen address.

## Tests

```bash
cd app
flutter test
```

`flutter analyze --fatal-warnings --no-fatal-infos` is the same check CI runs
(`.github/workflows/ci.yml`, pinned to Flutter 3.44.1). Match that version
locally: `ThemeData.cardTheme`/`dialogTheme` changed their expected parameter
type between releases, so a different SDK reports argument-type errors in
`lib/design/theme.dart` that say nothing about the code.

CI also builds every platform target — web and Linux on the Ubuntu runner,
Windows on its own runner. `analyze` and `test` never touch a platform runner,
so they cannot tell you whether the app still builds for one.

## What Phase 9 added

- **`lib/widgets/control_bar.dart`** — the mobile control bar above the
  keyboard (Section 6.2): Esc, Tab, a latching Ctrl, `|`, `/`, paste, and
  arrow keys. While Ctrl is latched, row 2 swaps the arrows for `C`/`D`/`Z`/`L`
  — the Ctrl+letter combinations a touchscreen has no other way to send. Every
  key's tap target is `CcrDensity.touch.minHitArea` (48dp), not the rough
  "44px" a screenshot would still look fine at.
- **`lib/api/upload.dart`** — a tus (tus.io) resumable upload client against
  the daemon's `/files/upload` endpoints (Section 8.2): hashes the file
  locally before sending, uploads in chunks, resumes from the server's real
  `Upload-Offset` on failure instead of restarting, and compares the server's
  returned `X-Checksum-SHA256` against the local hash a second time.
- **`lib/services/share_upload_service.dart`** +
  **`lib/widgets/share_suggestion_overlay.dart`** — the Dart-side half of
  Section 8.3's share flow. The service uploads an already-resolved local
  file into `<project>/to-agent/`; the overlay shows the resulting suggestion
  with an "Insert into terminal" button. **Neither ever writes to a terminal
  on its own** — Section 8.3 is explicit that a file's arrival is a
  suggestion, not an injection, because the agent may be mid-keystroke.

## Android

The `android/` platform project exists and builds. Two things in it are
deliberate rather than template defaults:

- **`applicationId` is `io.github.elias02345.claudecode_remote`**, not
  `com.example.*`. An application id is permanent once anything is installed
  under it.
- **Release builds refuse to sign themselves with the debug key.** Create
  `android/key.properties` (gitignored):

  ```properties
  storeFile=/absolute/path/to/upload-keystore.jks
  storePassword=...
  keyAlias=upload
  keyPassword=...
  ```

  Generate the keystore with:

  ```bash
  keytool -genkey -v -keystore upload-keystore.jks -keyalg RSA -keysize 2048 -validity 10000 -alias upload
  ```

  Without that file `flutter build apk --release` fails with an explanation.
  That is on purpose: a device that installs a debug-signed build can never
  take a properly signed update afterwards, so the convenient fallback costs
  more than it saves.

`MainActivity` handles `ACTION_SEND` and `ACTION_SEND_MULTIPLE`. A shared item
arrives as a `content://` URI whose read grant does not outlive the activity,
so the content is copied into the app's cache directory and Dart is handed a
real path it can still open when an upload retries after a reconnect. Shares
that arrive before the Flutter engine exists (a cold start from the share
sheet) are queued and pulled by Dart via `takePendingShares`.

Still missing on top of that: the session picker that decides *which* session a
shared file belongs to. That is app navigation rather than upload plumbing.

## What's not built and why

- **Device pairing is not implemented in the client.** This is the largest
  outstanding gap in the project, so it is stated first rather than last. The
  daemon's three-factor pairing chain is complete and fail-closed; the app has
  no way to walk it. Missing: Ed25519 key generation, platform-backed key
  storage, challenge signing against `/auth/challenge`, a pairing screen, and
  a setting for the daemon's address (it currently defaults to its own
  loopback address, which on a phone means the phone itself). Until this
  exists, the app can only talk to a daemon started with `--insecure-no-auth`,
  which the daemon refuses on any non-loopback bind address.
- **The file browser screen is not built.** `lib/api/` has the client for it
  and it is tested; no screen uses it. `ROADMAP.md` Phase 8 listed it in scope.
- **FIDO2 / hardware-key support is blocked on D-04** (`ROADMAP.md`), the
  WebAuthn relying-party domain. A passkey is bound to a domain; there is
  nothing to register a credential against until that's decided, so this
  isn't stubbed — a stub would be a WebAuthn flow with nowhere to point,
  which is worse than an honest gap.
- **Push notifications are not added client-side.** Update summaries,
  pending-reboot notices, and failed-update errors already go out server-side
  via ntfy (architecture Section 4.5, `D-03` in `ROADMAP.md`). A Flutter
  notification plugin in the app would duplicate a channel the daemon already
  has, for no capability gain yet. (The separate `from-agent/` file-arrival
  watcher from Section 7.3 was evaluated and explicitly *not* built — see
  `G-06` in `ROADMAP.md` — so there is no notification to duplicate there
  either.)
