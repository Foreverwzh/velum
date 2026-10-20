// 连接（专业模式）：最近的连接，可按域名 / 应用过滤，点开查看命中规则、出站链与来源。

import 'dart:async';

import 'package:flutter/material.dart';

import '../api.dart';
import '../state.dart';
import '../widgets.dart';

class ConnectionsPage extends StatefulWidget {
  const ConnectionsPage({super.key});

  @override
  State<ConnectionsPage> createState() => _ConnectionsPageState();
}

class _ConnectionsPageState extends State<ConnectionsPage> {
  List<Json> _conns = [];
  String _filter = '';
  String? _err;
  Timer? _timer;

  @override
  void initState() {
    super.initState();
    _load();
    _timer = Timer.periodic(const Duration(seconds: 2), (_) => _load());
  }

  @override
  void dispose() {
    _timer?.cancel();
    super.dispose();
  }

  Future<void> _load() async {
    try {
      final c = await Velum.read(context).api.connections(limit: 300);
      if (mounted) {
        setState(() {
          _conns = c;
          _err = null;
        });
      }
    } catch (e) {
      if (mounted) setState(() => _err = errText(e));
    }
  }

  @override
  Widget build(BuildContext context) {
    final f = _filter.toLowerCase();
    final list = f.isEmpty
        ? _conns
        : _conns.where((c) => '${c['domain'] ?? ''} ${c['destination']} ${c['process'] ?? ''} ${(c['chain'] as List?)?.join(' ') ?? ''}'.toLowerCase().contains(f)).toList();
    return Column(children: [
      Padding(
        padding: const EdgeInsets.fromLTRB(16, 8, 16, 4),
        child: TextField(
          decoration: const InputDecoration(prefixIcon: Icon(Icons.search), hintText: '按域名、应用、出站过滤', isDense: true, border: OutlineInputBorder()),
          onChanged: (v) => setState(() => _filter = v.trim()),
        ),
      ),
      if (_err != null) Padding(padding: const EdgeInsets.all(16), child: Text(_err!)),
      Expanded(
        child: list.isEmpty
            ? Center(child: Text(_conns.isEmpty ? '还没有连接记录' : '没有匹配的连接', style: TextStyle(color: Theme.of(context).colorScheme.outline)))
            : ListView.separated(
                itemCount: list.length,
                separatorBuilder: (_, _) => const Divider(height: 1),
                itemBuilder: (ctx, i) => _ConnTile(list[i]),
              ),
      ),
    ]);
  }
}

bool _rejected(Json c) => ((c['chain'] as List?) ?? []).any((x) => x == 'REJECT' || x == 'block');

class _ConnTile extends StatelessWidget {
  const _ConnTile(this.c);
  final Json c;

  @override
  Widget build(BuildContext context) {
    final live = c['end'] == null;
    final errs = (c['errors'] as List?) ?? [];
    final chain = ((c['chain'] as List?) ?? []).join(' → ');
    final scheme = Theme.of(context).colorScheme;
    final process = c['process'] as String?;
    return ListTile(
      dense: true,
      leading: Icon(
        errs.isNotEmpty || _rejected(c) ? Icons.block : (live ? Icons.sync_alt : Icons.check),
        color: errs.isNotEmpty ? scheme.error : (_rejected(c) ? Colors.orange : (live ? scheme.primary : scheme.outline)),
        size: 20,
      ),
      title: Text((c['domain'] as String?)?.isNotEmpty == true ? c['domain'] as String : c['destination'] as String, overflow: TextOverflow.ellipsis),
      subtitle: Text([if (process != null && process.isNotEmpty) process, chain].join(' · '), overflow: TextOverflow.ellipsis),
      trailing: Text('↑${human(c['upload'] as num)} ↓${human(c['download'] as num)}', style: Theme.of(context).textTheme.bodySmall),
      onTap: () => showModalBottomSheet<void>(
        context: context,
        showDragHandle: true,
        isScrollControlled: true,
        builder: (_) => DraggableScrollableSheet(
          expand: false,
          initialChildSize: 0.6,
          maxChildSize: 0.95,
          builder: (ctx, scroll) => ListView(controller: scroll, padding: const EdgeInsets.fromLTRB(16, 0, 16, 24), children: [
            SelectableText((c['domain'] as String?)?.isNotEmpty == true ? c['domain'] as String : c['destination'] as String,
                style: Theme.of(ctx).textTheme.titleMedium),
            const SizedBox(height: 8),
            KV('目标', SelectableText(c['destination'] as String)),
            if ((c['resolved'] as List?)?.isNotEmpty == true) KV('解析为', SelectableText((c['resolved'] as List).join(', '))),
            if (process != null && process.isNotEmpty) KV('应用', SelectableText(process)),
            KV('协议', Text('${c['network']}${(c['sniffed'] as String?)?.isNotEmpty == true ? ' / ${c['sniffed']}' : ''}')),
            KV('入站', Text(c['inbound'] as String)),
            KV('命中规则', SelectableText(c['rule'] as String)),
            KV('规则来源', SelectableText(srcText((c['provenance'] as Map?)?.cast<String, dynamic>()))),
            KV('出站链', SelectableText(chain)),
            KV('流量', Text('↑${human(c['upload'] as num)}  ↓${human(c['download'] as num)}')),
            KV('首字节', Text((c['first_byte_ms'] as num) < 0 ? '无下行数据' : '${c['first_byte_ms']}ms')),
            KV('时长', Text(live ? '进行中' : '${c['duration_ms']}ms')),
            for (final e in errs) KV('错误', SelectableText(e as String, style: TextStyle(color: Theme.of(ctx).colorScheme.error))),
          ]),
        ),
      ),
    );
  }
}
