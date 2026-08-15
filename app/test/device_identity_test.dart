import 'dart:convert';

import 'package:claudecode_remote/api/device_identity.dart';
import 'package:cryptography/cryptography.dart';
import 'package:flutter_test/flutter_test.dart';

/// In-memory stand-in for the platform secure-storage channel (Android
/// Keystore / Windows DPAPI / libsecret), so these tests exercise
/// [DeviceIdentity]'s own logic rather than a platform channel that does not
/// exist in the test environment.
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
  group('DeviceIdentity', () {
    test('is unpaired until a device_id is stored', () async {
      final identity = DeviceIdentity(store: _FakeSecureStore());
      expect(await identity.isPaired(), isFalse);

      await identity.storeDeviceId('dev-123');
      expect(await identity.isPaired(), isTrue);
      expect(await identity.deviceId(), 'dev-123');
    });

    // The daemon has to recognise the same key on every request; a keypair
    // that regenerated per call would make every request after the first
    // look like a different, unpaired device.
    test('the key is generated once and is stable across calls', () async {
      final identity = DeviceIdentity(store: _FakeSecureStore());

      final first = await identity.publicKeyBase64();
      final second = await identity.publicKeyBase64();

      expect(second, first);
    });

    test('two identities backed by the same store see the same key', () async {
      final store = _FakeSecureStore();
      final a = DeviceIdentity(store: store);
      final b = DeviceIdentity(store: store);

      expect(await b.publicKeyBase64(), await a.publicKeyBase64());
    });

    test('forget removes the device_id and the key, and a fresh key follows', () async {
      final store = _FakeSecureStore();
      final identity = DeviceIdentity(store: store);

      final keyBefore = await identity.publicKeyBase64();
      await identity.storeDeviceId('dev-123');

      await identity.forget();

      expect(await identity.isPaired(), isFalse);
      expect(await identity.deviceId(), isNull);

      // The whole point of forget(): nothing recoverable is left behind, so
      // the next key is a different one, not the old seed reappearing.
      final keyAfter = await identity.publicKeyBase64();
      expect(keyAfter, isNot(keyBefore));
    });

    test('sign produces a signature that verifies against the reported public key', () async {
      final identity = DeviceIdentity(store: _FakeSecureStore());
      final publicKeyB64 = await identity.publicKeyBase64();
      const challenge = [1, 2, 3, 4, 5, 42, 255, 0];

      final signature = await identity.sign(challenge);

      final verified = await Ed25519().verify(
        challenge,
        signature: Signature(
          signature,
          publicKey: SimplePublicKey(base64Decode(publicKeyB64), type: KeyPairType.ed25519),
        ),
      );
      expect(verified, isTrue);
    });

    // A signature over the wrong bytes must not verify — this is what makes
    // the challenge single-use meaningful; a signature that verified against
    // anything would let a captured one be replayed against a new challenge.
    test('a signature does not verify against a different message', () async {
      final identity = DeviceIdentity(store: _FakeSecureStore());
      final publicKeyB64 = await identity.publicKeyBase64();
      final signature = await identity.sign([1, 2, 3]);

      final verified = await Ed25519().verify(
        [9, 9, 9],
        signature: Signature(
          signature,
          publicKey: SimplePublicKey(base64Decode(publicKeyB64), type: KeyPairType.ed25519),
        ),
      );
      expect(verified, isFalse);
    });
  });
}
