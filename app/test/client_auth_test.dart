import 'dart:convert';

import 'package:claudecode_remote/api/client.dart';
import 'package:claudecode_remote/api/device_identity.dart';
import 'package:cryptography/cryptography.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

class _FakeSecureStore implements SecureStore {
  final Map<String, String> _values = {};

  @override
  Future<String?> read(String key) async => _values[key];

  @override
  Future<void> write(String key, String value) async => _values[key] = value;

  @override
  Future<void> delete(String key) async => _values.remove(key);
}

void main() {
  group('deviceAuthHeaders', () {
    test('refuses to sign anything before the device is paired', () async {
      final identity = DeviceIdentity(store: _FakeSecureStore());
      final authHeaders = deviceAuthHeaders(
        baseUri: Uri.parse('http://daemon.local'),
        identity: identity,
        httpClient: MockClient((req) async => throw StateError('must not call the daemon')),
      );

      await expectLater(authHeaders(), throwsA(isA<ApiException>()));
    });

    // The full round trip: fetch a fresh challenge, sign it, attach it — and
    // the signature that comes back must actually verify, not just be
    // present. A header with the right shape but a wrong or stale signature
    // would fail at the daemon anyway, but silently here.
    test('fetches a fresh challenge and signs it with the device key', () async {
      final identity = DeviceIdentity(store: _FakeSecureStore());
      await identity.storeDeviceId('dev-abc');
      final publicKeyB64 = await identity.publicKeyBase64();

      const challengeBytes = [10, 20, 30, 40, 50];
      http.Request? seenRequest;

      final authHeaders = deviceAuthHeaders(
        baseUri: Uri.parse('http://daemon.local'),
        identity: identity,
        httpClient: MockClient((req) async {
          seenRequest = req;
          return http.Response(
            jsonEncode({'challenge': base64Encode(challengeBytes)}),
            200,
            headers: {'content-type': 'application/json'},
          );
        }),
      );

      final headers = await authHeaders();

      expect(seenRequest!.url.path, '/auth/challenge');
      expect(jsonDecode(seenRequest!.body), {'device_id': 'dev-abc'});

      expect(headers['X-Device-Id'], 'dev-abc');
      final signature = base64Decode(headers['X-Device-Signature']!);
      final verified = await Ed25519().verify(
        challengeBytes,
        signature: Signature(
          signature,
          publicKey: SimplePublicKey(base64Decode(publicKeyB64), type: KeyPairType.ed25519),
        ),
      );
      expect(verified, isTrue);
    });

    // A single-use challenge means two requests must never sign the same
    // nonce — a cached header would defeat the entire point of issuing a
    // fresh challenge per call.
    test('signs a different challenge on every call', () async {
      final identity = DeviceIdentity(store: _FakeSecureStore());
      await identity.storeDeviceId('dev-abc');

      var counter = 0;
      final authHeaders = deviceAuthHeaders(
        baseUri: Uri.parse('http://daemon.local'),
        identity: identity,
        httpClient: MockClient((req) async {
          counter++;
          return http.Response(
            jsonEncode({
              'challenge': base64Encode([counter, counter, counter])
            }),
            200,
          );
        }),
      );

      final first = await authHeaders();
      final second = await authHeaders();

      expect(first['X-Device-Signature'], isNot(second['X-Device-Signature']));
    });
  });
}
