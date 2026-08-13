/// The connection indicator for a session row.
library;

import 'package:flutter/material.dart';

import '../api/session_socket.dart';
import '../design/theme.dart';
import '../design/tokens.dart';

/// An 8×8 status indicator inside a 20px hit area.
///
/// **The shape carries the state as well as the colour.** Colour alone fails
/// for anyone with a colour-vision deficiency, and this indicator is often the
/// only thing distinguishing two otherwise identical rows:
///
/// | state        | shape          |
/// |--------------|----------------|
/// | connected    | filled circle  |
/// | connecting   | hollow ring    |
/// | reconnecting | hollow ring    |
/// | disconnected | filled square  |
/// | ended        | dash           |
class StatusDot extends StatelessWidget {
  const StatusDot({super.key, required this.state, this.semanticLabel});

  final ConnectionState state;
  final String? semanticLabel;

  static const _dotSize = 8.0;
  static const _hitArea = 20.0;

  Color _color(CcrColors c) => switch (state) {
        ConnectionState.connected => c.statusConnected,
        ConnectionState.connecting => c.statusConnecting,
        ConnectionState.reconnecting => c.statusConnecting,
        ConnectionState.disconnected => c.statusDisconnected,
        ConnectionState.ended => c.statusEnded,
      };

  String get _label => switch (state) {
        ConnectionState.connected => 'connected',
        ConnectionState.connecting => 'connecting',
        ConnectionState.reconnecting => 'reconnecting',
        ConnectionState.disconnected => 'disconnected',
        ConnectionState.ended => 'session ended',
      };

  @override
  Widget build(BuildContext context) {
    final color = _color(context.ccr);

    return Semantics(
      label: semanticLabel ?? _label,
      child: SizedBox(
        width: _hitArea,
        height: _hitArea,
        child: Center(
          child: CustomPaint(
            size: const Size(_dotSize, _dotSize),
            painter: _DotPainter(state: state, color: color),
          ),
        ),
      ),
    );
  }
}

class _DotPainter extends CustomPainter {
  const _DotPainter({required this.state, required this.color});

  final ConnectionState state;
  final Color color;

  @override
  void paint(Canvas canvas, Size size) {
    final fill = Paint()
      ..color = color
      ..style = PaintingStyle.fill;
    final stroke = Paint()
      ..color = color
      ..style = PaintingStyle.stroke
      ..strokeWidth = 1.5;

    final centre = Offset(size.width / 2, size.height / 2);

    switch (state) {
      case ConnectionState.connected:
        canvas.drawCircle(centre, size.width / 2, fill);
      case ConnectionState.connecting:
      case ConnectionState.reconnecting:
        // A ring, not a smaller dot: hollow reads as "not settled yet" without
        // animation. Nothing here pulses — a blinking indicator next to
        // scrolling terminal output is a distraction the user cannot escape.
        canvas.drawCircle(centre, size.width / 2 - 0.75, stroke);
      case ConnectionState.disconnected:
        canvas.drawRect(Offset.zero & size, fill);
      case ConnectionState.ended:
        canvas.drawRect(
          Rect.fromLTWH(0, centre.dy - 1, size.width, 2),
          fill,
        );
    }
  }

  @override
  bool shouldRepaint(_DotPainter old) => old.state != state || old.color != color;
}
