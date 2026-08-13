/// ClaudeCode Remote client.
library;

import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';

import 'api/client.dart';
import 'design/theme.dart';
import 'design/tokens.dart';
import 'screens/session_list.dart';

void main() {
  runApp(const ClaudeCodeRemoteApp());
}

/// Root widget.
class ClaudeCodeRemoteApp extends StatelessWidget {
  const ClaudeCodeRemoteApp({super.key, this.baseUri});

  /// Daemon base URI. Defaults to the loopback address the daemon binds to;
  /// the real address is configured at pairing time.
  final Uri? baseUri;

  /// Touch on Android and iOS, pointer on desktop and desktop web.
  ///
  /// Chosen from the platform rather than the window width: a narrow window on
  /// a desktop still has a mouse, and shrinking hit targets on a phone because
  /// it is in landscape would be worse than useless.
  static CcrDensity densityFor(TargetPlatform platform) =>
      switch (platform) {
        TargetPlatform.android || TargetPlatform.iOS => CcrDensity.touch,
        _ => CcrDensity.pointer,
      };

  @override
  Widget build(BuildContext context) {
    final density = densityFor(defaultTargetPlatform);
    final uri = baseUri ?? Uri.parse('http://127.0.0.1:8080');

    final api = ApiClient(
      baseUri: uri,
      // Device signing lands with the pairing flow; until then the daemon is
      // run with --insecure-no-auth for local development only.
      authHeaders: () async => const <String, String>{},
    );

    return MaterialApp(
      title: 'ClaudeCode Remote',
      debugShowCheckedModeBanner: false,
      theme: buildTheme(brightness: Brightness.light, density: density),
      darkTheme: buildTheme(brightness: Brightness.dark, density: density),
      themeMode: ThemeMode.system,
      home: SessionListScreen(api: api),
    );
  }
}
