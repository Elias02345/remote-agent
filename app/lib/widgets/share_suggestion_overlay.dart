/// The "a file arrived" suggestion shown after a share-sheet upload
/// completes — architecture Section 8.3.
library;

import 'package:flutter/material.dart';

import '../design/theme.dart';
import '../design/tokens.dart';
import '../services/share_upload_service.dart';

/// Offers the shared file's suggested terminal line without ever sending it
/// itself.
///
/// Section 8.3 is explicit that this is a suggestion, not an injection: the
/// agent may be mid-keystroke in the session it's attached to, and writing
/// into that input stream on its behalf would corrupt whatever it was
/// typing. [onInsert] fires only from the button's `onPressed` — nothing in
/// `build` calls it, and nothing here reaches a socket or a terminal
/// directly.
class ShareSuggestionOverlay extends StatelessWidget {
  const ShareSuggestionOverlay({
    super.key,
    required this.suggestion,
    required this.onInsert,
    required this.onDismiss,
  });

  final SharedFileSuggestion suggestion;

  /// Called with [SharedFileSuggestion.insertLine] on an explicit tap only.
  /// The caller wires this to the same input sink as everything else the
  /// user types (e.g. `SessionSocket.sendInput`).
  final ValueChanged<String> onInsert;

  final VoidCallback onDismiss;

  @override
  Widget build(BuildContext context) {
    final c = context.ccr;

    return Material(
      color: c.surfaceOverlay,
      borderRadius: BorderRadius.circular(CcrRadius.md),
      child: Padding(
        padding: const EdgeInsets.all(CcrSpace.s4),
        child: Row(
          children: [
            Expanded(
              child: Column(
                mainAxisSize: MainAxisSize.min,
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text('File uploaded', style: CcrType.label.copyWith(color: c.textPrimary)),
                  const SizedBox(height: CcrSpace.s1),
                  Text(
                    suggestion.relativePath,
                    style: CcrType.monoUi.copyWith(color: c.textSecondary),
                  ),
                ],
              ),
            ),
            TextButton(
              key: const Key('ccr-suggestion-dismiss'),
              onPressed: onDismiss,
              child: Text('Dismiss', style: CcrType.label.copyWith(color: c.textSecondary)),
            ),
            const SizedBox(width: CcrSpace.s2),
            FilledButton(
              key: const Key('ccr-suggestion-insert'),
              onPressed: () => onInsert(suggestion.insertLine),
              style: FilledButton.styleFrom(
                backgroundColor: c.accentBase,
                foregroundColor: c.accentOn,
              ),
              child: const Text('Insert into terminal'),
            ),
          ],
        ),
      ),
    );
  }
}
