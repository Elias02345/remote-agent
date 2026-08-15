/// ClaudeCode Remote client.
library;

import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';

import 'api/client.dart';
import 'api/device_identity.dart';
import 'api/server_config.dart';
import 'design/theme.dart';
import 'design/tokens.dart';
import 'screens/pairing.dart';
import 'screens/session_list.dart';

void main() {
  runApp(ClaudeCodeRemoteApp());
}

/// Root widget.
///
/// Routes to [PairingScreen] until the device has a stored device_id and a
/// configured daemon address, then to [SessionListScreen] — every session
/// and file endpoint requires a paired device (architecture Section 5.4), so
/// nothing past pairing is reachable before both exist.
class ClaudeCodeRemoteApp extends StatefulWidget {
  ClaudeCodeRemoteApp({super.key, DeviceIdentity? identity, ServerConfig? serverConfig})
      : identity = identity ?? DeviceIdentity(),
        serverConfig = serverConfig ?? ServerConfig();

  final DeviceIdentity identity;
  final ServerConfig serverConfig;

  /// Touch on Android and iOS, pointer on desktop.
  ///
  /// Chosen from the platform rather than the window width: a narrow window
  /// on a desktop still has a mouse, and shrinking hit targets on a phone
  /// because it is in landscape would be worse than useless.
  static CcrDensity densityFor(TargetPlatform platform) =>
      switch (platform) {
        TargetPlatform.android || TargetPlatform.iOS => CcrDensity.touch,
        _ => CcrDensity.pointer,
      };

  @override
  State<ClaudeCodeRemoteApp> createState() => _ClaudeCodeRemoteAppState();
}

class _ClaudeCodeRemoteAppState extends State<ClaudeCodeRemoteApp> {
  bool _checking = true;
  bool _paired = false;
  Uri? _baseUri;

  @override
  void initState() {
    super.initState();
    _check();
  }

  /// Re-reads both halves of "is this app usable yet": a device_id and a
  /// server address. [PairingScreen] calls this back once it has stored the
  /// device_id it just got from `/owner/pair/complete`, so it never has to
  /// know what happens after pairing — this is the only place that decides.
  Future<void> _check() async {
    final paired = await widget.identity.isPaired();
    final baseUri = await widget.serverConfig.baseUri();
    if (!mounted) return;
    setState(() {
      _paired = paired && baseUri != null;
      _baseUri = baseUri;
      _checking = false;
    });
  }

  @override
  Widget build(BuildContext context) {
    final density = ClaudeCodeRemoteApp.densityFor(defaultTargetPlatform);

    final Widget home;
    if (_checking) {
      home = const Scaffold(body: Center(child: CircularProgressIndicator()));
    } else if (!_paired || _baseUri == null) {
      home = PairingScreen(
        identity: widget.identity,
        serverConfig: widget.serverConfig,
        onPaired: _check,
      );
    } else {
      final api = ApiClient(
        baseUri: _baseUri!,
        authHeaders: deviceAuthHeaders(baseUri: _baseUri!, identity: widget.identity),
      );
      home = SessionListScreen(api: api);
    }

    return MaterialApp(
      title: 'ClaudeCode Remote',
      debugShowCheckedModeBanner: false,
      theme: buildTheme(brightness: Brightness.light, density: density),
      darkTheme: buildTheme(brightness: Brightness.dark, density: density),
      themeMode: ThemeMode.system,
      home: home,
    );
  }
}
