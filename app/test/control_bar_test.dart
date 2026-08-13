import 'package:claudecode_remote/design/theme.dart';
import 'package:claudecode_remote/design/tokens.dart';
import 'package:claudecode_remote/widgets/control_bar.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  group('ctrlByte', () {
    // The formula this whole toggle exists to get right. Section 6.2's Ctrl
    // key is worthless if the byte it sends is wrong, and it fails silently
    // — the terminal just never receives the interrupt.
    test('is the letter\'s ASCII code minus 64', () {
      expect(ctrlByte('C'), String.fromCharCode(0x03)); // Ctrl+C, ETX
      expect(ctrlByte('D'), String.fromCharCode(0x04)); // Ctrl+D, EOT
      expect(ctrlByte('A'), String.fromCharCode(0x01));
      expect(ctrlByte('Z'), String.fromCharCode(0x1A));
      expect(ctrlByte('L'), String.fromCharCode(0x0C));
    });

    test('is case-insensitive', () {
      expect(ctrlByte('c'), ctrlByte('C'));
    });
  });

  Widget wrap(Widget child) => MaterialApp(
        theme: buildTheme(brightness: Brightness.dark, density: CcrDensity.touch),
        home: Scaffold(body: Align(alignment: Alignment.topLeft, child: child)),
      );

  group('ControlBar hit areas', () {
    testWidgets('every key clears the touch density\'s minimum hit area', (tester) async {
      final sent = <String>[];
      await tester.pumpWidget(wrap(ControlBar(onInput: sent.add)));

      // Row 2 is arrows by default; tapping Ctrl swaps it to C/D/Z/L, so both
      // sets are checked by toggling Ctrl mid-test rather than duplicating
      // the loop below.
      const row1 = ['Esc', 'Tab', 'Ctrl', '|', '/'];
      const row2Arrows = ['Up', 'Down', 'Left', 'Right'];

      for (final label in row1) {
        final size = tester.getSize(find.byKey(Key('ccr-key-$label')));
        // A control bar with 40px keys looks fine in a screenshot and is
        // unusable — this is the check that would have caught it.
        expect(size.height, greaterThanOrEqualTo(CcrDensity.touch.minHitArea),
            reason: '$label is shorter than the minimum touch target');
        expect(size.width, greaterThanOrEqualTo(CcrDensity.touch.minHitArea),
            reason: '$label is narrower than the minimum touch target');
      }

      for (final label in ['↑', '↓', '←', '→']) {
        final size = tester.getSize(find.byKey(Key('ccr-key-$label')));
        expect(size.height, greaterThanOrEqualTo(CcrDensity.touch.minHitArea));
        expect(size.width, greaterThanOrEqualTo(CcrDensity.touch.minHitArea));
      }
      // row2Arrows is asserted via its glyphs above; named here only so the
      // intent (arrows are what row 2 shows unlatched) reads at a glance.
      expect(row2Arrows.length, 4);

      await tester.tap(find.byKey(const Key('ccr-key-Ctrl')));
      await tester.pump();

      for (final label in ['C', 'D', 'Z', 'L']) {
        final size = tester.getSize(find.byKey(Key('ccr-key-$label')));
        expect(size.height, greaterThanOrEqualTo(CcrDensity.touch.minHitArea));
        expect(size.width, greaterThanOrEqualTo(CcrDensity.touch.minHitArea));
      }
    });
  });

  group('ControlBar byte emission', () {
    testWidgets('Esc sends the escape byte', (tester) async {
      final sent = <String>[];
      await tester.pumpWidget(wrap(ControlBar(onInput: sent.add)));
      await tester.tap(find.byKey(const Key('ccr-key-Esc')));
      expect(sent, ['\x1b']);
    });

    testWidgets('Tab sends a tab byte', (tester) async {
      final sent = <String>[];
      await tester.pumpWidget(wrap(ControlBar(onInput: sent.add)));
      await tester.tap(find.byKey(const Key('ccr-key-Tab')));
      expect(sent, ['\t']);
    });

    testWidgets('arrow keys send VT100 escape sequences', (tester) async {
      final sent = <String>[];
      await tester.pumpWidget(wrap(ControlBar(onInput: sent.add)));

      await tester.tap(find.byKey(const Key('ccr-key-↑')));
      await tester.tap(find.byKey(const Key('ccr-key-↓')));
      await tester.tap(find.byKey(const Key('ccr-key-←')));
      await tester.tap(find.byKey(const Key('ccr-key-→')));

      expect(sent, ['\x1b[A', '\x1b[B', '\x1b[D', '\x1b[C']);
    });

    testWidgets('| and / send themselves literally', (tester) async {
      final sent = <String>[];
      await tester.pumpWidget(wrap(ControlBar(onInput: sent.add)));
      await tester.tap(find.byKey(const Key('ccr-key-|')));
      await tester.tap(find.byKey(const Key('ccr-key-/')));
      expect(sent, ['|', '/']);
    });

    testWidgets('latching Ctrl then C sends Ctrl+C and releases the latch', (tester) async {
      final sent = <String>[];
      await tester.pumpWidget(wrap(ControlBar(onInput: sent.add)));

      await tester.tap(find.byKey(const Key('ccr-key-Ctrl')));
      await tester.pump();
      // Row 2 has swapped: the arrow keys are gone, replaced by C/D/Z/L.
      expect(find.byKey(const Key('ccr-key-↑')), findsNothing);
      expect(find.byKey(const Key('ccr-key-C')), findsOneWidget);

      await tester.tap(find.byKey(const Key('ccr-key-C')));
      await tester.pump();

      expect(sent, ['\x03']); // this is the whole point: Ctrl+C from a phone
      // Ctrl released itself after one use — row 2 is arrows again.
      expect(find.byKey(const Key('ccr-key-↑')), findsOneWidget);
      expect(find.byKey(const Key('ccr-key-C')), findsNothing);
    });

    testWidgets('tapping Ctrl again without a follow-up unlatches it', (tester) async {
      final sent = <String>[];
      await tester.pumpWidget(wrap(ControlBar(onInput: sent.add)));

      await tester.tap(find.byKey(const Key('ccr-key-Ctrl')));
      await tester.pump();
      await tester.tap(find.byKey(const Key('ccr-key-Ctrl')));
      await tester.pump();

      expect(sent, isEmpty, reason: 'Ctrl itself never sends a byte on its own');
      expect(find.byKey(const Key('ccr-key-↑')), findsOneWidget);
    });
  });

  group('ControlBar visibility', () {
    testWidgets('renders nothing when a hardware keyboard is present', (tester) async {
      await tester.pumpWidget(wrap(ControlBar(onInput: (_) {}, hasHardwareKeyboard: true)));
      expect(find.byKey(const Key('ccr-key-Esc')), findsNothing);
    });

    testWidgets('renders both rows when no hardware keyboard is present', (tester) async {
      await tester.pumpWidget(wrap(ControlBar(onInput: (_) {})));
      expect(find.byKey(const Key('ccr-key-Esc')), findsOneWidget);
      expect(find.byKey(const Key('ccr-key-↑')), findsOneWidget);
    });
  });
}
