/// Flutter [ThemeData] assembled from the design tokens.
///
/// Widgets should read `Theme.of(context).extension<CcrThemeExt>()!` for the
/// semantic roles rather than reaching for Material's colour scheme, which has
/// no concept of "the terminal well" or "connecting, but not an error".
library;

import 'package:flutter/material.dart';

import 'tokens.dart';

/// Carries the full token set through the widget tree.
@immutable
class CcrThemeExt extends ThemeExtension<CcrThemeExt> {
  const CcrThemeExt({required this.colors, required this.density});

  final CcrColors colors;
  final CcrDensity density;

  @override
  CcrThemeExt copyWith({CcrColors? colors, CcrDensity? density}) =>
      CcrThemeExt(colors: colors ?? this.colors, density: density ?? this.density);

  /// Themes do not interpolate here. Every value is a discrete design decision
  /// with a measured contrast ratio; a half-way blend between the light and
  /// dark palettes is a colour nobody checked.
  @override
  CcrThemeExt lerp(ThemeExtension<CcrThemeExt>? other, double t) =>
      t < 0.5 ? this : (other as CcrThemeExt? ?? this);
}

/// Builds the app theme for a brightness and an input density.
ThemeData buildTheme({
  required Brightness brightness,
  required CcrDensity density,
}) {
  final c = brightness == Brightness.dark ? CcrColors.dark : CcrColors.light;

  return ThemeData(
    useMaterial3: true,
    brightness: brightness,
    scaffoldBackgroundColor: c.surfaceSunken,
    canvasColor: c.surfaceBase,
    dividerColor: c.borderSubtle,
    fontFamily: CcrFonts.ui,
    colorScheme: ColorScheme(
      brightness: brightness,
      primary: c.accentBase,
      onPrimary: c.accentOn,
      secondary: c.statusConnected,
      onSecondary: c.accentOn,
      surface: c.surfaceBase,
      onSurface: c.textPrimary,
      error: c.statusError,
      onError: c.accentOn,
    ),
    textTheme: TextTheme(
      displaySmall: CcrType.display.copyWith(color: c.textPrimary),
      titleLarge: CcrType.title.copyWith(color: c.textPrimary),
      titleMedium: CcrType.subtitle.copyWith(color: c.textPrimary),
      bodyLarge: CcrType.body.copyWith(color: c.textPrimary),
      bodyMedium: CcrType.bodySm.copyWith(color: c.textSecondary),
      labelLarge: CcrType.label.copyWith(color: c.textPrimary),
      labelMedium: CcrType.caption.copyWith(color: c.textSecondary),
      labelSmall: CcrType.micro.copyWith(color: c.textTertiary),
    ),
    // Elevation is allowed on overlay surfaces and nowhere else. Material's
    // defaults put shadows on cards and app bars; that is precisely the look
    // the design system rejects, so they are turned off explicitly rather
    // than left to chance.
    cardTheme: CardTheme(
      color: c.surfaceRaised,
      elevation: 0,
      margin: EdgeInsets.zero,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(CcrRadius.md),
      ),
    ),
    appBarTheme: AppBarTheme(
      backgroundColor: c.surfaceBase,
      foregroundColor: c.textPrimary,
      elevation: 0,
      scrolledUnderElevation: 0,
    ),
    dialogTheme: DialogTheme(
      backgroundColor: c.surfaceOverlay,
      elevation: 0,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(CcrRadius.md),
      ),
    ),
    extensions: [CcrThemeExt(colors: c, density: density)],
  );
}

/// Convenience accessor for the semantic colours.
extension CcrThemeContext on BuildContext {
  CcrColors get ccr => Theme.of(this).extension<CcrThemeExt>()!.colors;
  CcrDensity get ccrDensity => Theme.of(this).extension<CcrThemeExt>()!.density;
}
