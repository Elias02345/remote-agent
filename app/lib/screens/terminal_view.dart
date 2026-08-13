/// The terminal screen.
library;

import 'package:flutter/material.dart';
import 'package:xterm/xterm.dart';

import '../api/client.dart';
import '../api/session_socket.dart';
import '../design/terminal_theme.dart';
import '../design/theme.dart';
import '../design/tokens.dart';
import '../models/session.dart';
import '../widgets/connection_banner.dart';

/// Shows one session's terminal.
class TerminalScreen extends StatefulWidget {
  const TerminalScreen({super.key, required this.session, required this.api});

  final Session session;
  final ApiClient api;

  @override
  State<TerminalScreen> createState() => _TerminalScreenState();
}

class _TerminalScreenState extends State<TerminalScreen> {
  late final Terminal _terminal;
  late final SessionSocket _socket;
  final TerminalController _controller = TerminalController();

  LinkState _state = LinkState.connecting;
  double _fontSize = CcrType.terminal.fontSize!;

  @override
  void initState() {
    super.initState();

    _terminal = Terminal(
      maxLines: 10000,
      // The daemon reports the session's real size; the emulator must not
      // clamp it to something narrower or TUI layouts wrap incorrectly.
      onResize: (w, h, _, __) => _socket.sendResize(w, h),
    );

    _socket = SessionSocket(
      baseUri: widget.api.baseUri,
      sessionId: widget.session.id,
      ticketProvider: () => widget.api.streamTicket(widget.session.id),
    );

    _socket.output.listen(_terminal.write);
    _socket.state.listen((s) {
      if (mounted) setState(() => _state = s);
    });

    // Deliberately no _terminal.clear() anywhere in the reconnect path. The
    // daemon replays the current screen with `tmux capture-pane -e` the moment
    // a client attaches, so clearing here would blank the screen and then
    // immediately redraw it — a visible flash for no gain.
    _socket.connect();

    _terminal.onOutput = _socket.sendInput;
  }

  @override
  void dispose() {
    // Closes the transport only. The tmux session keeps running; that is the
    // entire premise of the product.
    _socket.dispose();
    _controller.dispose();
    super.dispose();
  }

  void _adjustFont(double delta) {
    setState(() {
      _fontSize = (_fontSize + delta).clamp(
        CcrType.terminalMinSize,
        CcrType.terminalMaxSize,
      );
    });
  }

  Future<void> _confirmClose() async {
    final closed = await showDialog<bool>(
      context: context,
      builder: (context) => const _CloseSessionDialog(),
    );
    if (closed != true || !mounted) return;

    await widget.api.closeSession(widget.session.id);
    if (mounted) Navigator.of(context).pop(true);
  }

  @override
  Widget build(BuildContext context) {
    final c = context.ccr;

    return Scaffold(
      backgroundColor: c.surfaceSunken,
      appBar: AppBar(
        title: Text(widget.session.displayName, style: CcrType.title),
        actions: [
          IconButton(
            tooltip: 'Smaller text',
            onPressed: () => _adjustFont(-CcrType.terminalSizeStep),
            icon: const Icon(Icons.text_decrease),
          ),
          IconButton(
            tooltip: 'Larger text',
            onPressed: () => _adjustFont(CcrType.terminalSizeStep),
            icon: const Icon(Icons.text_increase),
          ),
          IconButton(
            tooltip: 'Close terminal',
            onPressed: _confirmClose,
            icon: const Icon(Icons.close),
          ),
        ],
      ),
      body: Column(
        children: [
          // Above the rect and pushing it down, never over it.
          ConnectionBanner(state: _state),
          Expanded(
            child: Container(
              color: c.surfaceTerminal,
              padding: const EdgeInsets.all(CcrSpace.s2),
              child: TerminalView(
                _terminal,
                controller: _controller,
                theme: buildTerminalTheme(c.terminal),
                // Line height stays fixed while the size scales, so the cell
                // grid stays integral and the rect never grows half-row gaps.
                textStyle: TerminalStyle(
                  fontSize: _fontSize,
                  height: CcrType.terminal.height!,
                  fontFamily: CcrFonts.mono,
                ),
                autofocus: true,
                backgroundOpacity: 1,
              ),
            ),
          ),
        ],
      ),
    );
  }
}

/// Confirmation before ending a session for good.
///
/// Closing is irreversible and is the only way a session ends, so the dialog
/// is biased towards keeping it: "Keep open" is the filled, focused default,
/// and **both Escape and Enter keep the session open**. Only an explicit click
/// on the outlined destructive action closes anything.
class _CloseSessionDialog extends StatelessWidget {
  const _CloseSessionDialog();

  @override
  Widget build(BuildContext context) {
    final c = context.ccr;

    return AlertDialog(
      backgroundColor: c.surfaceOverlay,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(CcrRadius.md),
        side: BorderSide(color: c.statusError),
      ),
      title: Text('Close this terminal?', style: CcrType.title.copyWith(color: c.textPrimary)),
      content: Text(
        'Everything running in it stops. Closing is the only way a session '
        'ends, and it cannot be undone.',
        style: CcrType.body.copyWith(color: c.textSecondary),
      ),
      actions: [
        OutlinedButton(
          onPressed: () => Navigator.of(context).pop(true),
          style: OutlinedButton.styleFrom(
            foregroundColor: c.statusError,
            side: BorderSide(color: c.statusError),
          ),
          child: const Text('Close terminal'),
        ),
        FilledButton(
          autofocus: true,
          onPressed: () => Navigator.of(context).pop(false),
          style: FilledButton.styleFrom(
            backgroundColor: c.accentBase,
            foregroundColor: c.accentOn,
          ),
          child: const Text('Keep open'),
        ),
      ],
    );
  }
}
