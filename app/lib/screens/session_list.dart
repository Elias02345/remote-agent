/// The session list — the app's home screen.
library;

import 'package:flutter/material.dart';

import '../api/client.dart';
import '../api/session_socket.dart';
import '../design/theme.dart';
import '../design/tokens.dart';
import '../models/session.dart';
import '../widgets/status_dot.dart';
import 'terminal_view.dart';

/// Lists open terminals, behaving like browser tabs.
class SessionListScreen extends StatefulWidget {
  const SessionListScreen({super.key, required this.api});

  final ApiClient api;

  @override
  State<SessionListScreen> createState() => _SessionListScreenState();
}

class _SessionListScreenState extends State<SessionListScreen> {
  List<Session> _sessions = const [];
  String? _error;
  bool _loading = true;

  @override
  void initState() {
    super.initState();
    _refresh();
  }

  Future<void> _refresh() async {
    setState(() => _loading = true);
    try {
      final sessions = await widget.api.listSessions();
      if (!mounted) return;
      setState(() {
        _sessions = sessions;
        _error = null;
        _loading = false;
      });
    } on Object catch (e) {
      if (!mounted) return;
      setState(() {
        _error = e.toString();
        _loading = false;
      });
    }
  }

  Future<void> _newTerminal() async {
    try {
      final session = await widget.api.createSession();
      if (!mounted) return;
      await _open(session);
    } on Object catch (e) {
      if (!mounted) return;
      setState(() => _error = e.toString());
    }
  }

  Future<void> _open(Session session) async {
    await Navigator.of(context).push<bool>(
      MaterialPageRoute(
        builder: (_) => TerminalScreen(session: session, api: widget.api),
      ),
    );
    await _refresh();
  }

  @override
  Widget build(BuildContext context) {
    final c = context.ccr;

    return Scaffold(
      backgroundColor: c.surfaceSunken,
      appBar: AppBar(
        title: Text('Terminals', style: CcrType.title),
        actions: [
          IconButton(
            tooltip: 'Refresh',
            onPressed: _refresh,
            icon: const Icon(Icons.refresh),
          ),
        ],
      ),
      floatingActionButton: FloatingActionButton.extended(
        onPressed: _newTerminal,
        backgroundColor: c.accentBase,
        foregroundColor: c.accentOn,
        icon: const Icon(Icons.add),
        label: Text('New terminal', style: CcrType.label),
      ),
      body: _buildBody(c),
    );
  }

  Widget _buildBody(CcrColors c) {
    if (_loading) return const Center(child: CircularProgressIndicator());

    if (_error != null) {
      return Center(
        child: Padding(
          padding: const EdgeInsets.all(CcrSpace.s6),
          child: Text(
            _error!,
            style: CcrType.body.copyWith(color: c.statusError),
            textAlign: TextAlign.center,
          ),
        ),
      );
    }

    if (_sessions.isEmpty) {
      return Center(
        child: Padding(
          padding: const EdgeInsets.all(CcrSpace.s6),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              Text('No terminals yet', style: CcrType.display.copyWith(color: c.textPrimary)),
              const SizedBox(height: CcrSpace.s2),
              Text(
                'A terminal you open keeps running until you close it — '
                'through drops, restarts and device switches.',
                style: CcrType.bodySm.copyWith(color: c.textSecondary),
                textAlign: TextAlign.center,
              ),
            ],
          ),
        ),
      );
    }

    final now = DateTime.now();
    return ListView.separated(
      itemCount: _sessions.length,
      separatorBuilder: (_, __) => Divider(height: 1, color: c.borderSubtle),
      itemBuilder: (context, i) => _SessionRow(
        session: _sessions[i],
        now: now,
        onTap: () => _open(_sessions[i]),
      ),
    );
  }
}

/// One row of the session list.
///
/// There is deliberately **no close control here at any breakpoint.** Closing
/// is permanent and is the only way a session ends; putting it a stray tap away
/// from "open" in a list is how someone loses a long-running agent run.
class _SessionRow extends StatelessWidget {
  const _SessionRow({required this.session, required this.now, required this.onTap});

  final Session session;
  final DateTime now;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final c = context.ccr;
    final density = context.ccrDensity;

    return InkWell(
      onTap: onTap,
      child: Container(
        constraints: BoxConstraints(minHeight: density.rowHeight),
        color: c.surfaceRaised,
        padding: const EdgeInsets.symmetric(
          horizontal: CcrSpace.s3,
          vertical: CcrSpace.s2,
        ),
        child: Row(
          children: [
            // Status is not shown by colour alone — StatusDot varies its shape.
            const StatusDot(state: ConnectionState.connected),
            const SizedBox(width: CcrSpace.s2),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                mainAxisSize: MainAxisSize.min,
                children: [
                  Text(
                    session.displayName,
                    style: CcrType.subtitle.copyWith(color: c.textPrimary),
                    overflow: TextOverflow.ellipsis,
                  ),
                  if (session.cwd.isNotEmpty)
                    Text(
                      session.cwd,
                      style: CcrType.monoUi.copyWith(color: c.textSecondary),
                      overflow: TextOverflow.ellipsis,
                    ),
                ],
              ),
            ),
            const SizedBox(width: CcrSpace.s2),
            Text(
              session.lastActivityLabel(now),
              style: CcrType.caption.copyWith(color: c.textTertiary),
            ),
          ],
        ),
      ),
    );
  }
}
