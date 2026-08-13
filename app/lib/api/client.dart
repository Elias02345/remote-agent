/// REST client for the daemon's session and file APIs.
library;

import 'dart:convert';

import 'package:http/http.dart' as http;

import '../models/session.dart';

/// Thrown when the daemon answers with a non-success status.
class ApiException implements Exception {
  const ApiException(this.statusCode, this.message);
  final int statusCode;
  final String message;

  @override
  String toString() => 'ApiException($statusCode): $message';
}

/// Talks to the daemon over REST.
///
/// Authentication is per-request device signing (architecture Section 5.4);
/// [authHeaders] supplies whatever the current device credential needs so this
/// client does not have to know how signing works.
class ApiClient {
  ApiClient({
    required this.baseUri,
    required this.authHeaders,
    http.Client? httpClient,
  }) : _http = httpClient ?? http.Client();

  final Uri baseUri;
  final Future<Map<String, String>> Function() authHeaders;
  final http.Client _http;

  Future<Map<String, String>> _headers() async => {
        'Content-Type': 'application/json',
        ...await authHeaders(),
      };

  Future<List<Session>> listSessions() async {
    final res = await _http.get(baseUri.replace(path: '/sessions'), headers: await _headers());
    _ensureOk(res);
    final body = jsonDecode(res.body);
    if (body is! List) return const [];
    return body
        .whereType<Map<String, dynamic>>()
        .map(Session.fromJson)
        .toList(growable: false);
  }

  Future<Session> createSession({String? name, String? cwd, String? shell}) async {
    final res = await _http.post(
      baseUri.replace(path: '/sessions'),
      headers: await _headers(),
      body: jsonEncode({
        if (name != null) 'name': name,
        if (cwd != null) 'cwd': cwd,
        if (shell != null) 'shell': shell,
      }),
    );
    _ensureOk(res);
    return Session.fromJson(jsonDecode(res.body) as Map<String, dynamic>);
  }

  Future<Session> renameSession(String id, String name) async {
    final res = await _http.patch(
      baseUri.replace(path: '/sessions/$id'),
      headers: await _headers(),
      body: jsonEncode({'name': name}),
    );
    _ensureOk(res);
    return Session.fromJson(jsonDecode(res.body) as Map<String, dynamic>);
  }

  /// Closes a session permanently.
  ///
  /// This is the only thing that ends a session, and it is irreversible: the
  /// tmux session is killed and its terminal lock released. Callers must have
  /// confirmed with the user first.
  Future<void> closeSession(String id) async {
    final res = await _http.delete(
      baseUri.replace(path: '/sessions/$id'),
      headers: await _headers(),
    );
    _ensureOk(res);
  }

  /// Obtains a short-lived single-use ticket for a WebSocket handshake.
  Future<String> streamTicket(String sessionId) async {
    final res = await _http.post(
      baseUri.replace(path: '/sessions/$sessionId/ticket'),
      headers: await _headers(),
    );
    _ensureOk(res);
    final body = jsonDecode(res.body);
    if (body is Map && body['ticket'] is String) return body['ticket'] as String;
    throw const ApiException(500, 'daemon returned no ticket');
  }

  void _ensureOk(http.Response res) {
    if (res.statusCode >= 200 && res.statusCode < 300) return;
    var message = res.reasonPhrase ?? 'request failed';
    try {
      final body = jsonDecode(res.body);
      if (body is Map && body['error'] is String) message = body['error'] as String;
    } on FormatException {
      // Non-JSON error body; the reason phrase is the best available message.
    }
    throw ApiException(res.statusCode, message);
  }

  void close() => _http.close();
}
