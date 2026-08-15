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

/// Result of one poll. Either [completed] is true and [deviceId] is set, or
/// it is false and [outstanding] lists what is still missing.
class PairingPoll {
  const PairingPoll._({required this.completed, this.deviceId, this.outstanding = const []});

  factory PairingPoll.completed(String deviceId) =>
      PairingPoll._(completed: true, deviceId: deviceId);

  factory PairingPoll.outstanding(List<String> outstanding) =>
      PairingPoll._(completed: false, outstanding: outstanding);

  final bool completed;
  final String? deviceId;
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

  /// Polls whether pairing has finished.
  ///
  /// The daemon has no separate status route — attempting
  /// `/owner/pair/complete` before every factor is satisfied *is* the status
  /// check: it answers 409 with whatever factors remain instead of pairing
  /// the device (`daemon/internal/identity/owner.go`, `handlePairComplete`).
  /// Calling it repeatedly is therefore both how completion is detected and
  /// how it is actually finished — there is no separate "now complete it"
  /// step once the last factor lands.
  Future<PairingPoll> poll(String pairingId) async {
    final res = await _http.post(
      baseUri.replace(path: '/owner/pair/complete'),
      headers: const {'Content-Type': 'application/json'},
      body: jsonEncode({'pairing_id': pairingId}),
    );

    if (res.statusCode == 200) {
      final body = _decodeMap(res);
      final deviceId = body['device_id'];
      if (deviceId is! String) {
        throw const ApiException(500, 'daemon reported success with no device_id');
      }
      return PairingPoll.completed(deviceId);
    }
    if (res.statusCode == 409) {
      final body = _decodeMap(res);
      // A 409 also covers "pairing already completed elsewhere" (no
      // 'outstanding' key); that is not this poll's problem to solve, so it
      // surfaces as an empty list rather than a thrown error — the caller
      // keeps waiting and a fresh /owner/pair/start is how a user actually
      // recovers from it.
      return PairingPoll.outstanding(_factors(body['outstanding']));
    }
    throw ApiException(res.statusCode, _message(res));
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
