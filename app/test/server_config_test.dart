import 'package:claudecode_remote/api/server_config.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  group('ServerConfig.validate', () {
    test('accepts a full http(s) address', () {
      expect(ServerConfig.validate('https://remote.example').toString(),
          'https://remote.example');
      expect(ServerConfig.validate('http://192.168.1.5:8080').toString(),
          'http://192.168.1.5:8080');
    });

    // The client's own loopback address used to be the default here; on a
    // phone that pointed the app at itself. A bare hostname with no scheme
    // is the same failure mode by omission — there is nothing to tell it
    // apart from a path, so it must be rejected rather than guessed at.
    test('rejects a relative URI', () {
      expect(() => ServerConfig.validate('remote.example'),
          throwsA(isA<InvalidServerAddress>()));
      expect(() => ServerConfig.validate('remote.example:8080'),
          throwsA(isA<InvalidServerAddress>()));
    });

    test('rejects a URI with no host', () {
      expect(() => ServerConfig.validate('http://'),
          throwsA(isA<InvalidServerAddress>()));
      expect(() => ServerConfig.validate('https:///pair'),
          throwsA(isA<InvalidServerAddress>()));
    });

    test('rejects a non-http(s) scheme', () {
      expect(() => ServerConfig.validate('ftp://remote.example'),
          throwsA(isA<InvalidServerAddress>()));
      expect(() => ServerConfig.validate('ccr://remote.example'),
          throwsA(isA<InvalidServerAddress>()));
    });
  });

  group('ServerConfig persistence', () {
    test('baseUri is null until something is set', () async {
      SharedPreferences.setMockInitialValues({});
      final config = ServerConfig(prefs: await SharedPreferences.getInstance());
      expect(await config.baseUri(), isNull);
    });

    test('a validated address round-trips through setBaseUri/baseUri', () async {
      SharedPreferences.setMockInitialValues({});
      final config = ServerConfig(prefs: await SharedPreferences.getInstance());

      await config.setBaseUri('https://remote.example');

      expect((await config.baseUri()).toString(), 'https://remote.example');
    });

    test('a rejected address is never persisted', () async {
      SharedPreferences.setMockInitialValues({});
      final config = ServerConfig(prefs: await SharedPreferences.getInstance());

      await expectLater(
        config.setBaseUri('not a url at all'),
        throwsA(isA<InvalidServerAddress>()),
      );
      expect(await config.baseUri(), isNull);
    });
  });
}
