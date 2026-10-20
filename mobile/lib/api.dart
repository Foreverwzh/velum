// Velum 核心本地接口的客户端：HTTP over UDS，与桌面端 desktop/src/api.ts、core/pkg/rpc 保持一致。
// 核心运行在 App 进程内（Kotlin 的 Core 启动），socket 路径由原生层提供。

import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/services.dart';

typedef Json = Map<String, dynamic>;

class ApiError implements Exception {
  ApiError(this.message, [this.status]);
  final String message;
  final int? status;
  @override
  String toString() => message;
}

/// 原生层（Kotlin）提供的平台操作。
class Native {
  static const _ch = MethodChannel('velum/native');

  /// 启动核心（已启动时无操作），返回 socket 路径、Velum 目录、是否在运行与启动失败原因。
  static Future<({String socket, String home, bool running, String? error})> core() async {
    final m = (await _ch.invokeMapMethod<String, dynamic>('core'))!;
    return (socket: m['socket'] as String, home: m['home'] as String, running: m['running'] as bool, error: m['error'] as String?);
  }

  /// 已安装的应用（不含 Velum 自身）。
  static Future<List<AppInfo>> apps() async {
    final list = (await _ch.invokeListMethod<Map<Object?, Object?>>('apps')) ?? [];
    return [
      for (final m in list)
        AppInfo(m['package'] as String, m['label'] as String, m['system'] as bool),
    ];
  }

  static final _icons = <String, Future<Uint8List?>>{};

  /// 应用图标（PNG），结果缓存。
  static Future<Uint8List?> appIcon(String pkg) => _icons[pkg] ??= _ch.invokeMethod<Uint8List>('appIcon', pkg);

  /// 申请 VPN 授权（首次弹出系统确认框），返回是否已授权。
  static Future<bool> prepareVpn() async => (await _ch.invokeMethod<bool>('prepareVpn')) ?? false;

  /// Android 13+：申请通知权限（前台服务通知）。
  static Future<void> requestNotifications() => _ch.invokeMethod('requestNotifications');
}

class AppInfo {
  const AppInfo(this.package, this.label, this.system);
  final String package;
  final String label;
  final bool system;
}

class Api {
  Api(this.socket);

  final String socket;
  late final HttpClient _http = HttpClient()
    ..connectionTimeout = const Duration(seconds: 3)
    ..connectionFactory = (uri, proxyHost, proxyPort) =>
        Socket.startConnect(InternetAddress(socket, type: InternetAddressType.unix), 0);

  Future<T> _call<T>(String method, String path, [Object? body, Duration timeout = const Duration(seconds: 60)]) async {
    final req = await _http.openUrl(method, Uri.parse('http://velum$path'));
    if (body != null) {
      req.headers.contentType = ContentType.json;
      req.write(jsonEncode(body));
    }
    final res = await req.close().timeout(timeout);
    final text = await res.transform(utf8.decoder).join().timeout(timeout);
    final data = text.isEmpty ? null : jsonDecode(text);
    if (res.statusCode >= 400) {
      throw ApiError((data is Map ? data['error'] as String? : null) ?? 'HTTP ${res.statusCode}', res.statusCode);
    }
    return data as T;
  }

  Future<Json> status() => _call('GET', '/v1/status');
  Future<Json> reload() => _call('POST', '/v1/reload');
  Future<Json> update() => _call('POST', '/v1/update');

  /// 运行时切换开关（保存到 state.json，不写 config.yaml）：on / off / config。
  Future<Json> setSwitch(String which, String value) => _call('POST', '/v1/switches', {which: value});

  Future<Json> why(String target, {bool noResolve = false}) =>
      _call('POST', '/v1/why', {'target': target, 'no_resolve': noResolve});
  Future<List<Json>> connections({int limit = 200, String host = ''}) async {
    final r = await _call<Json>('GET', '/v1/connections?limit=$limit${host.isEmpty ? '' : '&host=${Uri.encodeQueryComponent(host)}'}');
    return ((r['connections'] as List?) ?? []).cast<Json>();
  }

  Future<List<Json>> warnings() async => (((await _call<Json>('GET', '/v1/warnings'))['warnings'] as List?) ?? []).cast<Json>();
  Future<Json> select(String group, String member) => _call('POST', '/v1/groups/select', {'group': group, 'member': member});
  Future<Json> test(String group) => _call('POST', '/v1/groups/test', {'group': group});
  Future<Json> overlay() => _call('GET', '/v1/overlay');
  Future<Json> saveOverlay(String content) => _call('PUT', '/v1/overlay', {'content': content});
  Future<Json> subscription() => _call('GET', '/v1/subscription');

  /// 按应用分流（写入 config.yaml 的 tun 节）。
  Future<({List<String> include, List<String> exclude})> tunApps() async {
    final r = await _call<Json>('GET', '/v1/tun/apps');
    return (include: ((r['include_apps'] as List?) ?? []).cast<String>(), exclude: ((r['exclude_apps'] as List?) ?? []).cast<String>());
  }

  Future<Json> setTunApps({List<String> include = const [], List<String> exclude = const []}) =>
      _call('PUT', '/v1/tun/apps', {'include_apps': include, 'exclude_apps': exclude});

  /// 先拉取并校验，通过后才写入 config.yaml；空字符串表示清除订阅。
  Future<Json> setSubscription(String url) => _call('PUT', '/v1/subscription', {'url': url}, const Duration(minutes: 2));
  Future<Json> repair() => _call('POST', '/v1/repair', null, const Duration(minutes: 3));

  /// 连接结束事件（SSE）。取消订阅即断开。
  Stream<Json> connectionStream() async* {
    final req = await _http.openUrl('GET', Uri.parse('http://velum/v1/connections/stream'));
    final res = await req.close();
    await for (final line in res.transform(utf8.decoder).transform(const LineSplitter())) {
      if (line.startsWith('data:')) yield jsonDecode(line.substring(5).trim()) as Json;
    }
  }

  void close() => _http.close(force: true);
}
