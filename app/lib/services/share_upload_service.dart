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
    return segments.isNotEmpty && segments.last.isNotEmpty ? segments.last : file.path;
  }
}
