// 界面共享状态：核心状态（定时轮询）与界面偏好。

import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/material.dart';

import 'api.dart';

class VelumState extends ChangeNotifier {
  VelumState(this.api, this.home) {
    _loadPrefs();
    refresh();
    _timer = Timer.periodic(const Duration(seconds: 2), (_) => refresh());
  }

  final Api api;
  final String home;
  Timer? _timer;

  Json? status;
  String? error;

  /// 专业模式：显示连接、诊断等页面（只影响界面，保存在 Velum 目录的 ui.json）。
  bool pro = false;

  File get _prefs => File('$home/ui.json');

  void _loadPrefs() {
    try {
      pro = (jsonDecode(_prefs.readAsStringSync()) as Json)['pro'] == true;
    } catch (_) {}
  }

  void setPro(bool v) {
    pro = v;
    notifyListeners();
    try {
      _prefs.writeAsStringSync(jsonEncode({'pro': v}));
    } catch (_) {}
  }

  Future<void> refresh() async {
    try {
      status = await api.status();
      error = null;
    } catch (e) {
      status = null;
      error = e.toString();
    }
    notifyListeners();
  }

  /// 执行一个改动核心状态的操作，完成后立即刷新。
  Future<T> run<T>(Future<T> Function() fn) async {
    try {
      return await fn();
    } finally {
      await refresh();
    }
  }

  @override
  void dispose() {
    _timer?.cancel();
    api.close();
    super.dispose();
  }
}

/// 向下提供 VelumState，并在其变化时重建依赖它的部件。
class Velum extends InheritedNotifier<VelumState> {
  const Velum({super.key, required VelumState state, required super.child}) : super(notifier: state);

  static VelumState of(BuildContext context) => context.dependOnInheritedWidgetOfExactType<Velum>()!.notifier!;

  /// 只取引用、不订阅变化（用于回调中）。
  static VelumState read(BuildContext context) => context.getInheritedWidgetOfExactType<Velum>()!.notifier!;
}

String errText(Object e) => e is ApiError ? e.message : e.toString();

void toast(BuildContext context, String msg) =>
    ScaffoldMessenger.of(context)
      ..hideCurrentSnackBar()
      ..showSnackBar(SnackBar(content: Text(msg)));
