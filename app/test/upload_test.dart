import 'dart:convert';
import 'dart:io';

import 'package:claudecode_remote/api/client.dart';
import 'package:claudecode_remote/api/upload.dart';
import 'package:crypto/crypto.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

/// Decodes a tus `Upload-Metadata` header back into a map, the way the
/// daemon's `ParseUploadMetadata` (`daemon/internal/files/upload.go`) does.
Map<String, String> _decodeMetadata(String header) {
  final out = <String, String>{};
  for (final part in header.split(',')) {
    final fields = part.trim().split(' ');
    out[fields[0]] = utf8.decode(base64.decode(fields[1]));
  }
  return out;
}

void main() {
  late Directory tmp;

  setUp(() async {
    tmp = await Directory.systemTemp.createTemp('ccr_upload_test_');
  });

  tearDown(() async {
    if (tmp.existsSync()) tmp.deleteSync(recursive: true);
  });

  Future<File> writeFile(String name, List<int> bytes) async {
    final f = File('${tmp.path}/$name');
    await f.writeAsBytes(bytes);
    return f;
  }

  group('encodeTusMetadata', () {
    test('encodes comma-separated "key base64(value)" pairs, per tus', () {
      final header = encodeTusMetadata({'filename': 'photo.png', 'dir': 'proj/to-agent'});
      final pairs = header.split(',');
      expect(pairs, hasLength(2));
      for (final pair in pairs) {
        expect(pair.split(' '), hasLength(2), reason: 'tus wants "key base64value"');
      }
      expect(_decodeMetadata(header), {'filename': 'photo.png', 'dir': 'proj/to-agent'});
    });

    test('the sha256 value round-trips through base64 exactly', () {
      final hash = sha256.convert(utf8.encode('hi')).toString();
      final header = encodeTusMetadata({'filename': 'x', 'sha256': hash});
      expect(_decodeMetadata(header)['sha256'], hash);
    });
  });

  group('TusUploader.upload', () {
    test('a resumed upload continues from the offset the server reports', () async {
      const content = 'HELLO WORLD'; // 11 bytes
      final bytes = utf8.encode(content);
      final localHash = sha256.convert(bytes).toString();

      http.Request? createRequest;
      var patchAttempts = 0;
      final offsetsSeen = <int>[];

      final client = MockClient((req) async {
        switch (req.method) {
          case 'POST':
            createRequest = req;
            return http.Response('', 201, headers: {
              'location': '/files/upload/xyz',
              'upload-offset': '0',
            });
          case 'HEAD':
            // The chunk at offset 4 actually landed server-side even though
            // the client is about to see its request fail outright — this
            // is exactly the case resuming-by-HEAD exists for.
            return http.Response('', 200, headers: {
              'upload-offset': '8',
              'upload-length': '11',
            });
          case 'PATCH':
            patchAttempts++;
            final offset = int.parse(req.headers['Upload-Offset']!);
            offsetsSeen.add(offset);
            if (patchAttempts == 2) {
              throw const SocketException('connection reset');
            }
            final newOffset = offset + req.bodyBytes.length;
            if (newOffset >= bytes.length) {
              return http.Response('', 204, headers: {
                'upload-offset': '$newOffset',
                'x-checksum-sha256': localHash,
              });
            }
            return http.Response('', 204, headers: {'upload-offset': '$newOffset'});
          default:
            throw StateError('unexpected method ${req.method}');
        }
      });

      final file = await writeFile('hello.txt', bytes);
      final uploader = TusUploader(
        baseUri: Uri.parse('http://daemon.local'),
        authHeaders: () async => const <String, String>{},
        httpClient: client,
        chunkSize: 4,
      );

      final result = await uploader.upload(
        file,
        targetDir: 'myproject/to-agent',
        targetName: 'hello.txt',
      );

      expect(result, localHash);
      // Chunk 1 at offset 0, chunk 2's request at offset 4 (which then
      // "fails"), and — the point of the test — the retry resumes from 8,
      // the offset HEAD reported, instead of restarting at 0 or repeating 4.
      expect(offsetsSeen, [0, 4, 8]);

      final meta = _decodeMetadata(createRequest!.headers['Upload-Metadata']!);
      expect(meta['filename'], 'hello.txt');
      expect(meta['dir'], 'myproject/to-agent');
      expect(meta['sha256'], localHash);
      expect(createRequest!.headers['Upload-Length'], '${bytes.length}');
    });

    test('a mismatched server hash raises rather than reporting success', () async {
      final bytes = utf8.encode('payload');
      final localHash = sha256.convert(bytes).toString();

      final client = MockClient((req) async {
        if (req.method == 'POST') {
          return http.Response('', 201, headers: {'location': '/files/upload/mismatch'});
        }
        if (req.method == 'PATCH') {
          return http.Response('', 204, headers: {
            'upload-offset': '${bytes.length}',
            // Deliberately wrong. Covers both the server's own 422 path and
            // corruption that happens only in the response on the way back
            // — Section 8.2 step 4's whole reason to check a second time.
            'x-checksum-sha256': List.filled(64, '0').join(),
          });
        }
        throw StateError('unexpected method ${req.method}');
      });

      final file = await writeFile('payload.bin', bytes);
      final uploader = TusUploader(
        baseUri: Uri.parse('http://daemon.local'),
        authHeaders: () async => const <String, String>{},
        httpClient: client,
      );

      try {
        await uploader.upload(file, targetDir: 'p/to-agent', targetName: 'payload.bin');
        fail('expected a ChecksumMismatchException');
      } on ChecksumMismatchException catch (e) {
        // The exception must carry *what the client computed*, not just
        // that something disagreed — that's what makes the failure
        // actionable rather than a bare "upload failed".
        expect(e.expected, localHash);
        expect(e.reported, isNot(localHash));
      }
    });

    test('a server-side checksum mismatch (422) also raises, not just a missing header',
        () async {
      final bytes = utf8.encode('payload');

      final client = MockClient((req) async {
        if (req.method == 'POST') {
          return http.Response('', 201, headers: {'location': '/files/upload/mismatch'});
        }
        if (req.method == 'PATCH') {
          // Mirrors daemon/internal/files/http.go: 422 with the server's own
          // (different) computed hash, exactly like ErrChecksumMismatch.
          return http.Response('', 422, headers: {
            'upload-offset': '${bytes.length}',
            'x-checksum-sha256': sha256.convert(utf8.encode('corrupted')).toString(),
          });
        }
        throw StateError('unexpected method ${req.method}');
      });

      final file = await writeFile('payload.bin', bytes);
      final uploader = TusUploader(
        baseUri: Uri.parse('http://daemon.local'),
        authHeaders: () async => const <String, String>{},
        httpClient: client,
      );

      await expectLater(
        uploader.upload(file, targetDir: 'p/to-agent', targetName: 'payload.bin'),
        throwsA(isA<ChecksumMismatchException>()),
      );
    });

    test('a non-checksum daemon failure surfaces as ApiException', () async {
      final client = MockClient((req) async => http.Response('forbidden', 403));
      final file = await writeFile('x.bin', [1, 2, 3]);
      final uploader = TusUploader(
        baseUri: Uri.parse('http://daemon.local'),
        authHeaders: () async => const <String, String>{},
        httpClient: client,
      );

      await expectLater(
        uploader.upload(file, targetDir: 'p/to-agent', targetName: 'x.bin'),
        throwsA(isA<ApiException>()),
      );
    });
  });
}
