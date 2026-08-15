/// The device's Ed25519 identity (architecture Section 5.4): the keypair
/// that proves this install to the daemon, and the device_id the daemon
/// hands back once pairing completes.
library;

import 'dart:convert';
import 'dart:typed_data';

import 'package:cryptography/cryptography.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';

const _kPrivateKeySeed = 'ccr_device_private_key_seed';
const _kDeviceId = 'ccr_device_id';

/// The minimal storage seam [DeviceIdentity] needs.
///
/// [FlutterSecureStorage] backs this in the running app: Android Keystore,
/// Windows DPAPI, or libsecret depending on platform. It is behind an
/// interface (rather than [DeviceIdentity] using the plugin directly) so
/// tests can fake persistence without going through a platform channel,
/// matching how [ApiClient] takes an injectable `http.Client`.
abstract class SecureStore {
  Future<String?> read(String key);
  Future<void> write(String key, String value);
  Future<void> delete(String key);
}

class _PlatformSecureStore implements SecureStore {
  const _PlatformSecureStore();
  static const _storage = FlutterSecureStorage();

  @override
  Future<String?> read(String key) => _storage.read(key: key);

  @override
  Future<void> write(String key, String value) => _storage.write(key: key, value: value);

  @override
  Future<void> delete(String key) => _storage.delete(key: key);
}

/// Generates, stores and uses the device's Ed25519 keypair.
///
/// The private key lives in [SecureStore], never in plain app storage
/// (SharedPreferences, a file under the app's documents directory, ...).
/// Plain storage is included in a phone or desktop backup; restoring that
/// backup onto a different device would clone a "working" device identity
/// that neither the daemon nor anyone else can distinguish from the
/// original — a lost phone's cloud backup would carry a live credential
/// with it. Keystore/DPAPI/libsecret entries are excluded from those
/// backups for exactly this reason.
///
/// The private key itself never leaves this class: no public method returns
/// it, and it is never logged.
class DeviceIdentity {
  DeviceIdentity({SecureStore? store, Ed25519? algorithm})
      : _store = store ?? const _PlatformSecureStore(),
        _algorithm = algorithm ?? Ed25519();

  final SecureStore _store;
  final Ed25519 _algorithm;

  Future<SimpleKeyPair> _keyPair() async {
    final existing = await _store.read(_kPrivateKeySeed);
    if (existing != null) {
      return _algorithm.newKeyPairFromSeed(base64Decode(existing));
    }
    // No key yet: generate one and persist its seed immediately, before
    // returning it. A keypair generated but never stored would sign this one
    // request with an identity the next launch can never reproduce, and the
    // daemon would see a device that authenticates once and then vanishes.
    final pair = await _algorithm.newKeyPair();
    final seed = await pair.extractPrivateKeyBytes();
    await _store.write(_kPrivateKeySeed, base64Encode(seed));
    return pair;
  }

  /// True once pairing has stored a device_id, i.e. the daemon knows this
  /// device's public key. A keypair can exist without this being true —
  /// generation happens before `/owner/pair/start`, pairing completes after.
  Future<bool> isPaired() async => await _store.read(_kDeviceId) != null;

  /// The device's public key, base64-standard-encoded, as `/owner/pair/start`
  /// expects it. Generates the keypair on first call if none exists yet.
  Future<String> publicKeyBase64() async {
    final pair = await _keyPair();
    final public = await pair.extractPublicKey();
    return base64Encode(public.bytes);
  }

  /// The daemon-assigned device_id, or null before pairing has completed.
  Future<String?> deviceId() => _store.read(_kDeviceId);

  Future<void> storeDeviceId(String id) => _store.write(_kDeviceId, id);

  /// Signs [challenge] — the raw bytes from `/auth/challenge`'s decoded
  /// `challenge` field, not its base64 form — with the device's private key.
  Future<Uint8List> sign(List<int> challenge) async {
    final pair = await _keyPair();
    final signature = await _algorithm.sign(challenge, keyPair: pair);
    return Uint8List.fromList(signature.bytes);
  }

  /// Wipes the key and device id, e.g. before re-pairing on this device or
  /// walking away from a daemon for good.
  ///
  /// This does not revoke the key server-side — that is `/owner/devices/revoke`,
  /// a step-up-gated owner action against the daemon this device may no
  /// longer even be able to reach. Forgetting locally just means the device
  /// stops being able to prove the identity it used to have; the daemon
  /// still has to be told separately if the goal is to lock that identity out.
  Future<void> forget() async {
    await _store.delete(_kPrivateKeySeed);
    await _store.delete(_kDeviceId);
  }
}
