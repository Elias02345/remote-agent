# app

The cross-platform client (architecture Section 6): Android, Windows, Linux
and Web from one Flutter codebase, `xterm.dart` for terminal emulation.

> **Status: Phase 9 (Android & polish) in progress.** The mobile control bar,
> the resumable upload client, and the share-sheet suggestion overlay are
> built and tested. What's still outstanding is listed under
> [What's not built](#whats-not-built-and-why) and
> [Outstanding: Android platform glue](#outstanding-android-platform-glue)
> below — neither blocks Linux/Windows/Web.

## Running it

There is no `android/` directory yet (see below), so today this runs on
desktop and web only:

```bash
cd app
flutter pub get
flutter run -d linux    # or -d windows, -d chrome
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
(`.github/workflows/ci.yml`, pinned to Flutter 3.24.5). A newer local Flutter
SDK may report `CardTheme`/`DialogTheme` argument-type errors in
`lib/design/theme.dart` that CI does not — that's an SDK-version skew
(`ThemeData.cardTheme`/`dialogTheme` changed their expected parameter type in
a later Flutter release), not a defect introduced by this phase's changes.

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

## Outstanding: Android platform glue

`flutter create` has never been run for this project, so there is no
`android/` directory — hand-writing a Gradle project, manifest, and
`MainActivity` from scratch would be far more likely to be subtly wrong than
useful. The Dart side above is ready to receive a shared file's local path;
what's missing is the OS-level plumbing that produces that path. Once
someone runs:

```bash
cd app
flutter create --platforms=android .
```

apply this:

1. **Add a share-intent plugin** (`receive_sharing_intent` is the usual
   choice — it wraps exactly the manifest + native-side work below) to
   `pubspec.yaml`, or hand-roll the two pieces it would otherwise generate:

2. **`android/app/src/main/AndroidManifest.xml`** — add an intent filter to
   the main `<activity>` so "Send to ClaudeCode Remote" appears in other
   apps' share sheets:

   ```xml
   <activity
       android:name=".MainActivity"
       ...>
       <!-- existing intent-filter (MAIN/LAUNCHER) stays -->

       <intent-filter>
           <action android:name="android.intent.action.SEND" />
           <category android:name="android.intent.category.DEFAULT" />
           <data android:mimeType="*/*" />
       </intent-filter>
       <intent-filter>
           <action android:name="android.intent.action.SEND_MULTIPLE" />
           <category android:name="android.intent.category.DEFAULT" />
           <data android:mimeType="*/*" />
       </intent-filter>
   </activity>
   ```

3. **`android/app/src/main/kotlin/.../MainActivity.kt`** — a share intent
   arrives as a `content://` URI, not a filesystem path, so it needs
   resolving via `ContentResolver` before `ShareUploadService` (which takes a
   `dart:io File`) can use it. `receive_sharing_intent` does this for you; the
   shape of what it does, for reference:

   ```kotlin
   class MainActivity : FlutterFragmentActivity() {
       private val channel = "app.claudecoderemote/share"

       override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
           super.configureFlutterEngine(flutterEngine)
           MethodChannel(flutterEngine.dartExecutor.binaryMessenger, channel)
               .setMethodCallHandler { call, result ->
                   // The plugin (or hand-rolled equivalent) resolves the
                   // incoming Intent's content:// URI to a cached file path
                   // here and returns it to Dart, which then calls
                   // ShareUploadService.shareFile with a real File.
                   result.notImplemented()
               }
       }
   }
   ```

   Note `FlutterFragmentActivity`, not `FlutterActivity` — most share-intent
   and file-picker plugins require it.

4. Wire a session-picker into whatever calls `ShareUploadService.shareFile`:
   an incoming share intent doesn't know which running session's `to-agent/`
   the file belongs to. That selection UI isn't built yet either — it's a
   small piece deliberately left for whoever does this wiring, since it's app
   navigation, not upload plumbing.

This is unverified against a real Android build — there is nothing here to
compile it against yet.

## What's not built and why

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
