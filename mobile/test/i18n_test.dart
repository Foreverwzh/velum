// 检查界面文字都有英文：lib 里的中文字符串都经 t()，键都在 en.dart，占位符一致。
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:velum/en.dart';
import 'package:velum/i18n.dart';

final han = RegExp(r'[一-鿿]');

void main() {
  test('中文字符串都有英文', () {
    final used = <String>{};
    for (final f in Directory('lib').listSync(recursive: true).whereType<File>()) {
      if (!f.path.endsWith('.dart') || f.path.endsWith('en.dart') || f.path.endsWith('i18n.dart')) continue;
      for (final raw in f.readAsLinesSync()) {
        final line = raw.split(RegExp(r'(^|\s)//')).first;
        if (!han.hasMatch(line)) continue;
        final keys = RegExp(r"""\bt\((['"])(.*?)\1""").allMatches(line).map((m) => m.group(2)!).toList();
        var rest = line.replaceAll(RegExp(r"""\bt\((['"]).*?\1"""), '');
        expect(han.hasMatch(rest), isFalse, reason: '${f.path} 没有经 t()：$raw');
        used.addAll(keys);
      }
    }
    for (final k in used) {
      expect(en.containsKey(k), isTrue, reason: '缺少英文：$k');
    }
    for (final k in en.keys) {
      expect(used.contains(k), isTrue, reason: '英文对照里有已经不用的条目：$k');
      final vars = RegExp(r'\{\d+\}');
      expect(vars.allMatches(en[k]!).map((m) => m.group(0)).toSet(), vars.allMatches(k).map((m) => m.group(0)).toSet(), reason: k);
      expect(han.hasMatch(en[k]!), isFalse, reason: en[k]);
    }
  });

  test('t 按语言翻译', () {
    setLanguage('en');
    expect(t('{0} 个应用不经 Velum', [3]), '3 apps skip Velum');
    expect(t('没有译文'), '没有译文');
    setLanguage('zh');
    expect(t('{0} 个应用不经 Velum', [3]), '3 个应用不经 Velum');
  });
}
