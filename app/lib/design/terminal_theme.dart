/// Maps the design system's terminal palette onto xterm.dart.
library;

import 'package:xterm/xterm.dart';

import 'tokens.dart';

/// Builds an xterm [TerminalTheme] from [CcrTerminalColors].
///
/// Every value comes from the design tokens. Nothing here picks a colour — if
/// a hue looks wrong, it is wrong in `tokens.dart`, where the measured
/// contrast ratio sits next to it.
TerminalTheme buildTerminalTheme(CcrTerminalColors c) {
  return TerminalTheme(
    cursor: c.cursor,
    selection: c.selection,
    foreground: c.foreground,
    background: c.background,

    // xterm.dart wants a "bright foreground" for bold text. The palette's
    // brightWhite is exactly that role in the dark theme.
    black: c.black,
    red: c.red,
    green: c.green,
    yellow: c.yellow,
    blue: c.blue,
    magenta: c.magenta,
    cyan: c.cyan,
    white: c.white,
    brightBlack: c.brightBlack,
    brightRed: c.brightRed,
    brightGreen: c.brightGreen,
    brightYellow: c.brightYellow,
    brightBlue: c.brightBlue,
    brightMagenta: c.brightMagenta,
    brightCyan: c.brightCyan,
    brightWhite: c.brightWhite,

    // Search highlight reuses the accent wash rather than a new colour: one
    // more hue inside the rect would compete with program output.
    searchHitBackground: c.selection,
    searchHitBackgroundCurrent: c.brightYellow,
    searchHitForeground: c.foreground,
  );
}
