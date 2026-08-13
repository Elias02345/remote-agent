/// Design tokens for ClaudeCode Remote.
///
/// Transcribed from the Claude Design project "ClaudeCode Remote Design
/// System". This file is the single source of truth for colour, type, spacing
/// and density in the app — widgets must read from here rather than carrying
/// literal values, so a retune happens in one place.
///
/// Every colour below was chosen against a measured contrast ratio; the ratios
/// are kept in comments so a future edit can tell whether it is still inside
/// the budget. Do not "clean up" a value without re-measuring it.
library;

import 'package:flutter/widgets.dart';

/// Semantic colour roles, in both themes.
///
/// Named by role (`surfaceRaised`, `statusDisconnected`), never by appearance
/// (`grey800`, `orange`) — appearance names stop being true the moment the
/// theme flips.
class CcrColors {
  const CcrColors({
    required this.surfaceSunken,
    required this.surfaceBase,
    required this.surfaceRaised,
    required this.surfaceOverlay,
    required this.surfaceTerminal,
    required this.borderSubtle,
    required this.borderDefault,
    required this.borderStrong,
    required this.textPrimary,
    required this.textSecondary,
    required this.textTertiary,
    required this.textDisabled,
    required this.accentBase,
    required this.accentText,
    required this.accentOn,
    required this.accentWash,
    required this.statusConnected,
    required this.statusConnecting,
    required this.statusDisconnected,
    required this.statusEnded,
    required this.statusError,
    required this.terminal,
  });

  /// App shell behind everything: list background, empty states.
  final Color surfaceSunken;

  /// Default pane fill. Nav bars, sheets, the Server tab.
  final Color surfaceBase;

  /// Rows, cards, the control bar. Separated by fill, not shadow.
  final Color surfaceRaised;

  /// Dialogs, menus, toasts — the only place elevation is allowed.
  final Color surfaceOverlay;

  /// Slightly off base so the terminal reads as a well. The step is
  /// deliberately near-invisible (1.03): a hairline plus that much is enough
  /// to bound the rect without drawing a frame around it.
  final Color surfaceTerminal;

  /// Row rules and dividers. Present, not decorative.
  final Color borderSubtle;

  /// Input and button outlines, the terminal rect edge.
  final Color borderDefault;

  /// Hover and selected outlines; the control-bar key edge.
  final Color borderStrong;

  /// Session names, terminal-adjacent labels, dialog copy. 15.60:1 dark.
  final Color textPrimary;

  /// Paths, timestamps, byte counts. 7.17:1 — clears AA at 12px.
  final Color textSecondary;

  /// Section kickers and hints only. 3.60:1, below AA on purpose: this is
  /// never the sole carrier of meaning.
  final Color textTertiary;

  /// Disabled labels. Always paired with a non-colour cue.
  final Color textDisabled;

  /// Fills, focus rings, the active-tab bar, the new-terminal button.
  final Color accentBase;

  /// The accent stepped up for small text and links, where the base's 5.06:1
  /// gets thin.
  final Color accentText;

  /// Label colour on an accent fill. Near-black, not white — white on this
  /// terracotta is only 3.61:1.
  final Color accentOn;

  /// Selected-row and pressed tints. Alpha, so it survives both themes.
  final Color accentWash;

  /// Quiet on purpose: connected is the boring case.
  final Color statusConnected;

  /// Also carries reconnecting. Slate blue, so nothing transient reads as an
  /// error.
  final Color statusConnecting;

  /// The loudest value in the system and no hue at all. Disconnected is
  /// impossible to miss because it is the brightest thing on screen, not
  /// because it is red — the session is fine on the server, only the
  /// transport blinked.
  final Color statusDisconnected;

  /// Grey by design: an ended session is inert, not alarming.
  final Color statusEnded;

  /// Reserved for things the user must act on: hash mismatch, revoked device,
  /// failed backup. Never used for transport state.
  final Color statusError;

  /// The terminal's own palette.
  final CcrTerminalColors terminal;

  static const dark = CcrColors(
    surfaceSunken: Color(0xFF0E0E0F),
    surfaceBase: Color(0xFF151516),
    surfaceRaised: Color(0xFF1D1D1F),
    surfaceOverlay: Color(0xFF262628),
    surfaceTerminal: Color(0xFF121213),
    borderSubtle: Color(0xFF2A2A2C),
    borderDefault: Color(0xFF3A3A3D),
    borderStrong: Color(0xFF55555A),
    textPrimary: Color(0xFFEDEDEE),
    textSecondary: Color(0xFFA2A2A6),
    textTertiary: Color(0xFF6E6E73),
    textDisabled: Color(0xFF4A4A4E),
    accentBase: Color(0xFFC67139),
    accentText: Color(0xFFD98A4F),
    accentOn: Color(0xFF1A1206),
    accentWash: Color(0x24C67139), // 14%
    statusConnected: Color(0xFF8FA36A),
    statusConnecting: Color(0xFF7F9CC4),
    statusDisconnected: Color(0xFFF2F3F5),
    statusEnded: Color(0xFF6E6E73),
    statusError: Color(0xFFD4635F),
    terminal: CcrTerminalColors.dark,
  );

  static const light = CcrColors(
    surfaceSunken: Color(0xFFE9E9EB),
    surfaceBase: Color(0xFFF4F4F5),
    surfaceRaised: Color(0xFFFFFFFF),
    surfaceOverlay: Color(0xFFFFFFFF),
    surfaceTerminal: Color(0xFFFBFBFB),
    borderSubtle: Color(0xFFDCDCDE),
    borderDefault: Color(0xFFC6C7CA),
    borderStrong: Color(0xFF9A9CA1),
    textPrimary: Color(0xFF1A1B1C),
    textSecondary: Color(0xFF5C5F63),
    textTertiary: Color(0xFF85888D),
    textDisabled: Color(0xFFB9BABD),
    accentBase: Color(0xFFC67139), // unchanged across themes on purpose
    accentText: Color(0xFF96501F),
    accentOn: Color(0xFF1A1206),
    accentWash: Color(0x1FC67139), // 12%
    statusConnected: Color(0xFF4F6B30),
    statusConnecting: Color(0xFF33578C),
    statusDisconnected: Color(0xFF1A1B1C),
    statusEnded: Color(0xFF85888D),
    statusError: Color(0xFF9C2F28),
    terminal: CcrTerminalColors.light,
  );
}

/// The sixteen ANSI colours plus the terminal's own background, foreground,
/// cursor and selection.
///
/// This is a designed palette, not xterm's defaults. Three properties it holds
/// that a default palette does not:
///
///  * every colour clears a usable contrast ratio on [background] — default
///    xterm blue (#0000EE) scores 1.14 here, and `ls` directory listings are
///    made of it;
///  * green is pushed toward mint and yellow toward amber so the two separate
///    under deuteranopia, because terminal output uses red/green to mean
///    pass/fail constantly;
///  * none of the sixteen collides with the app's terracotta accent, so UI
///    chrome and program output stay visually distinct.
class CcrTerminalColors {
  const CcrTerminalColors({
    required this.background,
    required this.foreground,
    required this.cursor,
    required this.selection,
    required this.black,
    required this.red,
    required this.green,
    required this.yellow,
    required this.blue,
    required this.magenta,
    required this.cyan,
    required this.white,
    required this.brightBlack,
    required this.brightRed,
    required this.brightGreen,
    required this.brightYellow,
    required this.brightBlue,
    required this.brightMagenta,
    required this.brightCyan,
    required this.brightWhite,
  });

  /// Never pure black: pure black on OLED makes scrolling text smear, and it
  /// removes the one step of depth that bounds the rect.
  final Color background;

  /// Below pure white on purpose — 16.9:1 of unstyled log output for hours is
  /// fatiguing.
  final Color foreground;

  /// Neutral and brighter than the foreground so it stays findable in dense
  /// output. Deliberately not the accent: a terracotta cursor would be the one
  /// accent pixel inside the rect.
  final Color cursor;

  /// Cool slate, not accent-tinted. Chosen so foreground-over-selection still
  /// measures 7.23:1 — text stays readable while selected, which naive alpha
  /// overlays lose.
  final Color selection;

  final Color black;
  final Color red;
  final Color green;
  final Color yellow;
  final Color blue;
  final Color magenta;
  final Color cyan;
  final Color white;
  final Color brightBlack;
  final Color brightRed;
  final Color brightGreen;
  final Color brightYellow;
  final Color brightBlue;
  final Color brightMagenta;
  final Color brightCyan;
  final Color brightWhite;

  /// The sixteen in ANSI index order (0-15), for emulators that want a list.
  List<Color> get ansi => [
        black, red, green, yellow, blue, magenta, cyan, white,
        brightBlack, brightRed, brightGreen, brightYellow,
        brightBlue, brightMagenta, brightCyan, brightWhite,
      ];

  static const dark = CcrTerminalColors(
    background: Color(0xFF121213),
    foreground: Color(0xFFD5D6D9), // 12.88:1
    cursor: Color(0xFFE8E9EB), // 15.41:1
    selection: Color(0xFF384049), // fg over this: 7.23:1
    // Lifted well off pure black: programs use index 0 for dimmed text, and
    // #000 on a dark ground is invisible. 2.74:1 is intentional and only ever
    // used for de-emphasis.
    black: Color(0xFF565B64), // 2.74:1
    // Held deliberately darker than green so pass/fail keep a value
    // difference, not just a hue difference.
    red: Color(0xFFE0554F), // 4.98:1
    green: Color(0xFF6EC98A), // 9.26:1, hue ~150 for deuteranopia separation
    yellow: Color(0xFFCB9A41), // 7.34:1, amber not lemon
    blue: Color(0xFF62A8E8), // 7.39:1 — the classic xterm failure, fixed
    magenta: Color(0xFFCD7FD0), // 6.72:1, violet not pink (protanopia)
    cyan: Color(0xFF55BFC4), // 8.59:1, teal so symlinks separate from dirs
    white: Color(0xFFC9CACD), // 11.42:1
    brightBlack: Color(0xFF797F89), // 4.64:1 — the comment grey, real text
    brightRed: Color(0xFFFF8079), // 7.68:1 — the only colour allowed to shout
    brightGreen: Color(0xFF8FD9A4), // 11.26:1
    brightYellow: Color(0xFFECC06E), // 10.99:1
    brightBlue: Color(0xFF93C6F5), // 10.37:1
    brightMagenta: Color(0xFFE2A4E4), // 9.51:1
    brightCyan: Color(0xFF7FD8DC), // 11.37:1
    brightWhite: Color(0xFFF2F3F5), // 16.86:1
  );

  /// In the light theme, `white` and `brightWhite` are surface tones used by
  /// TUIs as fills and rules rather than as text. Their low text contrast is
  /// documented, not an oversight.
  static const light = CcrTerminalColors(
    background: Color(0xFFFBFBFB),
    foreground: Color(0xFF26282B), // 14.28:1
    cursor: Color(0xFF26282B),
    selection: Color(0xFFD8DEE6), // fg over this: 10.92:1
    black: Color(0xFF2A2D31), // 13.37:1
    red: Color(0xFFB3372F), // 5.79:1
    green: Color(0xFF2F7A45), // 5.09:1
    yellow: Color(0xFF8A6412), // 5.19:1
    blue: Color(0xFF1F5FA8), // 6.22:1
    magenta: Color(0xFF8F3D92), // 6.24:1
    cyan: Color(0xFF176F77), // 5.67:1
    white: Color(0xFF8C9096), // 3.10:1 — surface tone, not text
    brightBlack: Color(0xFF5F6469), // 5.78:1
    brightRed: Color(0xFF8E2620), // 8.29:1
    brightGreen: Color(0xFF1D5D33), // 7.61:1
    brightYellow: Color(0xFF674A09), // 7.93:1
    brightBlue: Color(0xFF143F75), // 10.16:1
    brightMagenta: Color(0xFF6C2A6F), // 9.23:1
    brightCyan: Color(0xFF0C5157), // 8.71:1
    brightWhite: Color(0xFFB8BBBF), // 1.86:1 — surface tone, not text
  );
}

/// Font families and their per-platform fallback chains.
class CcrFonts {
  /// Display face, used only for the pairing headline and empty states.
  static const display = 'Caprasimo';

  /// UI face for all chrome.
  static const ui = 'Figtree';

  /// Terminal and any monospace chrome (paths, hashes, session ids).
  static const mono = 'JetBrainsMono';

  /// Fallbacks after the bundled face, per platform. Flutter takes these as
  /// `fontFamilyFallback`; the list is ordered by how likely the face is to be
  /// present and how close its metrics are to JetBrains Mono.
  static const monoFallbackAndroid = ['Roboto Mono', 'Droid Sans Mono', 'monospace'];
  static const monoFallbackWindows = ['Cascadia Mono', 'Consolas', 'Lucida Console'];
  static const monoFallbackLinux = ['DejaVu Sans Mono', 'Liberation Mono', 'Noto Sans Mono'];
  static const monoFallbackWeb = ['ui-monospace', 'SFMono-Regular', 'Menlo', 'monospace'];
}

/// The type scale. Sizes are logical pixels; `height` is the CSS-style ratio.
class CcrType {
  const CcrType._();

  /// Pairing headline, empty state.
  static const display = TextStyle(
      fontFamily: CcrFonts.display, fontSize: 28, height: 1.10,
      fontWeight: FontWeight.w400, letterSpacing: -0.28);

  /// Screen titles, dialog titles.
  static const title = TextStyle(
      fontFamily: CcrFonts.ui, fontSize: 20, height: 1.25,
      fontWeight: FontWeight.w600, letterSpacing: -0.10);

  /// Session name in a row.
  static const subtitle = TextStyle(
      fontFamily: CcrFonts.ui, fontSize: 16, height: 1.35, fontWeight: FontWeight.w600);

  /// Dialog and banner copy.
  static const body = TextStyle(
      fontFamily: CcrFonts.ui, fontSize: 15, height: 1.50, fontWeight: FontWeight.w400);

  /// Secondary row text, hints.
  static const bodySm = TextStyle(
      fontFamily: CcrFonts.ui, fontSize: 13, height: 1.45, fontWeight: FontWeight.w400);

  /// Buttons, tabs, menu items.
  static const label = TextStyle(
      fontFamily: CcrFonts.ui, fontSize: 13, height: 1.20, fontWeight: FontWeight.w600);

  /// Transfer sizes, badge text.
  static const caption = TextStyle(
      fontFamily: CcrFonts.ui, fontSize: 11, height: 1.40,
      fontWeight: FontWeight.w500, letterSpacing: 0.11);

  /// Section kickers, all caps.
  static const micro = TextStyle(
      fontFamily: CcrFonts.ui, fontSize: 10, height: 1.00,
      fontWeight: FontWeight.w600, letterSpacing: 1.20);

  /// Paths, hashes and session ids that appear in chrome rather than in the
  /// terminal.
  static const monoUi = TextStyle(
      fontFamily: CcrFonts.mono, fontSize: 12.5, height: 1.50, fontWeight: FontWeight.w400);

  /// Terminal cells. User-scalable between [terminalMinSize] and
  /// [terminalMaxSize]; the line height stays 1.45 at every size so the cell
  /// grid stays integral and the rect does not develop half-row gaps.
  static const terminal = TextStyle(
      fontFamily: CcrFonts.mono, fontSize: 13.5, height: 1.45, fontWeight: FontWeight.w400);

  static const terminalMinSize = 11.0;
  static const terminalMaxSize = 17.0;
  static const terminalSizeStep = 0.5;
}

/// One 4px scale. `space05` is the only half-step and exists for hairline
/// insets.
class CcrSpace {
  const CcrSpace._();
  static const s05 = 2.0;
  static const s1 = 4.0;
  static const s2 = 8.0;
  static const s3 = 12.0;
  static const s4 = 16.0;
  static const s5 = 20.0;
  static const s6 = 24.0;
  static const s8 = 32.0;
  static const s10 = 40.0;
  static const s14 = 56.0;
}

/// Corner radii. Small controls are pills; panes and rows stay nearly square,
/// so the terminal rect does not sit inside a rounded frame.
class CcrRadius {
  const CcrRadius._();
  static const none = 0.0;
  static const xs = 3.0;
  static const sm = 5.0;
  static const md = 8.0;
  static const pill = 999.0;
}

/// Elevation is allowed in exactly one place: [CcrColors.surfaceOverlay]
/// surfaces — dialogs, menus, toasts. Everything else separates by fill.
/// A terminal app with drop shadows on every row looks cheap immediately.
class CcrElevation {
  const CcrElevation._();
  static const flat = <BoxShadow>[];
  static const overlay = <BoxShadow>[
    BoxShadow(color: Color(0x66000000), blurRadius: 32, offset: Offset(0, 12)),
  ];
}

/// Touch and pointer densities. The app ships both from one set of tokens
/// rather than branching per platform in widget code.
enum CcrDensity {
  /// Android, tablets, touch-capable web.
  touch(rowHeight: 64, minHitArea: 48, panePadding: 16),

  /// Windows, Linux, desktop web.
  pointer(rowHeight: 44, minHitArea: 28, panePadding: 20);

  const CcrDensity({
    required this.rowHeight,
    required this.minHitArea,
    required this.panePadding,
  });

  final double rowHeight;
  final double minHitArea;
  final double panePadding;
}
