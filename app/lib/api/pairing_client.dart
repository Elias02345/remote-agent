/// REST client for the daemon's `/owner/pair/*` routes.
///
/// Deliberately separate from [ApiClient]: pairing is unauthenticated by
/// necessity (a device has nothing to sign a challenge with until it is
/// paired — main.go's comment on `RegisterPairing` says this outright), so
/// this client must never attach the `X-Device-Id` / `X-Device-Signature`
/// headers `ApiClient.authHeaders` builds.
library;

import 'dart:convert';

import 'package:http/http.dart' as http;

import 'client.dart';

/// Result of `/owner/pair/start`.
class PairingStart {
  const PairingStart({required this.pairingId, required this.outstanding});

  final String pairingId;

  /// Factor names still needed, e.g. `password`, `totp`, `passkey`.
  final List<String> outstanding;
}

/// Result of one poll. Either every factor is done ([ready]) or
/// [outstanding] lists what the browser flow still has to satisfy.
///
/// Carries no device id: obtaining that is [PairingClient.complete]'s job,
/// and a poll deliberately cannot consume the attempt.
class PairingPoll {
  const PairingPoll._({required this.ready, this.outstanding = const []});

  factory PairingPoll.ready() => const PairingPoll._(ready: true);

  factory PairingPoll.outstanding(List<String> outstanding) =>
      PairingPoll._(ready: false, outstanding: outstanding);

  final bool ready;
  final List<String> outstanding;
}

class PairingClient {
  PairingClient({required this.baseUri, http.Client? httpClient})
      : _http = httpClient ?? http.Client();

  final Uri baseUri;
  final http.Client _http;

  /// Registers a pairing attempt for this device's public key and returns
  /// the pairing_id the browser flow and [poll] both key off.
  Future<PairingStart> start({
    required String devicePubkeyBase64,
    required String deviceName,
    required String platform,
  }) async {
    final res = await _http.post(
      baseUri.replace(path: '/owner/pair/start'),
      headers: const {'Content-Type': 'application/json'},
      body: jsonEncode({
        'device_pubkey': devicePubkeyBase64,
        'device_name': deviceName,
        'platform': platform,
      }),
    );
    if (res.statusCode != 200 && res.statusCode != 201) {
      throw ApiException(res.statusCode, _message(res));
    }
    final body = _decodeMap(res);
    final pairingId = body['pairing_id'];
    if (pairingId is! String) {
      throw const ApiException(500, 'daemon returned no pairing_id');
    }
    return PairingStart(pairingId: pairingId, outstanding: _factors(body['outstanding']));
  }

  /// Polls how far the browser flow has got.
  ///
  /// Deliberately a read, not an attempt to finish. `/owner/pair/complete`
  /// would also answer 409 with the outstanding factors and could be polled
  /// instead — but it is the call that consumes the attempt and hands back
  /// the device id, and polling a consuming endpoint every two seconds means
  /// the completing call is whichever poll happened to land first. If its
  /// response were then lost on the wire, the attempt would already be spent
  /// and the device id gone with it, with no way to ask again.
  ///
  /// Reading the status separately keeps exactly one call that consumes the
  /// attempt, made once, at a moment the caller chose.
  Future<PairingPoll> poll(String pairingId) async {
    final res = await _http.get(
      baseUri.replace(path: '/owner/pair/status', queryParameters: {'pairing_id': pairingId}),
    );
    if (res.statusCode != 200) {
      throw ApiException(res.statusCode, _message(res));
    }
    final body = _decodeMap(res);
    if (body['completed'] == true) {
      return PairingPoll.ready();
    }
    return PairingPoll.outstanding(_factors(body['outstanding']));
  }

  /// Finishes pairing and returns the device id. Call once, after [poll]
  /// reports every factor satisfied — this consumes the attempt, so a second
  /// call answers 404 rather than repeating the answer.
  Future<String> complete(String pairingId) async {
    final res = await _http.post(
      baseUri.replace(path: '/owner/pair/complete'),
      headers: const {'Content-Type': 'application/json'},
      body: jsonEncode({'pairing_id': pairingId}),
    );
    if (res.statusCode != 200) {
      throw ApiException(res.statusCode, _message(res));
    }
    final deviceId = _decodeMap(res)['device_id'];
    if (deviceId is! String) {
      throw const ApiException(500, 'daemon reported success with no device_id');
    }
    return deviceId;
  }

  void close() => _http.close();

  static Map<String, dynamic> _decodeMap(http.Response res) {
    final decoded = jsonDecode(res.body);
    return decoded is Map<String, dynamic> ? decoded : const {};
  }

  static List<String> _factors(Object? raw) =>
      raw is List ? raw.whereType<String>().toList(growable: false) : const [];

  static String _message(http.Response res) {
    try {
      final body = jsonDecode(res.body);
      if (body is Map && body['error'] is String) return body['error'] as String;
    } on FormatException {
      // Non-JSON error body; fall through to the reason phrase.
    }
    return res.reasonPhrase ?? 'pairing request failed';
  }
}
