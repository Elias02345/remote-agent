import 'dart:convert';
import 'dart:io';

import 'package:claudecode_remote/api/upload.dart';
import 'package:claudecode_remote/design/theme.dart';
import 'package:claudecode_remote/design/tokens.dart';
import 'package:claudecode_remote/models/session.dart';
import 'package:claudecode_remote/services/share_upload_service.dart';
import 'package:claudecode_remote/widgets/share_suggestion_overlay.dart';
import 'package:crypto/crypto.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

const _session = Session(
  id: 's1',
  name: 'agent-run',
  tmuxSession: 'ccr-s1',
  cwd: '/home/agent/myproject',
  shell: '/bin/bash',
  status: 'open',
  createdAt: 0,
  lastActiveAt: 0,
);

void main() {
  group('ShareUploadService', () {
    late Directory tmp;

    setUp(() async {
      tmp = await Directory.systemTemp.createTemp('ccr_share_test_');
    });

    tearDown(() {
      if (tmp.existsSync()) tmp.deleteSync(recursive: true);
    });

    test('uploads into <project>/to-agent/ and builds the Section 8.3 suggestion, '
        'without touching a terminal', () async {
      String? uploadedDir;
      const fileBytes = [1, 2, 3];
      // The real hash of fileBytes — TusUploader computes and checks this
      // for real (see upload_test.dart), so the mock must echo the actual
      // value back or this test would fail for the wrong reason.
      final realHash = sha256.convert(fileBytes).toString();

      final client = MockClient((req) async {
        if (req.method == 'POST') {
          // Confirms *where* the service told the uploader to put the file —
          // the one thing this service is responsible for getting right.
          final metaHeader = req.headers['Upload-Metadata']!;
          uploadedDir = _decodeMetadata(metaHeader)['dir'];
          return http.Response('', 201, headers: {'location': '/files/upload/1'});
        }
        return http.Response('', 204, headers: {
          'upload-offset': '${req.bodyBytes.length}',
          'x-checksum-sha256': realHash,
        });
      });

      final file = File('${tmp.path}/photo.png')..writeAsBytesSync(fileBytes);
      final service = ShareUploadService(
        uploader: TusUploader(
          baseUri: Uri.parse('http://daemon.local'),
          authHeaders: () async => const <String, String>{},
          httpClient: client,
        ),
      );

      final suggestion = await service.shareFile(
        file: file,
        session: _session,
        projectSlug: 'myproject',
      );

      expect(uploadedDir, 'myproject/to-agent');
      expect(suggestion.relativePath, 'to-agent/photo.png');
      expect(suggestion.insertLine, '# New file available: to-agent/photo.png');
      expect(suggestion.sha256, realHash);
      expect(suggestion.session, _session);
    });
  });

  group('ShareSuggestionOverlay', () {
    const suggestion = SharedFileSuggestion(
      session: _session,
      relativePath: 'to-agent/photo.png',
      insertLine: '# New file available: to-agent/photo.png',
      sha256: 'deadbeef',
    );

    Widget wrap(Widget child) => MaterialApp(
          theme: buildTheme(brightness: Brightness.dark, density: CcrDensity.touch),
          home: Scaffold(body: child),
        );

    testWidgets('never calls onInsert on its own — only on an explicit tap', (tester) async {
      var insertCalls = 0;
      await tester.pumpWidget(wrap(ShareSuggestionOverlay(
        suggestion: suggestion,
        onInsert: (_) => insertCalls++,
        onDismiss: () {},
      )));

      // Merely building and settling the widget must not have inserted
      // anything — this is the property Section 8.3 cares about most.
      await tester.pump(const Duration(seconds: 1));
      expect(insertCalls, 0);

      await tester.tap(find.byKey(const Key('ccr-suggestion-insert')));
      expect(insertCalls, 1);
    });

    testWidgets('tapping insert sends exactly the suggested line', (tester) async {
      String? inserted;
      await tester.pumpWidget(wrap(ShareSuggestionOverlay(
        suggestion: suggestion,
        onInsert: (line) => inserted = line,
        onDismiss: () {},
      )));

      await tester.tap(find.byKey(const Key('ccr-suggestion-insert')));
      expect(inserted, '# New file available: to-agent/photo.png');
    });

    testWidgets('dismiss never calls onInsert', (tester) async {
      var insertCalls = 0;
      var dismissed = false;
      await tester.pumpWidget(wrap(ShareSuggestionOverlay(
        suggestion: suggestion,
        onInsert: (_) => insertCalls++,
        onDismiss: () => dismissed = true,
      )));

      await tester.tap(find.byKey(const Key('ccr-suggestion-dismiss')));
      expect(dismissed, true);
      expect(insertCalls, 0);
    });
  });
}

/// Decodes a tus `Upload-Metadata` header, the way the daemon's
/// `ParseUploadMetadata` does.
Map<String, String> _decodeMetadata(String header) {
  final out = <String, String>{};
  for (final part in header.split(',')) {
    final fields = part.trim().split(' ');
    out[fields[0]] = utf8.decode(base64.decode(fields[1]));
  }
  return out;
}
