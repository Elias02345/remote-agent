/// The operator-supplied daemon address, persisted across restarts.
///
/// There is no usable default here. `ApiClient`'s baseUri used to default to
/// the app's own loopback address, which works only when the daemon runs on
/// the same machine as the client — on a phone that means the phone itself,
/// never the actual server. The address has to come from the operator, once,
/// at pairing time.
library;

import 'package:shared_preferences/shared_preferences.dart';

const _kBaseUri = 'ccr_base_uri';

/// Thrown when a candidate server address fails validation.
class InvalidServerAddress implements Exception {
  const InvalidServerAddress(this.message);
  final String message;

  @override
  String toString() => message;
}

/// Reads and persists the daemon's base URI.
class ServerConfig {
  ServerConfig({SharedPreferences? prefs}) : _prefsOverride = prefs;

  SharedPreferences? _prefsOverride;

  Future<SharedPreferences> _prefs() async =>
      _prefsOverride ??= await SharedPreferences.getInstance();

  /// The persisted address, or null if the operator has not set one yet.
  Future<Uri?> baseUri() async {
    final raw = (await _prefs()).getString(_kBaseUri);
    return raw == null ? null : Uri.parse(raw);
  }

  /// Validates and persists [input]. Throws [InvalidServerAddress] and
  /// changes nothing on a bad value.
  Future<void> setBaseUri(String input) async {
    final uri = validate(input);
    await (await _prefs()).setString(_kBaseUri, uri.toString());
  }

  /// Parses and validates [input] without persisting it, so a form field can
  /// reject bad input before the operator moves on.
  ///
  /// Requires an absolute http/https URI with a host: a relative path has
  /// nowhere to send a request, a bare scheme like `http://` has no host to
  /// connect to, and any other scheme (`ftp://`, a pasted `ccr://` deep link,
  /// plain text) is not something `ApiClient` or a WebSocket upgrade can use.
  static Uri validate(String input) {
    final uri = Uri.tryParse(input.trim());
    if (uri == null || !uri.isAbsolute) {
      throw const InvalidServerAddress(
          'Enter the full address, e.g. https://remote.example');
    }
    if (uri.scheme != 'http' && uri.scheme != 'https') {
      throw const InvalidServerAddress('Address must start with http:// or https://');
    }
    if (uri.host.isEmpty) {
      throw const InvalidServerAddress('Address needs a host');
    }
    return uri;
  }
}
