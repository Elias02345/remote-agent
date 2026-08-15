/// WebSocket transport for one terminal session.
///
/// The daemon sends `{"type":"output","data":"<base64>"}` and accepts
/// `{"type":"input","data":"<base64>"}` and
/// `{"type":"resize","cols":n,"rows":n}`.
library;

import 'dart:async';
import 'dart:convert';

import 'package:web_socket_channel/web_socket_channel.dart';

/// Connection state, mapped 1:1 onto the design system's status tokens.
enum LinkState {
  connecting,
  connected,
  reconnecting,
  disconnected,

  /// The session ended server-side. Distinct from [disconnected]: the session
  /// is gone, not merely unreachable.
  ended,
}

/// Decodes a stream of PTY byte chunks into text without corrupting characters
/// that straddle a chunk boundary.
///
/// This exists because the obvious implementation is wrong. PTY output arrives
/// in arbitrary chunks, and a multi-byte UTF-8 character — or a box-drawing
/// glyph in a TUI frame — is routinely split across two WebSocket frames.
/// Calling `utf8.decode(bytes, allowMalformed: true)` per chunk turns both
/// halves into U+FFFD, and the terminal draws mojibake exactly where the frame
/// lines should be.
///
/// A chunked converter keeps the partial sequence in its own state and emits
/// the character once the rest arrives. `allowMalformed` still matters: real
/// terminal output can carry genuinely invalid bytes, and throwing on them
/// would kill the stream over a stray byte.
class PtyTextDecoder {
  PtyTextDecoder() {
    // allowMalformed: true is load-bearing, not defensive padding. The default
    // `utf8.decoder` *throws* on an invalid byte, and real PTY output contains
    // them — a binary file cat'd by accident, a truncated escape, a stray 0xFF.
    // Throwing here would end the terminal stream over one bad byte, and the
    // session would look dead while it is still running perfectly on the
    // server.
    _sink = const Utf8Decoder(allowMalformed: true).startChunkedConversion(_out);
  }

  final _out = _StringCollector();
  late final Sink<List<int>> _sink;

  /// Feeds one chunk and returns whatever is now complete. May return an empty
  /// string when the chunk ended mid-character — that is correct, not a bug.
  String add(List<int> bytes) {
    _sink.add(bytes);
    return _out.take();
  }

  void close() => _sink.close();
}

class _StringCollector implements Sink<String> {
  final StringBuffer _buf = StringBuffer();

  @override
  void add(String data) => _buf.write(data);

  @override
  void close() {}

  String take() {
    final s = _buf.toString();
    _buf.clear();
    return s;
  }
}

/// Exponential backoff for reconnects.
///
/// Capped on purpose: an uncapped doubling turns a server restart into a
/// twenty-minute wait, and the user is left staring at a disconnected banner
/// long after the daemon came back.
class Backoff {
  Backoff({
    this.initial = const Duration(milliseconds: 500),
    this.max = const Duration(seconds: 15),
  });

  final Duration initial;
  final Duration max;

  int _attempt = 0;

  /// The delay before the next attempt.
  Duration next() {
    final ms = initial.inMilliseconds << _attempt;
    // Stop shifting well before 64 bits run out. The cap below clamps the
    // value anyway, so counting past this point changes nothing.
    if (_attempt < 30) _attempt++;
    return ms >= max.inMilliseconds ? max : Duration(milliseconds: ms);
  }

  /// Call after a successful connection so the next drop starts from the
  /// short delay again.
  void reset() => _attempt = 0;
}

/// A live connection to one session's byte stream.
class SessionSocket {
  SessionSocket({
    required this.baseUri,
    required this.sessionId,
    required this.ticketProvider,
    Backoff? backoff,
  }) : _backoff = backoff ?? Backoff();

  /// Base URI of the daemon, e.g. `https://remote.example`.
  final Uri baseUri;
  final String sessionId;

  /// Supplies a short-lived, single-use ticket for the handshake.
  ///
  /// A browser cannot set headers on a WebSocket handshake, so the credential
  /// travels as a query parameter. That is only acceptable because the ticket
  /// is single-use and expires in seconds — a long-lived token in a URL ends
  /// up in proxy logs and browser history.
  final Future<String> Function() ticketProvider;

  final Backoff _backoff;

  WebSocketChannel? _channel;
  PtyTextDecoder _decoder = PtyTextDecoder();
  bool _closedByUser = false;

  final _output = StreamController<String>.broadcast();
  final _state = StreamController<LinkState>.broadcast();

  /// Decoded terminal text, ready to hand to the emulator.
  Stream<String> get output => _output.stream;

  /// Connection state for the banner.
  Stream<LinkState> get state => _state.stream;

  /// Opens the connection and keeps it open across drops.
  Future<void> connect() async {
    _closedByUser = false;
    await _open(first: true);
  }

  Future<void> _open({required bool first}) async {
    if (_closedByUser) return;
    _state.add(first ? LinkState.connecting : LinkState.reconnecting);

    try {
      final ticket = await ticketProvider();
      final scheme = baseUri.scheme == 'https' ? 'wss' : 'ws';
      final uri = baseUri.replace(
        scheme: scheme,
        path: '/sessions/$sessionId/stream',
        queryParameters: {'ticket': ticket},
      );

      final channel = WebSocketChannel.connect(uri);
      await channel.ready;

      _channel = channel;
      // A fresh decoder per connection: a partial character from before the
      // drop can never be completed, and carrying it over would corrupt the
      // first character after reconnect.
      _decoder = PtyTextDecoder();
      _backoff.reset();
      _state.add(LinkState.connected);

      channel.stream.listen(
        _onMessage,
        onDone: _scheduleReconnect,
        onError: (Object _) => _scheduleReconnect(),
        cancelOnError: true,
      );
    } on Object {
      _scheduleReconnect();
    }
  }

  void _onMessage(dynamic raw) {
    if (raw is! String) return;
    final Object? decoded = jsonDecode(raw);
    if (decoded is! Map) return;

    switch (decoded['type']) {
      case 'output':
        final data = decoded['data'];
        if (data is String) {
          final text = _decoder.add(base64Decode(data));
          if (text.isNotEmpty) _output.add(text);
        }
      case 'exit':
        // The session itself ended. Do not reconnect: there is nothing to
        // reconnect to, and retrying would look like a network problem.
        _closedByUser = true;
        _state.add(LinkState.ended);
        _channel?.sink.close();
    }
  }

  void _scheduleReconnect() {
    if (_closedByUser) return;
    _state.add(LinkState.disconnected);
    Timer(_backoff.next(), () => _open(first: false));
  }

  /// Sends user keystrokes.
  void sendInput(String data) {
    _channel?.sink.add(jsonEncode({
      'type': 'input',
      'data': base64Encode(utf8.encode(data)),
    }));
  }

  /// Reports a new terminal grid size.
  ///
  /// Sent on every grid change: the daemon runs tmux with
  /// `window-size latest`, so whichever client resized last is the one the
  /// session's layout follows.
  void sendResize(int cols, int rows) {
    _channel?.sink.add(jsonEncode({'type': 'resize', 'cols': cols, 'rows': rows}));
  }

  /// Closes the transport. Does **not** end the session: the tmux session on
  /// the server keeps running, which is the whole point of the architecture.
  /// Only an explicit DELETE ends a session.
  Future<void> dispose() async {
    _closedByUser = true;
    await _channel?.sink.close();
    _decoder.close();
    await _output.close();
    await _state.close();
  }
}
