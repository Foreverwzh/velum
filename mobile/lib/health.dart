// 把核心状态归纳为一句给人看的结论（与桌面端 desktop/src/health.ts 口径一致，去掉系统代理相关的分支）。

import 'i18n.dart';
import 'api.dart';

enum Tone { green, amber, red, gray }

class Health {
  const Health(this.tone, this.title, this.detail);
  final Tone tone;
  final String title;
  final String detail;
}

Health health(Json? s, [String? err]) {
  if (s == null) return Health(Tone.red, t('Velum 未运行'), err ?? t('无法连接核心'));
  final lastError = s['last_error'] as String?;
  if (lastError != null && lastError.isNotEmpty) return Health(Tone.red, t('配置有问题'), lastError);
  if (s['running'] != true) return Health(Tone.red, t('引擎未运行'), t('查看日志或尝试一键修复'));
  final source = (s['subscription'] as Json?)?['source'] as String?;
  if (source == null || source.isEmpty) return Health(Tone.gray, t('还没有订阅'), t('填写订阅地址后即可开始使用'));
  final network = s['network'] as Json?;
  final paused = network?['paused'] as String?;
  if (paused != null && paused.isNotEmpty) return Health(Tone.amber, t('已暂停接管'), paused);
  if (network?['captive'] == 'captive') return Health(Tone.amber, t('需要网页认证'), t('当前网络需要先在浏览器中完成登录'));
  if (network?['captive'] == 'offline') return Health(Tone.amber, t('网络不通'), t('当前网络无法访问互联网'));
  if (s['tun_on'] == true) return Health(Tone.green, t('正在保护'), t('所有应用的流量经 Velum 分流'));
  return Health(Tone.gray, t('未连接'), t('打开开关后，所有应用的流量经 Velum 分流'));
}
