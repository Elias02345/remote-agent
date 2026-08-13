/// The mobile control bar shown above the on-screen keyboard during a
/// terminal session (architecture Section 6.2).
library;

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../design/theme.dart';
import '../design/tokens.dart';

/// Width above which the two rows collapse into one. The bar exists to make
/// up for a cramped phone keyboard; a surface this wide has room to spare,
/// and a tablet/foldable in landscape does not need two stacked rows.
const double controlBarWideBreakpoint = 900;

/// Local layout constant. `tokens.dart` has no entry for "how wide is a
/// control-bar key" — it is not a design-system value shared by anything
/// else, so it lives here next to the widget that uses it, the same way
/// `ConnectionBanner` keeps its own `_transientHeight`.
const double _keyWidth = 52.0;

/// Converts an uppercase letter to the byte its Ctrl combination sends.
///
/// ASCII control codes are the letter's code point minus 64 (`C` is 67, so
/// Ctrl+C is 3 = ETX). This is deliberately a free function, not inlined
/// into a tap handler: getting the formula wrong silently breaks every
/// interrupt sent through the bar, so it gets its own direct test instead of
/// only being exercised through a widget pump.
String ctrlByte(String letter) {
  assert(letter.length == 1, 'ctrlByte takes a single letter, got "$letter"');
  final code = letter.toUpperCase().codeUnitAt(0) - 64;
  return String.fromCharCode(code);
}

/// Two rows of keys above the keyboard: Esc, Tab, a latching Ctrl, `|`, `/`,
/// paste, and either arrow keys or (while Ctrl is latched) the Ctrl+letter
/// combinations a touchscreen otherwise cannot reach.
class ControlBar extends StatefulWidget {
  const ControlBar({
    super.key,
    required this.onInput,
    this.hasHardwareKeyboard = false,
  });

  /// Sends raw bytes to the session — the same sink `Terminal.onOutput` is
  /// wired to in `TerminalScreen`.
  final ValueChanged<String> onInput;

  /// Whether a physical keyboard is attached. When true the bar renders
  /// nothing, per Section 6.2: a hardware keyboard already has Ctrl and
  /// arrow keys.
  ///
  /// ponytail: real detection needs a platform channel (Android's
  /// `Configuration.keyboard`, roughly), and this repo has no `android/`
  /// directory yet — see `app/README.md`. The host screen passes this once
  /// that glue exists; `false` (show the bar) is the safe default for a
  /// phone, which is the only place this widget is used at all.
  final bool hasHardwareKeyboard;

  @override
  State<ControlBar> createState() => _ControlBarState();
}

class _ControlBarState extends State<ControlBar> {
  bool _ctrlLatched = false;

  void _send(String data) {
    widget.onInput(data);
    // Latched Ctrl applies to exactly the next combination and then
    // releases, like every mobile Ctrl toggle (Termux, Blink) — a Ctrl key
    // that stays latched until tapped again is one extra step for the
    // common case of sending a single Ctrl+C.
    if (_ctrlLatched) setState(() => _ctrlLatched = false);
  }

  void _toggleCtrl() => setState(() => _ctrlLatched = !_ctrlLatched);

  Future<void> _paste() async {
    final data = await Clipboard.getData(Clipboard.kTextPlain);
    final text = data?.text;
    if (text != null && text.isNotEmpty) widget.onInput(text);
  }

  @override
  Widget build(BuildContext context) {
    if (widget.hasHardwareKeyboard) return const SizedBox.shrink();

    final c = context.ccr;
    final wide = MediaQuery.sizeOf(context).width >= controlBarWideBreakpoint;
    final row1 = _row1(c);
    final row2 = _row2(c);

    return ColoredBox(
      color: c.surfaceBase,
      child: Padding(
        padding: const EdgeInsets.all(CcrSpace.s1),
        child: wide
            ? Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  ...row1,
                  const SizedBox(width: CcrSpace.s3),
                  ...row2,
                ],
              )
            : Column(
                mainAxisSize: MainAxisSize.min,
                children: [
                  Row(mainAxisSize: MainAxisSize.min, children: row1),
                  const SizedBox(height: CcrSpace.s1),
                  Row(mainAxisSize: MainAxisSize.min, children: row2),
                ],
              ),
      ),
    );
  }

  List<Widget> _row1(CcrColors c) => [
        _key(c, 'Esc', () => _send('\x1b')),
        const SizedBox(width: CcrSpace.s1),
        _key(c, 'Tab', () => _send('\t')),
        const SizedBox(width: CcrSpace.s1),
        _key(c, 'Ctrl', _toggleCtrl, active: _ctrlLatched),
        const SizedBox(width: CcrSpace.s1),
        _key(c, '|', () => _send('|')),
        const SizedBox(width: CcrSpace.s1),
        _key(c, '/', () => _send('/')),
        const SizedBox(width: CcrSpace.s1),
        _key(c, '⎘', _paste, semanticLabel: 'Paste'),
      ];

  /// Row 2 is arrow keys normally. While Ctrl is latched it swaps to the
  /// combinations a phone otherwise cannot send: arrow keys are VT100
  /// escape sequences, not single bytes, so "Ctrl+Up" has no byte to send in
  /// the first place. C/D/Z/L cover interrupt, EOF, suspend and clear-screen
  /// — the ones that actually come up controlling a running agent, and
  /// specifically the reason this toggle exists: Ctrl+C is otherwise
  /// unreachable on a touch keyboard.
  List<Widget> _row2(CcrColors c) => _ctrlLatched
      ? [
          _key(c, 'C', () => _send(ctrlByte('C')), semanticLabel: 'Ctrl+C'),
          const SizedBox(width: CcrSpace.s1),
          _key(c, 'D', () => _send(ctrlByte('D')), semanticLabel: 'Ctrl+D'),
          const SizedBox(width: CcrSpace.s1),
          _key(c, 'Z', () => _send(ctrlByte('Z')), semanticLabel: 'Ctrl+Z'),
          const SizedBox(width: CcrSpace.s1),
          _key(c, 'L', () => _send(ctrlByte('L')), semanticLabel: 'Ctrl+L'),
        ]
      : [
          _key(c, '↑', () => _send('\x1b[A'), semanticLabel: 'Up'),
          const SizedBox(width: CcrSpace.s1),
          _key(c, '↓', () => _send('\x1b[B'), semanticLabel: 'Down'),
          const SizedBox(width: CcrSpace.s1),
          _key(c, '←', () => _send('\x1b[D'), semanticLabel: 'Left'),
          const SizedBox(width: CcrSpace.s1),
          _key(c, '→', () => _send('\x1b[C'), semanticLabel: 'Right'),
        ];

  Widget _key(
    CcrColors c,
    String label,
    VoidCallback onTap, {
    bool active = false,
    String? semanticLabel,
  }) {
    return Semantics(
      button: true,
      label: semanticLabel ?? label,
      child: SizedBox(
        key: Key('ccr-key-$label'),
        width: _keyWidth,
        // The touch density's minimum hit area, not a literal 44 — a key
        // that looks right in a screenshot but sits under 48dp of tappable
        // area is unusable, which is exactly the mistake this token exists
        // to prevent.
        height: CcrDensity.touch.minHitArea,
        child: Material(
          color: active ? c.accentWash : c.surfaceOverlay,
          borderRadius: BorderRadius.circular(CcrRadius.xs),
          child: InkWell(
            onTap: onTap,
            borderRadius: BorderRadius.circular(CcrRadius.xs),
            child: DecoratedBox(
              decoration: BoxDecoration(
                border: Border.all(color: active ? c.accentBase : c.borderDefault),
                borderRadius: BorderRadius.circular(CcrRadius.xs),
              ),
              child: Center(
                child: Text(
                  label,
                  style: CcrType.label.copyWith(
                    color: active ? c.accentText : c.textPrimary,
                  ),
                ),
              ),
            ),
          ),
        ),
      ),
    );
  }
}
