/// Bridges a file shared into the app from outside (Android share sheet,
/// desktop drag & drop, or the in-app "Share file" action) to a session's
/// `to-agent/` folder — architecture Section 8.3.
library;

import 'dart:io';

import '../api/upload.dart';
import '../models/session.dart';

/// What the UI offers the user once a shared file finishes uploading.
///
/// Carries data only, no widget. [insertLine] is the suggestion text from
/// Section 8.3 (`# New file available: to-agent/photo.png`) — nothing in
/// this class or in [ShareUploadService] ever writes it to a terminal.
/// Doing that automatically is the one thing Section 8.3 explicitly rules
/// out: the agent may be mid-keystroke, and stealing its input stream to
/// announce a file would corrupt whatever it was typing. Only a user tap on
/// the "Insert into terminal" button — see `ShareSuggestionOverlay` — ever
/// hands this text to a session's input sink.
class SharedFileSuggestion {
  const SharedFileSuggestion({
    required this.session,
    required this.relativePath,
    required this.insertLine,
    required this.sha256,
  });

  final Session session;

  /// Path of the uploaded file relative to the project's exchange folder,
  /// e.g. `to-agent/photo.png`.
  final String relativePath;

  /// The suggested line to insert into the terminal, never written there
  /// automatically.
  final String insertLine;

  /// The verified SHA-256 [TusUploader.upload] returned. Surfaced for the UI
  /// to show; nothing here re-checks it — that already happened twice, per
  /// Section 8.2.
  final String sha256;
}

/// Uploads an already-resolved local file into a session's exchange folder.
///
/// What actually delivers that local file path — the Android share-sheet
/// intent, a desktop file picker, drag & drop — is platform glue this class
/// does not need to know about. See `app/README.md` for what is still
/// outstanding on the Android side (there is no `android/` directory yet to
/// wire a real share-sheet intent into).
class ShareUploadService {
  const ShareUploadService({required this.uploader});

  final TusUploader uploader;

  /// Uploads [file] into `<projectSlug>/to-agent/` for [session] and returns
  /// the suggestion the UI should offer. Never touches the terminal.
  Future<SharedFileSuggestion> shareFile({
    required File file,
    required Session session,
    required String projectSlug,
    UploadProgress? onProgress,
  }) async {
    final filename = _filenameOf(file);
    final targetDir = '$projectSlug/to-agent';

    final sha256 = await uploader.upload(
      file,
      targetDir: targetDir,
      targetName: filename,
      onProgress: onProgress,
    );

    final relativePath = 'to-agent/$filename';
    return SharedFileSuggestion(
      session: session,
      relativePath: relativePath,
      insertLine: '# New file available: $relativePath',
      sha256: sha256,
    );
  }

  static String _filenameOf(File file) {
    final segments = file.uri.pathSegments;
    final raw = segments.isNotEmpty && segments.last.isNotEmpty ? segments.last : file.path;
    return sanitiseFilename(raw);
  }
}

/// Strips characters a terminal would interpret rather than display.
///
/// [SharedFileSuggestion.insertLine] is offered to the user as text to insert
/// into a live terminal. The line is built from a filename, and a filename that
/// arrives through the Android share sheet comes from whatever app sent it —
/// it is not something this app chose. A name containing a carriage return
/// turns the single suggested comment into two lines, the second of which the
/// shell executes the moment the user taps "Insert into terminal"; an escape
/// character can rewrite what the terminal displays so the visible line is not
/// the line that runs.
///
/// The user tapping the button is what makes this reachable, so it is not
/// silent code execution — but "the user approved it" is worth very little when
/// what they approved was not what they saw. Refusing the characters costs
/// nothing: no legitimate filename contains a newline or a C0 control code.
///
/// Exported for direct testing rather than only through a File.
String sanitiseFilename(String raw) {
  final cleaned = raw.replaceAll(RegExp(r'[\x00-\x1f\x7f]'), '');
  // Strip path separators too: a name is a name, and a shared "file" claiming
  // to be `../../etc/cron.d/evil` has no business becoming a path segment. The
  // daemon refuses this as well (files.Store.Resolve), but the suggestion line
  // is built here and shown to the user regardless of what the server says.
  final base = cleaned.split(RegExp(r'[/\\]')).last;
  return base.isEmpty ? 'shared-file' : base;
}
