/// Regressions for the client-side findings of the 2026-08 security review.
library;

import 'dart:async';
import 'dart:io';

import 'package:claudecode_remote/api/session_socket.dart';
import 'package:claudecode_remote/api/upload.dart';
import 'package:claudecode_remote/services/share_upload_service.dart';
import 'package:crypto/crypto.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

void main() {
  group('sanitiseFilename', () {
    // The suggestion line is built from a filename and offered to the user as
    // text to paste into a live terminal. A share-sheet filename comes from
    // whatever app sent it, so a carriage return turns one suggested comment
    // into two lines — the second of which the shell runs the moment the user
    // taps insert.
    test('strips characters a terminal would execute rather than display', () {
      // Built with fromCharCode rather than written as literals: a raw CR or
      // ESC sitting in a source file is invisible in review, which is exactly
      // the property that makes it worth testing for.
      String ch(int c) => String.fromCharCode(c);
      const cr = 13, lf = 10, esc = 27, nul = 0, del = 127;

      // A carriage return ends the suggested comment line; everything after it
      // becomes a second line the shell runs.
      expect(sanitiseFilename('photo.png${ch(cr)}rm -rf ~'), 'photo.pngrm -rf ~');
      expect(sanitiseFilename('note.txt${ch(lf)}whoami'), 'note.txtwhoami');
      // ESC lets the name rewrite what the terminal shows, so the visible line
      // is not the line that runs.
      expect(sanitiseFilename('a${ch(esc)}[2Kb.txt'), 'a[2Kb.txt');
      expect(sanitiseFilename('nul${ch(nul)}byte.txt'), 'nulbyte.txt');
      expect(sanitiseFilename('del${ch(del)}.txt'), 'del.txt');
    });

    test('keeps only the final path segment', () {
      expect(sanitiseFilename('../../etc/cron.d/evil'), 'evil');
      expect(sanitiseFilename(r'..\..\windows\system32\evil.exe'), 'evil.exe');
    });

    test('never returns an empty name', () {
      expect(sanitiseFilename('\r\n'), 'shared-file');
      expect(sanitiseFilename('/'), 'shared-file');
    });

    test('leaves an ordinary filename alone', () {
      expect(sanitiseFilename('Screenshot 2026-08-15 at 14.02.11.png'),
          'Screenshot 2026-08-15 at 14.02.11.png');
    });
  });

  group('TusUploader', () {
    late Directory tmp;

    setUp(() async => tmp = await Directory.systemTemp.createTemp('ccr_sec_test_'));
    tearDown(() async {
      if (tmp.existsSync()) tmp.deleteSync(recursive: true);
    });

    // The chunk loop is `while (offset < length)`, which never runs for an
    // empty file: no chunk was ever sent, so the server never saw the upload
    // completed and the caller got a StateError for a transfer with nothing
    // wrong with it. The partial upload then sat on the daemon's disk forever.
    test('completes a zero-byte file instead of failing', () async {
      final emptyHash = sha256.convert(<int>[]).toString();
      final patches = <int>[];

      final client = MockClient((req) async {
        switch (req.method) {
          case 'POST':
            return http.Response('', 201,
                headers: {'location': '/files/upload/zero', 'upload-offset': '0'});
          case 'PATCH':
            patches.add(req.contentLength);
            return http.Response('', 204, headers: {
              'upload-offset': '0',
              'x-checksum-sha256': emptyHash,
            });
          default:
            throw StateError('unexpected method ${req.method}');
        }
      });

      final file = File('${tmp.path}/empty.txt')..writeAsBytesSync(<int>[]);
      final uploader = TusUploader(
        baseUri: Uri.parse('http://daemon.local'),
        authHeaders: () async => const <String, String>{},
        httpClient: client,
      );

      final result = await uploader.upload(file, targetDir: 'p/to-agent', targetName: 'empty.txt');

      expect(result, emptyHash);
      expect(patches, [0], reason: 'exactly one zero-length PATCH completes the upload');
    });

    // Hashing and chunking both stream from disk now, so a file larger than a
    // comfortable buffer must go through without being loaded whole.
    test('uploads a file larger than its chunk size without buffering it all',
        () async {
      final bytes = List<int>.generate(300 * 1024, (i) => i % 251);
      final hash = sha256.convert(bytes).toString();
      final received = <int>[];

      final client = MockClient((req) async {
        if (req.method == 'POST') {
          return http.Response('', 201,
              headers: {'location': '/files/upload/big', 'upload-offset': '0'});
        }
        received.addAll(req.bodyBytes);
        return http.Response('', 204, headers: {
          'upload-offset': '${received.length}',
          'x-checksum-sha256': hash,
        });
      });

      final file = File('${tmp.path}/big.bin')..writeAsBytesSync(bytes);
      final uploader = TusUploader(
        baseUri: Uri.parse('http://daemon.local'),
        authHeaders: () async => const <String, String>{},
        httpClient: client,
        chunkSize: 64 * 1024,
      );

      expect(
        await uploader.upload(file, targetDir: 'p/to-agent', targetName: 'big.bin'),
        hash,
      );
      expect(received, equals(bytes), reason: 'every byte arrives, in order');
    });
  });

  group('SessionSocket', () {
    // sendInput used to write to whatever channel was last stored, including a
    // closed one mid-reconnect. The keystrokes vanished with no echo, no error
    // and no way for the user to tell they never left the device.
    test('reports that input was not sent while disconnected', () {
      final socket = SessionSocket(
        baseUri: Uri.parse('http://daemon.local'),
        sessionId: 's1',
        ticketProvider: () async => 'ticket',
      );
      addTearDown(socket.dispose);

      expect(socket.sendInput('ls\n'), isFalse,
          reason: 'never connected, so there is nothing to send on');
    });

    // Opening a connection has two awaits in it. Disposing during either one
    // used to leave the late socket stored and its listener pushing into
    // controllers dispose had already closed.
    test('a connection that arrives after dispose is abandoned, not stored', () async {
      final ticketRequested = Completer<void>();
      final releaseTicket = Completer<String>();

      final socket = SessionSocket(
        baseUri: Uri.parse('http://daemon.local'),
        sessionId: 's1',
        ticketProvider: () {
          if (!ticketRequested.isCompleted) ticketRequested.complete();
          return releaseTicket.future;
        },
      );

      final connecting = socket.connect();
      await ticketRequested.future;

      // Dispose while the ticket fetch is still outstanding.
      await socket.dispose();
      releaseTicket.complete('late-ticket');

      // The whole point: this must complete quietly rather than throwing
      // "Cannot add new events after calling close" from a closed controller.
      await expectLater(connecting, completes);
    });
  });

  group('Backoff', () {
    test('caps instead of doubling forever', () {
      final b = Backoff(
        initial: const Duration(milliseconds: 500),
        max: const Duration(seconds: 15),
      );
      final delays = List.generate(40, (_) => b.next());
      expect(delays.first, const Duration(milliseconds: 500));
      expect(delays.last, const Duration(seconds: 15));
      for (final d in delays) {
        expect(d, lessThanOrEqualTo(const Duration(seconds: 15)));
        expect(d, greaterThan(Duration.zero), reason: 'a shift overflow would produce 0 or negative');
      }
    });
  });
}
