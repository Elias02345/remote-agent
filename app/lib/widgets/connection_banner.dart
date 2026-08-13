/// The connection banner shown above a terminal.
library;

import 'package:flutter/material.dart';

import '../api/session_socket.dart';
import '../design/theme.dart';
import '../design/tokens.dart';

/// Reports transport state above the terminal rect.
///
/// Two rules from the design system, both load-bearing:
///
///  * **It never overlays the terminal.** It sits above the rect and pushes it
///    down, because covering output the user is reading to tell them about the
///    network is the wrong trade every time.
///  * **The text names what happened to the device, not to the session.** The
///    tmux session on the server is unaffected by a dropped WebSocket, and
///    wording like "session lost" would be a lie that costs the user real
///    worry.
class ConnectionBanner extends StatelessWidget {
  const ConnectionBanner({super.key, required this.state});

  final ConnectionState state;

  static const _transientHeight = 26.0;
  static const _disconnectedHeight = 60.0;

  bool get _isVisible => state != ConnectionState.connected;

  @override
  Widget build(BuildContext context) {
    if (!_isVisible) return const SizedBox.shrink();

    final c = context.ccr;
    final (Color edge, String text, String? detail) = switch (state) {
      ConnectionState.connecting => (c.statusConnecting, 'Connecting…', null),
      ConnectionState.reconnecting => (c.statusConnecting, 'Reconnecting…', null),
      ConnectionState.disconnected => (
          c.statusDisconnected,
          'This device lost the connection.',
          'The session keeps running on the server. Reconnecting automatically.',
        ),
      ConnectionState.ended => (
          c.statusEnded,
          'This session has ended.',
          'It was closed explicitly; nothing is running any more.',
        ),
      ConnectionState.connected => (c.statusConnected, '', null),
    };

    final tall = detail != null;

    return Container(
      width: double.infinity,
      height: tall ? _disconnectedHeight : _transientHeight,
      padding: const EdgeInsets.symmetric(horizontal: CcrSpace.s4),
      decoration: BoxDecoration(
        color: tall ? c.surfaceOverlay : c.surfaceRaised,
        border: Border(top: BorderSide(color: edge, width: 2)),
      ),
      child: Column(
        mainAxisAlignment: MainAxisAlignment.center,
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(text, style: CcrType.label.copyWith(color: c.textPrimary)),
          if (detail != null)
            Text(detail, style: CcrType.bodySm.copyWith(color: c.textSecondary)),
        ],
      ),
    );
  }
}
