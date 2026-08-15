/// REST client for the daemon's session and file APIs.
library;

import 'dart:convert';

import 'package:http/http.dart' as http;

import '../models/session.dart';
import 'device_identity.dart';

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
  ///
  /// [sessionId] is not part of the request: the daemon's ticket is not
  /// scoped to a session (`wsTicketStore` in `cmd/claudecode-remoted/main.go`
  /// hands out a generic single-use ticket, and `wrapSessionAuth` accepts it
  /// for any `/sessions/{id}/stream` path). The parameter stays so call
  /// sites read the same either way and a future daemon that does scope
  /// tickets per-session would not need every caller rewritten.
  Future<String> streamTicket(String sessionId) async {
    final res = await _http.post(
      baseUri.replace(path: '/auth/ws-ticket'),
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

/// Builds the [ApiClient.authHeaders] (and [TusUploader.authHeaders])
/// callback: fetch a fresh challenge, sign it with the device key, attach it.
///
/// A challenge is single-use (architecture Section 5.4), so this cannot
/// cache a signature and reuse it — every call fetches its own challenge and
/// signs that one. This is a free function rather than a method on
/// [DeviceIdentity] because it needs an HTTP round trip to `/auth/challenge`,
/// which must go out **unsigned** — looping it back through this same
/// callback would try to sign a challenge to fetch a challenge.
Future<Map<String, String>> Function() deviceAuthHeaders({
  required Uri baseUri,
  required DeviceIdentity identity,
  http.Client? httpClient,
}) {
  final client = httpClient ?? http.Client();
  return () async {
    final deviceId = await identity.deviceId();
    if (deviceId == null) {
      throw const ApiException(401, 'device is not paired yet');
    }

    final res = await client.post(
      baseUri.replace(path: '/auth/challenge'),
      headers: const {'Content-Type': 'application/json'},
      body: jsonEncode({'device_id': deviceId}),
    );
    if (res.statusCode != 200) {
      throw ApiException(res.statusCode, 'could not obtain a challenge');
    }
    final body = jsonDecode(res.body);
    if (body is! Map || body['challenge'] is! String) {
      throw const ApiException(500, 'daemon returned no challenge');
    }

    final challenge = base64Decode(body['challenge'] as String);
    final signature = await identity.sign(challenge);
    return {
      'X-Device-Id': deviceId,
      'X-Device-Signature': base64Encode(signature),
    };
  };
}
