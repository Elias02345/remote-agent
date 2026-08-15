/// tus (tus.io) resumable upload client against the daemon's
/// `/files/upload` endpoints — the client side of architecture Section 8.2.
library;

import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

import 'package:convert/convert.dart';
import 'package:crypto/crypto.dart';
import 'package:http/http.dart' as http;

import 'client.dart';

/// Thrown when the server's `X-Checksum-SHA256` disagrees with the hash the
/// client computed before the upload started.
///
/// This is Section 8.2 step 4, the *second* comparison: the server already
/// hashed what it wrote and would have discarded the file itself on a
/// mismatch (that case surfaces as a 422 and reaches here too, since the
/// header is still compared). Skipping this check because "the server
/// already checked" throws away the one thing step 4 exists for — catching a
/// corruption that happens in the response on the way back.
class ChecksumMismatchException implements Exception {
  const ChecksumMismatchException(this.expected, this.reported);

  /// The hash computed locally before the upload began.
  final String expected;

  /// What the server sent back in `X-Checksum-SHA256`, or `null` if the
  /// header was missing entirely.
  final String? reported;

  @override
  String toString() =>
      'ChecksumMismatchException: expected $expected, server reported ${reported ?? '(missing)'}';
}

/// Reports bytes sent out of the file's total size, after each chunk.
typedef UploadProgress = void Function(int sent, int total);

/// Encodes a tus `Upload-Metadata` header: comma-separated
/// `key base64(value)` pairs, matching what the daemon's
/// `ParseUploadMetadata` expects on the other end.
///
/// A free function rather than inlined into the request builder so the
/// encoding — the exact thing "per tus" in the task means — has its own
/// direct test instead of only being exercised through a mocked HTTP round
/// trip.
String encodeTusMetadata(Map<String, String> kv) => kv.entries
    .map((e) => '${e.key} ${base64.encode(utf8.encode(e.value))}')
    .join(',');

/// Uploads one local file to the daemon, chunked and resumable, with the
/// double SHA-256 check Section 8.2 specifies.
class TusUploader {
  TusUploader({
    required this.baseUri,
    required this.authHeaders,
    http.Client? httpClient,
    this.chunkSize = 1024 * 1024,
    this.maxAttempts = 5,
  }) : _http = httpClient ?? http.Client();

  /// Base URI of the daemon.
  final Uri baseUri;

  /// Same per-request device-signing hook as [ApiClient.authHeaders]; kept
  /// separate rather than taking an [ApiClient] because uploads use PATCH
  /// and HEAD against a per-upload URL, not the fixed REST routes that
  /// client wraps.
  final Future<Map<String, String>> Function() authHeaders;

  /// Bytes per chunk. 1 MiB by default — large enough that a phone doesn't
  /// spend more time on HTTP round trips than on the network, small enough
  /// that resuming after a drop doesn't re-send much.
  final int chunkSize;

  /// How many times a stalled chunk is retried (each retry re-queries the
  /// server's real offset first) before giving up.
  final int maxAttempts;

  final http.Client _http;

  static const _tusVersion = '1.0.0';

  /// Hashes [file], uploads it to `<targetDir>/<targetName>` (relative to
  /// the store roots — e.g. `myproject/to-agent`), and returns the verified
  /// SHA-256 once both comparisons from Section 8.2 pass.
  ///
  /// Both the hash and the chunks are streamed from disk. Reading the whole
  /// file into memory first was simpler and worked for the photos this is
  /// mostly used for, but it puts a hard ceiling on the feature that has
  /// nothing to do with the protocol: the transfer is resumable and chunked
  /// precisely so a large file is not a single indivisible thing, and buffering
  /// it whole gives that back — a phone picking a video out of its gallery
  /// would be killed by the OS before the first byte went out.
  Future<String> upload(
    File file, {
    required String targetDir,
    required String targetName,
    UploadProgress? onProgress,
  }) async {
    final length = await file.length();
    // Step 1 of Section 8.2: hash locally before a single byte goes out.
    // Chunked so memory stays flat regardless of file size.
    final localHash = await _hashFile(file);

    final uploadUri = await _create(
      length: length,
      dir: targetDir,
      filename: targetName,
      sha256Hex: localHash,
    );

    final handle = await file.open();
    try {
      var offset = 0;
      http.Response? finishing;
      Object? lastError;

      for (var attempt = 1; attempt <= maxAttempts && finishing == null; attempt++) {
        try {
          // An empty file has no chunk to send, so the loop below never runs
          // and never produces a finishing response. tus still expects the
          // upload to be completed, so it gets one zero-length PATCH: the
          // server sees offset == length and finishes it. Without this a
          // zero-byte file could be created but never completed — it would
          // sit as a partial upload forever and the caller would be handed a
          // StateError for a transfer that had nothing wrong with it.
          if (length == 0) {
            finishing = await _patchChunk(uploadUri, Uint8List(0), 0);
            onProgress?.call(0, 0);
            break;
          }

          while (offset < length) {
            final end = offset + chunkSize < length ? offset + chunkSize : length;
            await handle.setPosition(offset);
            final chunk = await handle.read(end - offset);
            final res = await _patchChunk(uploadUri, chunk, offset);
            offset = int.tryParse(res.headers['upload-offset'] ?? '') ?? end;
            onProgress?.call(offset, length);
            if (offset >= length) finishing = res;
          }
        } on Object catch (e) {
          lastError = e;
          // Resume from wherever the server actually landed, not from where
          // this attempt assumed it left off — a chunk can succeed on the
          // wire even when the client never sees the response, and resending
          // it would either double-write or hit ErrOffsetConflict for nothing.
          offset = await _headOffset(uploadUri);
        }
      }

      if (finishing == null) {
        throw lastError ??
            StateError('upload did not complete: no confirmation from the server');
      }

      return _verifyChecksum(finishing, localHash);
    } finally {
      await handle.close();
    }
  }

  /// SHA-256 of [file], read in chunks so a large file does not have to fit in
  /// memory to be hashed.
  static Future<String> _hashFile(File file) async {
    final output = AccumulatorSink<Digest>();
    final input = sha256.startChunkedConversion(output);
    await for (final chunk in file.openRead()) {
      input.add(chunk);
    }
    input.close();
    return output.events.single.toString();
  }

  Future<Uri> _create({
    required int length,
    required String dir,
    required String filename,
    required String sha256Hex,
  }) async {
    final metadata = encodeTusMetadata({
      'filename': filename,
      if (dir.isNotEmpty) 'dir': dir,
      'sha256': sha256Hex,
    });

    final res = await _http.post(
      baseUri.replace(path: '/files/upload'),
      headers: {
        'Tus-Resumable': _tusVersion,
        'Upload-Length': '$length',
        'Upload-Metadata': metadata,
        ...await authHeaders(),
      },
    );

    if (res.statusCode != 201) {
      throw ApiException(res.statusCode, _messageOf(res, 'could not start upload'));
    }
    final location = res.headers['location'];
    if (location == null) {
      throw const ApiException(500, 'daemon returned no Location for the upload');
    }
    return baseUri.resolve(location);
  }

  Future<http.Response> _patchChunk(Uri uploadUri, Uint8List chunk, int offset) async {
    final res = await _http.patch(
      uploadUri,
      headers: {
        'Tus-Resumable': _tusVersion,
        'Upload-Offset': '$offset',
        'Content-Type': 'application/offset+octet-stream',
        ...await authHeaders(),
      },
      body: chunk,
    );

    // 204 is a normal chunk (done or not); 422 is a completed-but-mismatched
    // upload, which is a real outcome to hand to the checksum comparison
    // rather than an HTTP failure to retry — retrying would just recreate
    // the same mismatch. Anything else (409 offset conflict, 5xx, ...) is a
    // real failure and drives the resume-from-HEAD path in [upload].
    if (res.statusCode == 204 || res.statusCode == 422) return res;
    throw ApiException(res.statusCode, _messageOf(res, 'chunk upload failed'));
  }

  Future<int> _headOffset(Uri uploadUri) async {
    final res = await _http.head(
      uploadUri,
      headers: {'Tus-Resumable': _tusVersion, ...await authHeaders()},
    );
    if (res.statusCode != 200) {
      throw ApiException(res.statusCode, _messageOf(res, 'could not query upload offset'));
    }
    return int.tryParse(res.headers['upload-offset'] ?? '') ?? 0;
  }

  /// Step 4 of Section 8.2: compare the server's returned hash against the
  /// one computed locally, a second time, independent of whatever the
  /// server itself already decided.
  String _verifyChecksum(http.Response finishing, String localHash) {
    final serverHash = finishing.headers['x-checksum-sha256'];
    if (serverHash == null || !_equalsIgnoreCase(serverHash, localHash)) {
      throw ChecksumMismatchException(localHash, serverHash);
    }
    if (finishing.statusCode != 204) {
      // Unreachable in practice — the server only echoes a matching hash on
      // its success path — but reporting success on an unexpected status
      // just because the hash happened to match is exactly the kind of
      // silent-shortcut this project's CLAUDE.md rules out.
      throw ApiException(finishing.statusCode, 'unexpected status after checksum match');
    }
    return serverHash;
  }

  static bool _equalsIgnoreCase(String a, String b) => a.toLowerCase() == b.toLowerCase();

  static String _messageOf(http.Response res, String fallback) =>
      res.body.isNotEmpty ? res.body : (res.reasonPhrase ?? fallback);

  void close() => _http.close();
}
