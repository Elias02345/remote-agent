# app

The cross-platform client (architecture Section 6): Android, Windows and
Linux from one Flutter codebase, `xterm.dart` for terminal emulation. Web is
not a supported target — it was dropped rather than carried as a fourth
platform nobody was testing.

> **Status: Phase 10 (device pairing) built.** Key generation, secure
> storage, per-request challenge signing and the pairing screen
> (`lib/screens/pairing.dart`) are in place and tested. The three factors
> are completed in the system browser against the daemon's own pairing
> page — WebAuthn is a browser API, and keeping the password out of this
> app means a compromised app never sees it. See
> [What's not built](#whats-not-built-and-why) for what remains.

## Running it

All three targets have platform projects. Android additionally needs a
signing key for release builds (see below).

```bash
cd app
flutter pub get
flutter run -d linux    # or -d windows, or an Android device
```

Point it at a running daemon (`cd daemon && go run ./cmd/claudecode-remoted
--db /tmp/ccr.db --lock-dir /tmp/ccr-locks`, see `daemon/README.md`). Unlike
earlier phases there is no compiled-in default address any more — the app
asks for the daemon's address on first run and persists it (`lib/api/server_config.dart`).
The old default, the client's own loopback address, meant "the phone itself"
on a phone, which is never where the daemon actually runs.

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

CI also builds every platform target — Linux on the Ubuntu runner, Windows on
its own runner. `analyze` and `test` never touch a platform runner, so they
cannot tell you whether the app still builds for one.

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

## What Phase 10 added

- **`lib/api/device_identity.dart`** — generates and stores the device's
  Ed25519 keypair (Section 5.4) behind `flutter_secure_storage`, so the
  private key lands in Android Keystore / Windows DPAPI / libsecret rather
  than plain app storage. Plain storage is included in a phone backup;
  restoring that backup on another device would clone a "working" device
  identity nothing can tell apart from the original.
- **`lib/api/server_config.dart`** — persists the operator-supplied daemon
  address (SharedPreferences) and validates it before it is ever used: an
  absolute http/https URI with a host, not a relative path or a bare scheme.
- **`lib/api/pairing_client.dart`** — the unauthenticated REST client for
  `/owner/pair/*`. Kept separate from `ApiClient` on purpose: pairing has to
  work before there is a device credential to sign a request with, so it
  must never go through the header-attaching path the rest of the app uses.
- **`deviceAuthHeaders` in `lib/api/client.dart`** — the `authHeaders`
  callback `ApiClient` and `TusUploader` both take: fetches a fresh
  `/auth/challenge`, signs it with the device key, attaches
  `X-Device-Id`/`X-Device-Signature`. Also fixed `ApiClient.streamTicket` to
  call the daemon's actual ticket route, `/auth/ws-ticket` — it was posting
  to `/sessions/{id}/ticket`, which the daemon has never served.
- **`lib/screens/pairing.dart`** — walks `/owner/pair/start` →
  (password/TOTP/passkey, completed in the system browser, never in this
  app) → polling `GET /owner/pair/status` → one `POST /owner/pair/complete`,
  then stores the device_id and
  hands control back to `main.dart`. Includes an address entry step when
  none is configured yet, and a "forget this device" action.
- **`lib/main.dart`** now routes to `PairingScreen` until both a device_id
  and a server address exist, and to `SessionListScreen` once they do.

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

- **The file browser screen is not built.** `lib/api/` has the client for it
  and it is tested; no screen uses it. `ROADMAP.md` Phase 8 listed it in scope.
- **Passkeys need `CCR_PUBLIC_DOMAIN` set before they work at all** (D-04 in
  `ROADMAP.md`). The registration and assertion paths are built now, but a
  passkey is cryptographically bound to a relying-party ID, and this repo
  ships no default domain — a guessed one could reach production, and an RP
  ID cannot be changed afterwards without invalidating every passkey already
  registered. Until the operator sets it, every passkey path fails closed and
  pairing cannot complete. That is the intended behaviour, not a gap.
- **No end-to-end run against a real authenticator.** The authorisation rules
  around registration are unit-tested; a full ceremony needs a real browser
  and a real authenticator against a real domain, which nothing in CI has.
- **Push notifications are not added client-side.** Update summaries,
  pending-reboot notices, and failed-update errors already go out server-side
  via ntfy (architecture Section 4.5, `D-03` in `ROADMAP.md`). A Flutter
  notification plugin in the app would duplicate a channel the daemon already
  has, for no capability gain yet. (The separate `from-agent/` file-arrival
  watcher from Section 7.3 was evaluated and explicitly *not* built — see
  `G-06` in `ROADMAP.md` — so there is no notification to duplicate there
  either.)
