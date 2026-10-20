// 设置：订阅、按应用分流、本地覆盖、告警、日志、专业模式与关于。

import 'dart:io';

import 'package:flutter/material.dart';

import '../api.dart';
import '../state.dart';
import '../widgets.dart';
import 'apps.dart';
import 'subscription.dart';

class SettingsPage extends StatelessWidget {
  const SettingsPage({super.key});

  @override
  Widget build(BuildContext context) {
    final v = Velum.of(context);
    final s = v.status;
    final sub = s?['subscription'] as Json?;
    final outline = TextStyle(color: Theme.of(context).colorScheme.outline);
    final tun = s?['tun'] as Json?;
    void push(Widget page) => Navigator.of(context).push(MaterialPageRoute<void>(builder: (_) => Velum(state: Velum.read(context), child: page)));

    return ListView(padding: const EdgeInsets.symmetric(vertical: 8), children: [
      Section(
        title: '订阅',
        child: Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
          if (sub != null && (sub['source'] as String? ?? '').isNotEmpty) ...[
            Text('${sub['nodes']} 个节点 · ${sub['groups']} 个策略组 · ${sub['rules']} 条规则', style: outline),
            if ((sub['fetch_error'] as String?)?.isNotEmpty == true)
              Text('上次更新失败：${sub['fetch_error']}', style: TextStyle(color: Theme.of(context).colorScheme.error)),
            const SizedBox(height: 8),
          ],
          SubscriptionForm(onSaved: v.refresh),
        ]),
      ),
      Section(
        padding: EdgeInsets.zero,
        child: Column(children: [
          ListTile(
            leading: const Icon(Icons.apps),
            title: const Text('按应用分流'),
            subtitle: Text(tun == null
                ? '选择不经 Velum 的应用'
                : (tun['include_apps'] as List?)?.isNotEmpty == true
                    ? '只有 ${(tun['include_apps'] as List).length} 个应用经 Velum'
                    : (tun['exclude_apps'] as List?)?.isNotEmpty == true
                        ? '${(tun['exclude_apps'] as List).length} 个应用不经 Velum'
                        : '所有应用都经 Velum'),
            trailing: const Icon(Icons.chevron_right),
            onTap: () => push(const AppsPage()),
          ),
          ListTile(
            leading: const Icon(Icons.edit_note),
            title: const Text('本地覆盖'),
            subtitle: const Text('开发直连、hosts 覆盖（overlay.yaml）'),
            trailing: const Icon(Icons.chevron_right),
            onTap: () => push(const OverlayPage()),
          ),
          ListTile(
            leading: const Icon(Icons.warning_amber),
            title: const Text('告警'),
            subtitle: Text('${s?['warnings'] ?? 0} 条：订阅中不支持或已降级的内容'),
            trailing: const Icon(Icons.chevron_right),
            onTap: () => push(const WarningsPage()),
          ),
          ListTile(
            leading: const Icon(Icons.article_outlined),
            title: const Text('日志'),
            trailing: const Icon(Icons.chevron_right),
            onTap: () => push(const LogPage()),
          ),
        ]),
      ),
      Section(
        padding: EdgeInsets.zero,
        child: SwitchListTile(
          secondary: const Icon(Icons.tune),
          title: const Text('专业模式'),
          subtitle: const Text('显示连接与诊断页面'),
          value: v.pro,
          onChanged: v.setPro,
        ),
      ),
      Section(
        title: '关于',
        child: Column(children: [
          KV('版本', Text('${s?['version'] ?? '—'}')),
          KV('Velum 目录', SelectableText(v.home)),
        ]),
      ),
    ]);
  }
}

/// 本地覆盖（overlay.yaml）：先校验再写入，应用失败时核心自动改回原内容。
class OverlayPage extends StatefulWidget {
  const OverlayPage({super.key});

  @override
  State<OverlayPage> createState() => _OverlayPageState();
}

class _OverlayPageState extends State<OverlayPage> {
  final _ctl = TextEditingController();
  bool _loaded = false;
  bool _saving = false;

  @override
  void initState() {
    super.initState();
    Velum.read(context).api.overlay().then((r) {
      if (mounted) {
        setState(() {
          _ctl.text = (r['content'] as String?) ?? '';
          _loaded = true;
        });
      }
    }).catchError((Object e) {
      if (mounted) toast(context, errText(e));
    });
  }

  @override
  void dispose() {
    _ctl.dispose();
    super.dispose();
  }

  Future<void> _save() async {
    setState(() => _saving = true);
    try {
      await Velum.read(context).run(() => Velum.read(context).api.saveOverlay(_ctl.text));
      if (mounted) toast(context, '已应用');
    } catch (e) {
      if (mounted) toast(context, errText(e));
    } finally {
      if (mounted) setState(() => _saving = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(title: const Text('本地覆盖'), actions: [
        TextButton(onPressed: !_loaded || _saving ? null : _save, child: Text(_saving ? '应用中…' : '应用')),
      ]),
      body: Padding(
        padding: const EdgeInsets.all(12),
        child: TextField(
          controller: _ctl,
          enabled: _loaded,
          maxLines: null,
          expands: true,
          textAlignVertical: TextAlignVertical.top,
          autocorrect: false,
          style: const TextStyle(fontFamily: 'monospace', fontSize: 13),
          decoration: const InputDecoration(border: OutlineInputBorder()),
        ),
      ),
    );
  }
}

class WarningsPage extends StatefulWidget {
  const WarningsPage({super.key});

  @override
  State<WarningsPage> createState() => _WarningsPageState();
}

class _WarningsPageState extends State<WarningsPage> {
  List<Json>? _list;

  @override
  void initState() {
    super.initState();
    Velum.read(context).api.warnings().then((w) {
      if (mounted) setState(() => _list = w);
    }).catchError((Object e) {
      if (mounted) toast(context, errText(e));
    });
  }

  static const _names = {
    'node': '节点', 'provider': '订阅源', 'group': '策略组', 'rule': '规则', 'ruleset': '规则集', 'dns': 'DNS', 'global': '全局', 'overlay': '本地设置',
  };

  @override
  Widget build(BuildContext context) {
    final list = _list;
    final byCat = <String, List<Json>>{};
    for (final w in list ?? <Json>[]) {
      byCat.putIfAbsent(w['category'] as String, () => []).add(w);
    }
    final outline = TextStyle(color: Theme.of(context).colorScheme.outline);
    return Scaffold(
      appBar: AppBar(title: const Text('告警')),
      body: list == null
          ? const Center(child: CircularProgressIndicator())
          : list.isEmpty
              ? Center(child: Text('没有告警', style: outline))
              : ListView(padding: const EdgeInsets.symmetric(vertical: 8), children: [
                  for (final e in byCat.entries)
                    Section(
                      title: '${_names[e.key] ?? e.key}（${e.value.length}）',
                      child: Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
                        for (final w in e.value)
                          Padding(
                            padding: const EdgeInsets.symmetric(vertical: 4),
                            child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                              Text('${(w['subject'] as String?)?.isNotEmpty == true ? '${w['subject']}：' : ''}${w['reason']}'),
                              SelectableText(srcText((w['source'] as Map?)?.cast<String, dynamic>()), style: outline),
                            ]),
                          ),
                      ]),
                    ),
                ]),
    );
  }
}

/// 核心日志（最后 500 行）。核心与界面在同一进程，直接读文件。
class LogPage extends StatefulWidget {
  const LogPage({super.key});

  @override
  State<LogPage> createState() => _LogPageState();
}

class _LogPageState extends State<LogPage> {
  String _text = '';

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    final f = File('${Velum.read(context).home}/logs/velumd.log');
    String text;
    try {
      final lines = await f.readAsLines();
      text = lines.skip(lines.length > 500 ? lines.length - 500 : 0).join('\n');
    } catch (e) {
      text = '无法读取日志：$e';
    }
    if (mounted) setState(() => _text = text);
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(title: const Text('日志'), actions: [IconButton(onPressed: _load, icon: const Icon(Icons.refresh))]),
      body: SingleChildScrollView(
        reverse: true,
        padding: const EdgeInsets.all(12),
        child: SelectableText(_text, style: const TextStyle(fontFamily: 'monospace', fontSize: 11)),
      ),
    );
  }
}
