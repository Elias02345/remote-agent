/// The pairing screen: turns an unpaired install into a device the daemon
/// trusts (architecture Section 10.2's three-factor chain, from the app's
/// side of it).
library;

import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:url_launcher/url_launcher.dart';

import '../api/device_identity.dart';
import '../api/pairing_client.dart';
import '../api/server_config.dart';
import '../design/theme.dart';
import '../design/tokens.dart';

/// Walks the operator through pairing this install with a daemon.
///
/// The three factors — password, TOTP, passkey — are deliberately NOT
/// collected here. WebAuthn is a browser API this app cannot call, and more
/// importantly, keeping the owner's password out of this app means a
/// compromised build of this app never sees it. All three happen on the
/// `/pair` page the daemon itself serves, opened in the system browser; this
/// screen only starts the attempt and polls until it finishes.
class PairingScreen extends StatefulWidget {
  const PairingScreen({
    super.key,
    required this.identity,
    required this.serverConfig,
    required this.onPaired,
  });

  final DeviceIdentity identity;
  final ServerConfig serverConfig;

  /// Called once `/owner/pair/complete` has actually paired the device and
  /// the device_id is stored. The caller (main.dart) owns what happens next.
  final VoidCallback onPaired;

  @override
  State<PairingScreen> createState() => _PairingScreenState();
}

enum _Phase { loading, needAddress, starting, waiting, paired, error }

class _PairingScreenState extends State<PairingScreen> {
  _Phase _phase = _Phase.loading;
  Uri? _baseUri;
  String? _error;
  PairingClient? _client;

  final _addressController = TextEditingController();

  String? _pairingId;
  List<String> _outstanding = const [];
  Timer? _pollTimer;

  /// Bumped on dispose (and on every restart of the flow) so a poll response
  /// that arrives late cannot call setState on a gone widget — the same
  /// generation-counter guard session_socket.dart uses for the same reason:
  /// an await can outlive the thing that started it.
  int _generation = 0;

  @override
  void initState() {
    super.initState();
    _loadAddress();
  }

  @override
  void dispose() {
    _generation++;
    _pollTimer?.cancel();
    _client?.close();
    _addressController.dispose();
    super.dispose();
  }

  Future<void> _loadAddress() async {
    final generation = _generation;
    final uri = await widget.serverConfig.baseUri();
    if (generation != _generation || !mounted) return;

    if (uri == null) {
      setState(() => _phase = _Phase.needAddress);
      return;
    }
    _baseUri = uri;
    await _startPairing();
  }

  Future<void> _submitAddress() async {
    try {
      await widget.serverConfig.setBaseUri(_addressController.text);
    } on InvalidServerAddress catch (e) {
      setState(() => _error = e.message);
      return;
    }
    if (!mounted) return;
    _baseUri = await widget.serverConfig.baseUri();
    setState(() => _error = null);
    await _startPairing();
  }

  Future<void> _startPairing() async {
    final generation = ++_generation;
    _pollTimer?.cancel();
    setState(() => _phase = _Phase.starting);

    try {
      final pubKey = await widget.identity.publicKeyBase64();
      _client?.close();
      final client = PairingClient(baseUri: _baseUri!);
      _client = client;

      final start = await client.start(
        devicePubkeyBase64: pubKey,
        deviceName: _deviceName(),
        platform: _platformName(),
      );
      if (generation != _generation || !mounted) return;

      setState(() {
        _pairingId = start.pairingId;
        _outstanding = start.outstanding;
        _phase = _Phase.waiting;
      });
      _pollTimer = Timer.periodic(const Duration(seconds: 2), (_) => _poll(client, generation));
    } on Object catch (e) {
      if (generation != _generation || !mounted) return;
      setState(() {
        _error = e.toString();
        _phase = _Phase.error;
      });
    }
  }

  Future<void> _poll(PairingClient client, int generation) async {
    final pairingId = _pairingId;
    if (pairingId == null || generation != _generation) return;

    try {
      final result = await client.poll(pairingId);
      if (generation != _generation || !mounted) return;

      if (result.completed) {
        _pollTimer?.cancel();
        await widget.identity.storeDeviceId(result.deviceId!);
        if (generation != _generation || !mounted) return;
        setState(() => _phase = _Phase.paired);
        widget.onPaired();
        return;
      }
      setState(() => _outstanding = result.outstanding);
    } on Object {
      // A single failed poll — a dropped connection, the daemon mid-restart —
      // is not fatal on its own; the next tick tries again 2 seconds later.
      // Surfacing every transient failure would bounce the operator back to
      // an error screen while they are still mid-pairing in the browser.
    }
  }

  String _deviceName() => switch (defaultTargetPlatform) {
        TargetPlatform.android => 'Android device',
        TargetPlatform.windows => 'Windows device',
        TargetPlatform.linux => 'Linux device',
        _ => 'ClaudeCode Remote device',
      };

  String _platformName() => defaultTargetPlatform.name;

  Uri? _pairUri() {
    final uri = _baseUri;
    final pairingId = _pairingId;
    if (uri == null || pairingId == null) return null;
    return uri.replace(path: '/pair', queryParameters: {'pairing_id': pairingId});
  }

  Future<void> _openBrowser() async {
    final uri = _pairUri();
    if (uri == null) return;
    await launchUrl(uri, mode: LaunchMode.externalApplication);
  }

  Future<void> _forget() async {
    _pollTimer?.cancel();
    _generation++;
    await widget.identity.forget();
    if (!mounted) return;
    setState(() {
      _pairingId = null;
      _outstanding = const [];
      _error = null;
      _phase = _Phase.needAddress;
    });
  }

  @override
  Widget build(BuildContext context) {
    final c = context.ccr;
    return Scaffold(
      backgroundColor: c.surfaceSunken,
      body: SafeArea(
        child: Center(
          child: ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: 480),
            child: Padding(
              padding: const EdgeInsets.all(CcrSpace.s6),
              child: _buildBody(c),
            ),
          ),
        ),
      ),
    );
  }

  Widget _buildBody(CcrColors c) {
    switch (_phase) {
      case _Phase.loading:
      case _Phase.starting:
        return const Center(child: CircularProgressIndicator());
      case _Phase.needAddress:
        return _AddressForm(
          controller: _addressController,
          error: _error,
          onSubmit: _submitAddress,
        );
      case _Phase.waiting:
        return _WaitingView(
          pairUri: _pairUri(),
          outstanding: _outstanding,
          onOpenBrowser: _openBrowser,
          onForget: _forget,
        );
      case _Phase.paired:
        return Center(
          child: Text('Paired.', style: CcrType.title.copyWith(color: c.textPrimary)),
        );
      case _Phase.error:
        return _ErrorView(
          message: _error ?? 'Pairing failed',
          onRetry: _startPairing,
          onForget: _forget,
        );
    }
  }
}

class _AddressForm extends StatelessWidget {
  const _AddressForm({required this.controller, required this.error, required this.onSubmit});

  final TextEditingController controller;
  final String? error;
  final VoidCallback onSubmit;

  @override
  Widget build(BuildContext context) {
    final c = context.ccr;
    return Column(
      mainAxisSize: MainAxisSize.min,
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Text('Where is your daemon?', style: CcrType.display.copyWith(color: c.textPrimary)),
        const SizedBox(height: CcrSpace.s2),
        Text(
          "The app's own address is not it — that would point back at this "
          'device. Enter the address CloudGate or your reverse proxy exposes.',
          style: CcrType.bodySm.copyWith(color: c.textSecondary),
        ),
        const SizedBox(height: CcrSpace.s6),
        TextField(
          controller: controller,
          autofocus: true,
          keyboardType: TextInputType.url,
          style: CcrType.body.copyWith(color: c.textPrimary),
          decoration: InputDecoration(
            hintText: 'https://remote.example',
            errorText: error,
            border: const OutlineInputBorder(),
          ),
          onSubmitted: (_) => onSubmit(),
        ),
        const SizedBox(height: CcrSpace.s4),
        FilledButton(
          onPressed: onSubmit,
          style: FilledButton.styleFrom(
            backgroundColor: c.accentBase,
            foregroundColor: c.accentOn,
          ),
          child: const Text('Continue', style: CcrType.label),
        ),
      ],
    );
  }
}

class _WaitingView extends StatelessWidget {
  const _WaitingView({
    required this.pairUri,
    required this.outstanding,
    required this.onOpenBrowser,
    required this.onForget,
  });

  final Uri? pairUri;
  final List<String> outstanding;
  final VoidCallback onOpenBrowser;
  final VoidCallback onForget;

  static const _factorLabels = {
    'password': 'Password',
    'totp': 'Authenticator code',
    'passkey': 'Passkey',
  };

  @override
  Widget build(BuildContext context) {
    final c = context.ccr;
    return Column(
      mainAxisSize: MainAxisSize.min,
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Text('Finish pairing in your browser', style: CcrType.title.copyWith(color: c.textPrimary)),
        const SizedBox(height: CcrSpace.s2),
        Text(
          'Password, authenticator code and passkey are entered there, never '
          'in this app.',
          style: CcrType.bodySm.copyWith(color: c.textSecondary),
        ),
        const SizedBox(height: CcrSpace.s6),
        if (pairUri != null)
          Container(
            padding: const EdgeInsets.all(CcrSpace.s3),
            decoration: BoxDecoration(
              color: c.surfaceRaised,
              border: Border.all(color: c.borderDefault),
              borderRadius: BorderRadius.circular(CcrRadius.md),
            ),
            child: SelectableText(
              pairUri.toString(),
              style: CcrType.monoUi.copyWith(color: c.textSecondary),
            ),
          ),
        const SizedBox(height: CcrSpace.s4),
        FilledButton.icon(
          onPressed: onOpenBrowser,
          style: FilledButton.styleFrom(backgroundColor: c.accentBase, foregroundColor: c.accentOn),
          icon: const Icon(Icons.open_in_browser),
          label: const Text('Open in browser', style: CcrType.label),
        ),
        const SizedBox(height: CcrSpace.s6),
        Text('Still needed', style: CcrType.label.copyWith(color: c.textPrimary)),
        const SizedBox(height: CcrSpace.s2),
        if (outstanding.isEmpty)
          Row(
            children: [
              Icon(Icons.check_circle, color: c.statusConnected, size: 18),
              const SizedBox(width: CcrSpace.s2),
              Text('All factors done — finishing…',
                  style: CcrType.bodySm.copyWith(color: c.textSecondary)),
            ],
          )
        else
          ...outstanding.map(
            (factor) => Padding(
              padding: const EdgeInsets.only(bottom: CcrSpace.s1),
              child: Row(
                children: [
                  Icon(Icons.radio_button_unchecked, color: c.textTertiary, size: 18),
                  const SizedBox(width: CcrSpace.s2),
                  Text(_factorLabels[factor] ?? factor,
                      style: CcrType.bodySm.copyWith(color: c.textSecondary)),
                ],
              ),
            ),
          ),
        const SizedBox(height: CcrSpace.s8),
        TextButton(
          onPressed: onForget,
          style: TextButton.styleFrom(foregroundColor: c.statusError),
          child: const Text('Forget this device', style: CcrType.label),
        ),
      ],
    );
  }
}

class _ErrorView extends StatelessWidget {
  const _ErrorView({required this.message, required this.onRetry, required this.onForget});

  final String message;
  final VoidCallback onRetry;
  final VoidCallback onForget;

  @override
  Widget build(BuildContext context) {
    final c = context.ccr;
    return Column(
      mainAxisSize: MainAxisSize.min,
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Text('Pairing failed', style: CcrType.title.copyWith(color: c.textPrimary)),
        const SizedBox(height: CcrSpace.s2),
        Text(message, style: CcrType.body.copyWith(color: c.statusError)),
        const SizedBox(height: CcrSpace.s6),
        FilledButton(
          onPressed: onRetry,
          style: FilledButton.styleFrom(backgroundColor: c.accentBase, foregroundColor: c.accentOn),
          child: const Text('Retry', style: CcrType.label),
        ),
        const SizedBox(height: CcrSpace.s2),
        OutlinedButton(
          onPressed: onForget,
          style: OutlinedButton.styleFrom(foregroundColor: c.statusError),
          child: const Text('Forget this device', style: CcrType.label),
        ),
      ],
    );
  }
}
