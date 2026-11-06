// 界面语言。源文字就是中文，同时作为查找英文的键（与核心、桌面端相同）；完整性由 test/i18n_test.dart 保证。
// 语言跟随系统，在启动时确定。

import 'dart:ui';

import 'en.dart';

bool _english = !PlatformDispatcher.instance.locale.languageCode.startsWith('zh');

/// 当前语言：zh 或 en。
String get language => _english ? 'en' : 'zh';

/// 测试用。
void setLanguage(String l) => _english = !l.startsWith('zh');

/// 翻译 src；{0}、{1} 依次替换为 args。
String t(String src, [List<Object?> args = const []]) {
  var s = _english ? (en[src] ?? src) : src;
  for (var i = 0; i < args.length; i++) {
    s = s.replaceAll('{$i}', '${args[i]}');
  }
  return s;
}
