// 共用的小部件。

import 'package:flutter/material.dart';

import 'health.dart';

/// 带标题的分组卡片。
class Section extends StatelessWidget {
  const Section({super.key, this.title, this.trailing, required this.child, this.padding = const EdgeInsets.fromLTRB(16, 12, 16, 16)});
  final String? title;
  final Widget? trailing;
  final Widget child;
  final EdgeInsets padding;

  @override
  Widget build(BuildContext context) {
    final t = Theme.of(context).textTheme;
    return Card(
      margin: const EdgeInsets.symmetric(horizontal: 16, vertical: 6),
      clipBehavior: Clip.antiAlias,
      child: Padding(
        padding: padding,
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            if (title != null || trailing != null)
              Padding(
                padding: const EdgeInsets.only(bottom: 8),
                child: Row(children: [
                  Expanded(child: Text(title ?? '', style: t.titleSmall)),
                  ?trailing,
                ]),
              ),
            child,
          ],
        ),
      ),
    );
  }
}

Color toneColor(BuildContext context, Tone tone) => switch (tone) {
      Tone.green => Colors.green,
      Tone.amber => Colors.orange,
      Tone.red => Theme.of(context).colorScheme.error,
      Tone.gray => Theme.of(context).colorScheme.outline,
    };

/// 延迟：null 未测，负数失败。
class DelayText extends StatelessWidget {
  const DelayText(this.ms, {super.key});
  final int? ms;

  @override
  Widget build(BuildContext context) {
    final ms = this.ms;
    final style = Theme.of(context).textTheme.bodySmall;
    if (ms == null) return Text('—', style: style?.copyWith(color: Theme.of(context).colorScheme.outline));
    if (ms < 0) return Text('超时', style: style?.copyWith(color: Theme.of(context).colorScheme.error));
    final c = ms < 300 ? Colors.green : (ms < 800 ? Colors.orange : Theme.of(context).colorScheme.error);
    return Text('${ms}ms', style: style?.copyWith(color: c, fontFeatures: const [FontFeature.tabularFigures()]));
  }
}

/// 键值行。
class KV extends StatelessWidget {
  const KV(this.k, this.v, {super.key});
  final String k;
  final Widget v;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 3),
      child: Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
        SizedBox(width: 88, child: Text(k, style: TextStyle(color: Theme.of(context).colorScheme.outline))),
        Expanded(child: v),
      ]),
    );
  }
}

/// 来源（文件:行 + 原文），用于诊断与告警。
String srcText(Map<String, dynamic>? s) {
  if (s == null) return '';
  var loc = (s['file'] as String?)?.split('/').last ?? (s['kind'] as String? ?? '');
  if (s['line'] != null) loc += ':${s['line']}';
  final text = s['text'] as String?;
  return text == null || text.isEmpty ? loc : '$loc  $text';
}

String human(num n) {
  if (n >= 1 << 20) return '${(n / (1 << 20)).toStringAsFixed(1)}M';
  if (n >= 1 << 10) return '${(n / (1 << 10)).toStringAsFixed(1)}K';
  return '${n}B';
}
