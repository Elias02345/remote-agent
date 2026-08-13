/// A terminal session as the daemon reports it.
library;

/// Mirrors the `sessions` row from architecture Section 5.3.
class Session {
  const Session({
    required this.id,
    required this.name,
    required this.tmuxSession,
    required this.cwd,
    required this.shell,
    required this.status,
    required this.createdAt,
    required this.lastActiveAt,
  });

  factory Session.fromJson(Map<String, dynamic> json) => Session(
        id: json['id'] as String,
        name: (json['name'] as String?) ?? '',
        tmuxSession: (json['tmux_session'] as String?) ?? '',
        cwd: (json['cwd'] as String?) ?? '',
        shell: (json['shell'] as String?) ?? '',
        status: (json['status'] as String?) ?? 'open',
        createdAt: (json['created_at'] as int?) ?? 0,
        lastActiveAt: (json['last_active_at'] as int?) ?? 0,
      );

  final String id;
  final String name;
  final String tmuxSession;
  final String cwd;
  final String shell;

  /// Only ever `open` or `closed`, and only an explicit delete produces
  /// `closed` — never a disconnect and never a timeout.
  final String status;

  final int createdAt;
  final int lastActiveAt;

  /// What the row shows as its title.
  String get displayName => name.isNotEmpty ? name : id;

  /// Human-readable last activity, for the row's right column.
  String lastActivityLabel(DateTime now) {
    if (lastActiveAt == 0) return '—';
    final then = DateTime.fromMillisecondsSinceEpoch(lastActiveAt * 1000);
    final d = now.difference(then);
    if (d.inSeconds < 60) return 'just now';
    if (d.inMinutes < 60) return '${d.inMinutes} min ago';
    if (d.inHours < 24) return '${d.inHours} h ago';
    return '${d.inDays} d ago';
  }
}
