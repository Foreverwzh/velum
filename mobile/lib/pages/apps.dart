// 按应用分流：选择不经 Velum 的应用（或只让所选应用经 Velum），写入 config.yaml 的 tun 节。

import 'dart:typed_data';

import 'package:flutter/material.dart';

import '../api.dart';
import '../state.dart';

class AppsPage extends StatefulWidget {
  const AppsPage({super.key});

  @override
  State<AppsPage> createState() => _AppsPageState();
}

class _AppsPageState extends State<AppsPage> {
  List<AppInfo>? _apps;
  bool _include = false; // false：所选应用不经 Velum；true：只有所选应用经 Velum
  Set<String> _selected = {};
  Set<String> _initial = {}; // 打开页面时已选的应用排在前面（勾选时不重新排序，避免列表跳动）
  bool _showSystem = false;
  String _filter = '';
  bool _dirty = false;
  bool _saving = false;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    try {
      final api = Velum.read(context).api;
      final cfg = await api.tunApps();
      final apps = await Native.apps();
      apps.sort((a, b) => a.label.toLowerCase().compareTo(b.label.toLowerCase()));
      setState(() {
        _apps = apps;
        _include = cfg.include.isNotEmpty;
        _selected = {...(_include ? cfg.include : cfg.exclude)};
        _initial = {..._selected};
      });
    } catch (e) {
      if (mounted) toast(context, errText(e));
    }
  }

  Future<void> _save() async {
    setState(() => _saving = true);
    try {
      final list = _selected.toList()..sort();
      final v = Velum.read(context);
      await v.run(() => _include ? v.api.setTunApps(include: list) : v.api.setTunApps(exclude: list));
      if (mounted) {
        setState(() => _dirty = false);
        toast(context, '已保存，VPN 已按新设置重新建立');
      }
    } catch (e) {
      if (mounted) toast(context, errText(e));
    } finally {
      if (mounted) setState(() => _saving = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final apps = _apps;
    final f = _filter.toLowerCase();
    // 已选的应用总是显示（即使是系统应用）
    final shown = apps
        ?.where((a) => (_showSystem || !a.system || _selected.contains(a.package)) &&
            (f.isEmpty || a.label.toLowerCase().contains(f) || a.package.toLowerCase().contains(f)))
        .toList()
      ?..sort((a, b) => (_initial.contains(a.package) ? 0 : 1).compareTo(_initial.contains(b.package) ? 0 : 1));
    return Scaffold(
      appBar: AppBar(title: const Text('按应用分流'), actions: [
        TextButton(onPressed: !_dirty || _saving ? null : _save, child: Text(_saving ? '保存中…' : '保存')),
      ]),
      body: apps == null
          ? const Center(child: CircularProgressIndicator())
          : Column(children: [
              Padding(
                padding: const EdgeInsets.fromLTRB(16, 8, 16, 0),
                child: SegmentedButton<bool>(
                  segments: const [
                    ButtonSegment(value: false, label: Text('所选应用不经 Velum')),
                    ButtonSegment(value: true, label: Text('只有所选应用经 Velum')),
                  ],
                  selected: {_include},
                  onSelectionChanged: (v) => setState(() {
                    _include = v.first;
                    _dirty = true;
                  }),
                ),
              ),
              Padding(
                padding: const EdgeInsets.fromLTRB(16, 8, 16, 0),
                child: TextField(
                  decoration: const InputDecoration(prefixIcon: Icon(Icons.search), hintText: '搜索应用', isDense: true, border: OutlineInputBorder()),
                  onChanged: (v) => setState(() => _filter = v.trim()),
                ),
              ),
              SwitchListTile(
                dense: true,
                title: const Text('显示系统应用'),
                value: _showSystem,
                onChanged: (v) => setState(() => _showSystem = v),
              ),
              const Divider(height: 1),
              Expanded(
                child: ListView.builder(
                  itemCount: shown!.length,
                  itemBuilder: (ctx, i) {
                    final a = shown[i];
                    return CheckboxListTile(
                      secondary: _AppIcon(a.package),
                      title: Text(a.label, overflow: TextOverflow.ellipsis),
                      subtitle: Text(a.package, overflow: TextOverflow.ellipsis),
                      value: _selected.contains(a.package),
                      onChanged: (v) => setState(() {
                        v == true ? _selected.add(a.package) : _selected.remove(a.package);
                        _dirty = true;
                      }),
                    );
                  },
                ),
              ),
            ]),
    );
  }
}

class _AppIcon extends StatelessWidget {
  const _AppIcon(this.package);
  final String package;

  @override
  Widget build(BuildContext context) {
    return SizedBox(
      width: 36,
      height: 36,
      child: FutureBuilder<Uint8List?>(
        future: Native.appIcon(package),
        builder: (ctx, snap) => snap.data == null ? const Icon(Icons.android) : Image.memory(snap.data!, gaplessPlayback: true),
      ),
    );
  }
}
