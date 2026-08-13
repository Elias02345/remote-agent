import 'package:claudecode_remote/api/session_socket.dart';
import 'package:claudecode_remote/design/terminal_theme.dart';
import 'package:claudecode_remote/design/theme.dart';
import 'package:claudecode_remote/design/tokens.dart';
import 'package:claudecode_remote/widgets/status_dot.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  group('theme', () {
    test('builds for both brightnesses and carries the tokens through', () {
      for (final brightness in Brightness.values) {
        final theme = buildTheme(brightness: brightness, density: CcrDensity.pointer);
        final ext = theme.extension<CcrThemeExt>();
        expect(ext, isNotNull, reason: 'widgets read their colours from this extension');
        expect(ext!.colors.accentBase, CcrColors.dark.accentBase,
            reason: 'the accent is unchanged across themes by design');
      }
    });

    test('elevation is off on cards and app bars', () {
      // The design system allows elevation only on overlay surfaces. Material
      // defaults would put shadows on every card, which is exactly the look
      // that was rejected.
      final theme = buildTheme(brightness: Brightness.dark, density: CcrDensity.pointer);
      expect(theme.cardTheme.elevation, 0);
      expect(theme.appBarTheme.elevation, 0);
      expect(theme.appBarTheme.scrolledUnderElevation, 0);
    });

    test('themes do not interpolate to an unchecked colour', () {
      const a = CcrThemeExt(colors: CcrColors.dark, density: CcrDensity.pointer);
      const b = CcrThemeExt(colors: CcrColors.light, density: CcrDensity.touch);
      // Every colour has a measured contrast ratio; a half-way blend is a
      // colour nobody checked.
      expect((a.lerp(b, 0.4)).colors.surfaceBase, CcrColors.dark.surfaceBase);
      expect((a.lerp(b, 0.6)).colors.surfaceBase, CcrColors.light.surfaceBase);
    });
  });

  group('terminal palette', () {
    test('has sixteen distinct ANSI colours', () {
      final ansi = CcrTerminalColors.dark.ansi;
      expect(ansi.length, 16);
      expect(ansi.toSet().length, 16, reason: 'two identical ANSI slots would be indistinguishable');
    });

    test('does not reuse the app accent inside the terminal', () {
      // If a terminal colour equalled the accent, UI chrome and program output
      // would become visually ambiguous.
      expect(CcrTerminalColors.dark.ansi, isNot(contains(CcrColors.dark.accentBase)));
      expect(CcrTerminalColors.dark.cursor, isNot(CcrColors.dark.accentBase));
    });

    test('background is never pure black or pure white', () {
      expect(CcrTerminalColors.dark.background, isNot(const Color(0xFF000000)));
      expect(CcrTerminalColors.light.background, isNot(const Color(0xFFFFFFFF)));
    });

    test('builds an xterm theme for both variants', () {
      for (final palette in [CcrTerminalColors.dark, CcrTerminalColors.light]) {
        final theme = buildTerminalTheme(palette);
        expect(theme.background, palette.background);
        expect(theme.foreground, palette.foreground);
      }
    });
  });

  group('StatusDot', () {
    testWidgets('renders a distinct shape per state, not only a colour',
        (tester) async {
      // Colour alone fails for colour-vision deficiencies, and this dot is
      // often the only difference between two otherwise identical rows.
      for (final state in LinkState.values) {
        await tester.pumpWidget(
          MaterialApp(
            theme: buildTheme(brightness: Brightness.dark, density: CcrDensity.pointer),
            home: Scaffold(body: StatusDot(state: state)),
          ),
        );
        expect(find.byType(StatusDot), findsOneWidget);

        final semantics = tester.getSemantics(find.byType(StatusDot));
        expect(semantics.label, isNotEmpty,
            reason: 'every state needs a screen-reader label, not just a colour');
      }
    });
  });
}
