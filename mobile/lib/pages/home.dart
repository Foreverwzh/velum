// 首页（极简视图）：普通用户也能看懂的状态、总开关、分流选择、一键修复。与桌面端 Simple 视图口径一致。

import 'package:flutter/material.dart';

import '../api.dart';
import '../health.dart';
import '../state.dart';
import '../widgets.dart';
import 'subscription.dart';

class HomePage extends StatefulWidget {
  const HomePage({super.key});

  @override
  State<HomePage> createState() => _HomePageState();
}

class _HomePageState extends State<HomePage> {
  String? _busy;
  List<Json>? _steps;
  Map<String, bool> _failed = {};

  Future<void> _do(String label, Future<void> Function() fn) async {
    setState(() => _busy = label);
    try {
      await Velum.read(context).run(fn);
    } catch (e) {
      if (mounted) toast(context, errText(e));
    } finally {
      if (mounted) setState(() => _busy = null);
    }
  }

  Future<void> _toggle(bool on) => _do('tun', () async {
        final api = Velum.read(context).api;
        if (on) {
          if (!await Native.prepareVpn()) throw ApiError('需要允许 Velum 建立 VPN 才能接管流量');
          await Native.requestNotifications();
        }
        await api.setSwitch('tun', on ? 'on' : 'off');
      });

  @override
  Widget build(BuildContext context) {
    final v = Velum.of(context);
    final s = v.status;
    final h = health(s, v.error);
    final hasSub = ((s?['subscription'] as Json?)?['source'] as String? ?? '').isNotEmpty;
    final groups = ((s?['groups'] as List?) ?? []).cast<Json>();
    final selectable = groups.where((g) => g['type'] == 'select').toList();

    return RefreshIndicator(
      onRefresh: v.refresh,
      child: ListView(
        padding: const EdgeInsets.symmetric(vertical: 8),
        children: [
          if (s != null && !hasSub)
            Section(
              title: '欢迎使用 Velum',
              child: Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
                Text('填写订阅地址即可开始使用。订阅地址由提供订阅的人给你。', style: TextStyle(color: Theme.of(context).colorScheme.outline)),
                const SizedBox(height: 8),
                SubscriptionForm(onSaved: v.refresh),
              ]),
            ),
          _StatusCard(h: h, status: s, busy: _busy == 'tun', onToggle: s == null || !hasSub || _busy != null ? null : _toggle),
          if (s?['network'] case final Json n) _NetworkCard(n),
          if (selectable.isNotEmpty)
            Section(
              title: '分流',
              trailing: TextButton(
                onPressed: _busy != null
                    ? null
                    : () => _do('test', () async {
                          final r = await v.api.test('');
                          final delays = (r['delays'] as Map?)?.cast<String, dynamic>() ?? {};
                          setState(() => _failed = {for (final e in delays.entries) if ((e.value as num) < 0) e.key: true});
                        }),
                child: Text(_busy == 'test' ? '测速中…' : '测速'),
              ),
              padding: const EdgeInsets.fromLTRB(16, 4, 8, 8),
              child: GroupList(groups: groups, selectable: selectable, failed: _failed, enabled: _busy == null,
                  onSelect: (g, m) => _do('select', () => v.api.select(g, m))),
            ),
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 8, 16, 0),
            child: Row(children: [
              Expanded(
                child: FilledButton.icon(
                  icon: const Icon(Icons.healing),
                  onPressed: s == null || _busy != null
                      ? null
                      : () => _do('repair', () async {
                            final r = await v.api.repair();
                            setState(() => _steps = ((r['steps'] as List?) ?? []).cast<Json>());
                          }),
                  label: Text(_busy == 'repair' ? '修复中…' : '一键修复'),
                ),
              ),
              const SizedBox(width: 12),
              Expanded(
                child: OutlinedButton.icon(
                  icon: const Icon(Icons.refresh),
                  onPressed: s == null || !hasSub || _busy != null ? null : () => _do('update', v.api.update),
                  label: Text(_busy == 'update' ? '更新中…' : '更新订阅'),
                ),
              ),
            ]),
          ),
          if (_steps case final steps?)
            Section(
              title: '修复结果',
              child: Column(children: [
                for (final st in steps)
                  ListTile(
                    dense: true,
                    contentPadding: EdgeInsets.zero,
                    leading: Icon(st['ok'] == true ? Icons.check_circle : Icons.error,
                        color: st['ok'] == true ? Colors.green : Theme.of(context).colorScheme.error),
                    title: Text(st['name'] as String),
                    subtitle: (st['detail'] as String?)?.isNotEmpty == true ? Text(st['detail'] as String) : null,
                  ),
              ]),
            ),
          const SizedBox(height: 16),
        ],
      ),
    );
  }
}

class _StatusCard extends StatelessWidget {
  const _StatusCard({required this.h, required this.status, required this.busy, required this.onToggle});
  final Health h;
  final Json? status;
  final bool busy;
  final ValueChanged<bool>? onToggle;

  @override
  Widget build(BuildContext context) {
    final color = toneColor(context, h.tone);
    final want = status?['want_tun'] == true;
    final t = Theme.of(context).textTheme;
    return Section(
      child: Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
        Row(children: [
          Container(
            width: 48,
            height: 48,
            decoration: BoxDecoration(color: color.withValues(alpha: 0.15), shape: BoxShape.circle),
            child: Icon(h.tone == Tone.green ? Icons.shield : Icons.shield_outlined, color: color),
          ),
          const SizedBox(width: 16),
          Expanded(
            child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Text(h.title, style: t.titleLarge),
              Text(h.detail, style: t.bodyMedium?.copyWith(color: Theme.of(context).colorScheme.outline)),
            ]),
          ),
        ]),
        const SizedBox(height: 16),
        SizedBox(
          height: 52,
          child: want
              ? OutlinedButton(onPressed: onToggle == null ? null : () => onToggle!(false), child: Text(busy ? '正在断开…' : '断开'))
              : FilledButton(onPressed: onToggle == null ? null : () => onToggle!(true), child: Text(busy ? '正在连接…' : '连接')),
        ),
      ]),
    );
  }
}

class _NetworkCard extends StatelessWidget {
  const _NetworkCard(this.n);
  final Json n;

  @override
  Widget build(BuildContext context) {
    final captive = n['captive'] as String?;
    return Section(
      title: '网络环境',
      child: Column(children: [
        KV('当前网络', Text((n['primary_interface'] as String?)?.isNotEmpty == true ? n['primary_interface'] as String : '—')),
        KV(
          '连通性',
          Text(switch (captive) { 'online' => '正常', 'captive' => '需要网页认证', 'offline' => '离线', _ => '检测中…' },
              style: TextStyle(color: switch (captive) { 'online' => Colors.green, 'captive' => Colors.orange, 'offline' => Theme.of(context).colorScheme.error, _ => null })),
        ),
      ]),
    );
  }
}

/// 每个可选策略组一行：当前选择 → 实际节点 + 延迟；点开选择成员。
class GroupList extends StatelessWidget {
  const GroupList({super.key, required this.groups, required this.selectable, required this.failed, required this.enabled, required this.onSelect});
  final List<Json> groups;
  final List<Json> selectable;
  final Map<String, bool> failed;
  final bool enabled;
  final void Function(String group, String member) onSelect;

  @override
  Widget build(BuildContext context) {
    final byTag = {for (final g in groups) g['tag'] as String: g};
    String leafOf(String tag) {
      var cur = tag;
      for (var i = 0; i < 8; i++) {
        final sel = byTag[cur]?['selected'] as String?;
        if (sel == null || sel.isEmpty) break;
        cur = sel;
      }
      return cur;
    }

    int? delayOf(String tag) {
      if (failed[tag] == true) return -1;
      for (final g in groups) {
        final d = (g['delays'] as Map?)?[tag];
        if (d != null) return (d as num).toInt();
      }
      return null;
    }

    return Column(children: [
      for (final g in selectable)
        ListTile(
          contentPadding: EdgeInsets.zero,
          title: Text(g['tag'] as String, overflow: TextOverflow.ellipsis),
          subtitle: Text(leafOf(g['tag'] as String), overflow: TextOverflow.ellipsis),
          trailing: Row(mainAxisSize: MainAxisSize.min, children: [DelayText(delayOf(leafOf(g['tag'] as String))), const Icon(Icons.chevron_right)]),
          onTap: !enabled
              ? null
              : () => showModalBottomSheet<void>(
                    context: context,
                    showDragHandle: true,
                    isScrollControlled: true,
                    builder: (ctx) => DraggableScrollableSheet(
                      expand: false,
                      initialChildSize: 0.6,
                      maxChildSize: 0.9,
                      builder: (ctx, scroll) => ListView(controller: scroll, children: [
                        Padding(
                          padding: const EdgeInsets.fromLTRB(16, 0, 16, 8),
                          child: Text(g['tag'] as String, style: Theme.of(ctx).textTheme.titleMedium),
                        ),
                        for (final m in ((g['members'] as List?) ?? []).cast<String>())
                          ListTile(
                            leading: Icon(m == g['selected'] ? Icons.radio_button_checked : Icons.radio_button_unchecked,
                                color: m == g['selected'] ? Theme.of(ctx).colorScheme.primary : null),
                            title: Text(m, overflow: TextOverflow.ellipsis),
                            subtitle: byTag.containsKey(m) ? Text('→ ${leafOf(m)}', overflow: TextOverflow.ellipsis) : null,
                            trailing: DelayText(delayOf(byTag.containsKey(m) ? leafOf(m) : m)),
                            onTap: () {
                              Navigator.pop(ctx);
                              if (m != g['selected']) onSelect(g['tag'] as String, m);
                            },
                          ),
                      ]),
                    ),
                  ),
        ),
    ]);
  }
}
