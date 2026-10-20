// 诊断（专业模式）：输入 URL / 域名 / IP，推演 DNS → 规则 → 出站，并与实际连接对照。与桌面端「诊断」一致。
// Android 没有系统代理，省略桌面端的「系统代理」一步。

import 'package:flutter/material.dart';

import '../api.dart';
import '../state.dart';
import '../widgets.dart';

class DiagnosePage extends StatefulWidget {
  const DiagnosePage({super.key});

  @override
  State<DiagnosePage> createState() => _DiagnosePageState();
}

class _DiagnosePageState extends State<DiagnosePage> {
  final _ctl = TextEditingController();
  Json? _res;
  List<Json> _live = [];
  String? _err;
  bool _all = false;
  bool _busy = false;

  @override
  void dispose() {
    _ctl.dispose();
    super.dispose();
  }

  Future<void> _run() async {
    final target = _ctl.text.trim();
    if (target.isEmpty) return;
    FocusScope.of(context).unfocus();
    setState(() => _busy = true);
    final api = Velum.read(context).api;
    try {
      final r = await api.why(target);
      final t = (r['report'] as Json)['target'] as Json;
      final live = await api.connections(limit: 5, host: (t['domain'] ?? t['ip'] ?? '') as String);
      setState(() {
        _res = r;
        _live = live;
        _err = null;
      });
    } catch (e) {
      setState(() => _err = errText(e));
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final res = _res;
    return ListView(padding: const EdgeInsets.symmetric(vertical: 8), children: [
      Padding(
        padding: const EdgeInsets.symmetric(horizontal: 16),
        child: Row(children: [
          Expanded(
            child: TextField(
              controller: _ctl,
              decoration: const InputDecoration(hintText: 'URL、域名或 IP，如 www.google.com', isDense: true, border: OutlineInputBorder()),
              keyboardType: TextInputType.url,
              autocorrect: false,
              onSubmitted: (_) => _run(),
            ),
          ),
          const SizedBox(width: 8),
          FilledButton(onPressed: _busy ? null : _run, child: const Text('推演')),
        ]),
      ),
      if (_err != null) Padding(padding: const EdgeInsets.all(16), child: Text(_err!, style: TextStyle(color: Theme.of(context).colorScheme.error))),
      if (res != null) ..._report(context, res),
    ]);
  }

  List<Widget> _report(BuildContext context, Json res) {
    final r = res['report'] as Json;
    final t = r['target'] as Json;
    final dns = r['dns'] as Json;
    final d = r['decision'] as Json;
    final rules = ((r['rules'] as List?) ?? []).cast<Json>();
    final shown = rules.where((x) => _all || x['matched'] == true || x['unknown'] == true).toList();
    final scheme = Theme.of(context).colorScheme;
    final outline = TextStyle(color: scheme.outline);
    final resolved = ((res['resolved'] as List?) ?? []).cast<String>();
    final chain = ((res['chain'] as List?) ?? []).cast<String>();
    Map<String, dynamic>? src(Object? s) => (s as Map?)?.cast<String, dynamic>();

    return [
      Section(
        title: '${t['domain'] ?? t['ip']}:${t['port']}',
        trailing: d['uncertain'] == true
            ? const Chip(label: Text('结论不确定'), visualDensity: VisualDensity.compact)
            : const Chip(label: Text('确定'), visualDensity: VisualDensity.compact),
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          _Step('① DNS', [
            Text(dns['applicable'] != true
                ? '目标是 IP，无需解析'
                : (dns['answer'] as List?)?.isNotEmpty == true
                    ? 'hosts 覆盖 ${(dns['answer'] as List).join(', ')}'
                    : '${dns['server'] ?? ''}'),
            if (src(dns['source']) != null) SelectableText(srcText(src(dns['source'])), style: outline),
            if (resolved.isNotEmpty) Text('解析结果 ${resolved.join(', ')}', style: outline),
            if ((res['resolve_error'] as String?)?.isNotEmpty == true) Text('解析失败：${res['resolve_error']}', style: const TextStyle(color: Colors.orange)),
          ]),
          _Step('② 规则', [
            for (final x in shown)
              Padding(
                padding: const EdgeInsets.symmetric(vertical: 2),
                child: Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
                  SizedBox(width: 40, child: Text('#${x['index']}', style: outline)),
                  Text(x['action'] == 'resolve' ? '↻ ' : x['matched'] == true ? '✓ ' : x['unknown'] == true ? '? ' : '✗ ',
                      style: TextStyle(color: x['action'] == 'resolve' ? scheme.primary : x['matched'] == true ? Colors.green : x['unknown'] == true ? Colors.orange : scheme.outline)),
                  Expanded(
                    child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                      SelectableText(srcText(src(x['source']))),
                      Text('${x['reason']}', style: outline),
                    ]),
                  ),
                ]),
              ),
            if ((d['rule_index'] as num) < 0) Text('final ✓ ${srcText(src(d['source']))}'),
            TextButton(
              style: TextButton.styleFrom(padding: EdgeInsets.zero, visualDensity: VisualDensity.compact),
              onPressed: () => setState(() => _all = !_all),
              child: Text(_all ? '只看命中与未知' : '显示全部 ${rules.length} 条（含未命中原因）'),
            ),
          ]),
          _Step('③ 出站', [
            Text(chain.isNotEmpty ? chain.join(' → ') : (d['action'] == 'reject' ? '拦截' : '${d['outbound'] ?? ''}'),
                style: const TextStyle(fontWeight: FontWeight.w600)),
            if ((d['override_address'] as String?)?.isNotEmpty == true) Text('目标改写为 ${d['override_address']}（hosts 覆盖）', style: outline),
          ]),
          for (final n in ((r['notes'] as List?) ?? []).cast<String>())
            Container(
              margin: const EdgeInsets.only(top: 8),
              padding: const EdgeInsets.all(8),
              decoration: BoxDecoration(color: Colors.orange.withValues(alpha: 0.12), borderRadius: BorderRadius.circular(8)),
              child: Text(n),
            ),
        ]),
      ),
      Section(
        title: '实际连接（最近 5 条）',
        child: _live.isEmpty
            ? Text('暂无记录。经 Velum 访问一次该目标后再推演，可对照实际走向。', style: outline)
            : Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
                for (final c in _live)
                  Padding(
                    padding: const EdgeInsets.symmetric(vertical: 4),
                    child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                      Row(children: [
                        Expanded(child: Text(((c['chain'] as List?) ?? []).join(' → '))),
                        if (c['rule_index'] != d['rule_index']) const Text('与推演不一致', style: TextStyle(color: Colors.orange)),
                      ]),
                      Text(
                        '${(c['rule_index'] as num) >= 0 ? '#${c['rule_index']} ' : 'final '}${srcText(src(c['provenance']))} · ↓${human(c['download'] as num)}',
                        style: outline,
                      ),
                    ]),
                  ),
              ]),
      ),
    ];
  }
}

class _Step extends StatelessWidget {
  const _Step(this.title, this.children);
  final String title;
  final List<Widget> children;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 6),
      child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Text(title, style: Theme.of(context).textTheme.labelLarge?.copyWith(color: Theme.of(context).colorScheme.outline)),
        const SizedBox(height: 2),
        ...children,
      ]),
    );
  }
}
