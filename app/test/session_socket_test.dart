import 'dart:convert';

import 'package:claudecode_remote/api/session_socket.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  group('PtyTextDecoder', () {
    // The bug this whole class exists to prevent. A multi-byte character split
    // across two chunks must survive; decoding each chunk on its own turns
    // both halves into U+FFFD and the terminal draws mojibake exactly where
    // TUI frame lines are.
    test('a character split across two chunks is not corrupted', () {
      final d = PtyTextDecoder();
      // U+2500 BOX DRAWINGS LIGHT HORIZONTAL: 0xE2 0x94 0x80.
      const boxChar = '─';
      final bytes = utf8.encode(boxChar);
      expect(bytes.length, 3);

      final first = d.add(bytes.sublist(0, 2));
      final second = d.add(bytes.sublist(2));

      expect(first, isEmpty, reason: 'an incomplete character must not be emitted yet');
      expect(second, boxChar);
      expect(first + second, boxChar);
    });

    test('a character split across three chunks survives', () {
      final d = PtyTextDecoder();
      const emoji = '\u{1F600}'; // 4 bytes
      final bytes = utf8.encode(emoji);

      final out = StringBuffer()
        ..write(d.add(bytes.sublist(0, 1)))
        ..write(d.add(bytes.sublist(1, 3)))
        ..write(d.add(bytes.sublist(3)));

      expect(out.toString(), emoji);
    });

    test('escape sequences pass through byte for byte', () {
      final d = PtyTextDecoder();
      // Alternate screen on, then a colour SGR. If these are mangled the
      // screen is garbled, so they get their own assertion.
      const seq = '\x1b[?1049h\x1b[38;5;196mred\x1b[0m';
      expect(d.add(utf8.encode(seq)), seq);
    });

    test('invalid bytes do not kill the stream', () {
      final d = PtyTextDecoder();
      // 0xFF is never valid UTF-8. Real PTY output does contain stray bytes;
      // throwing on them would end the session over one bad byte.
      final out = d.add([0x41, 0xFF, 0x42]);
      expect(out, contains('A'));
      expect(out, contains('B'));
    });

    test('base64 round trip preserves a non-UTF-8 byte sequence length', () {
      final raw = <int>[0x1b, 0x5b, 0x41, 0xFF, 0xFE, 0x00, 0x7F];
      final decoded = base64Decode(base64Encode(raw));
      expect(decoded, equals(raw));
    });
  });

  group('Backoff', () {
    test('grows exponentially then caps', () {
      final b = Backoff(
        initial: const Duration(milliseconds: 100),
        max: const Duration(milliseconds: 800),
      );

      expect(b.next().inMilliseconds, 100);
      expect(b.next().inMilliseconds, 200);
      expect(b.next().inMilliseconds, 400);
      expect(b.next().inMilliseconds, 800);
      // Capped, not doubling forever: an uncapped backoff leaves the user on a
      // disconnected banner long after the daemon came back.
      expect(b.next().inMilliseconds, 800);
      expect(b.next().inMilliseconds, 800);
    });

    test('reset returns to the initial delay', () {
      final b = Backoff(initial: const Duration(milliseconds: 100));
      b.next();
      b.next();
      b.reset();
      expect(b.next().inMilliseconds, 100);
    });
  });
}
