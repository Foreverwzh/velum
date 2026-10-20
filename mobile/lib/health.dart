// 把核心状态归纳为一句给人看的结论（与桌面端 desktop/src/health.ts 口径一致，去掉系统代理相关的分支）。

import 'api.dart';

enum Tone { green, amber, red, gray }

class Health {
  const Health(this.tone, this.title, this.detail);
  final Tone tone;
  final String title;
  final String detail;
}

Health health(Json? s, [String? err]) {
  if (s == null) return Health(Tone.red, 'Velum 未运行', err ?? '无法连接核心');
  final lastError = s['last_error'] as String?;
  if (lastError != null && lastError.isNotEmpty) return Health(Tone.red, '配置有问题', lastError);
  if (s['running'] != true) return const Health(Tone.red, '引擎未运行', '查看日志或尝试一键修复');
  final source = (s['subscription'] as Json?)?['source'] as String?;
  if (source == null || source.isEmpty) return const Health(Tone.gray, '还没有订阅', '填写订阅地址后即可开始使用');
  final network = s['network'] as Json?;
  final paused = network?['paused'] as String?;
  if (paused != null && paused.isNotEmpty) return Health(Tone.amber, '已暂停接管', paused);
  if (network?['captive'] == 'captive') return const Health(Tone.amber, '需要网页认证', '当前网络需要先在浏览器中完成登录');
  if (network?['captive'] == 'offline') return const Health(Tone.amber, '网络不通', '当前网络无法访问互联网');
  if (s['tun_on'] == true) return const Health(Tone.green, '正在保护', '所有应用的流量经 Velum 分流');
  return const Health(Tone.gray, '未连接', '打开开关后，所有应用的流量经 Velum 分流');
}
