// 订阅地址：先拉取并校验，通过后才写入 config.yaml。

import 'package:flutter/material.dart';

import '../state.dart';

class SubscriptionForm extends StatefulWidget {
  const SubscriptionForm({super.key, required this.onSaved});
  final VoidCallback onSaved;

  @override
  State<SubscriptionForm> createState() => _SubscriptionFormState();
}

class _SubscriptionFormState extends State<SubscriptionForm> {
  final _ctl = TextEditingController();
  bool _saving = false;

  @override
  void initState() {
    super.initState();
    Velum.read(context).api.subscription().then((r) {
      if (mounted && _ctl.text.isEmpty) _ctl.text = (r['url'] as String?) ?? '';
    }).catchError((_) {});
  }

  @override
  void dispose() {
    _ctl.dispose();
    super.dispose();
  }

  Future<void> _save() async {
    FocusScope.of(context).unfocus();
    setState(() => _saving = true);
    try {
      await Velum.read(context).api.setSubscription(_ctl.text.trim());
      if (mounted) toast(context, '订阅已更新');
      widget.onSaved();
    } catch (e) {
      if (mounted) toast(context, '订阅无效：${errText(e)}');
    } finally {
      if (mounted) setState(() => _saving = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    return Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
      TextField(
        controller: _ctl,
        decoration: const InputDecoration(labelText: '订阅地址', hintText: 'https://…', border: OutlineInputBorder()),
        keyboardType: TextInputType.url,
        autocorrect: false,
        onSubmitted: (_) => _save(),
      ),
      const SizedBox(height: 8),
      FilledButton(onPressed: _saving ? null : _save, child: Text(_saving ? '校验中…' : '保存')),
    ]);
  }
}
