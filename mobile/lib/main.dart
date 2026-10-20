import 'dart:async';

import 'package:flutter/material.dart';

import 'api.dart';
import 'pages/connections.dart';
import 'pages/diagnose.dart';
import 'pages/home.dart';
import 'pages/settings.dart';
import 'state.dart';

void main() => runApp(const VelumApp());

class VelumApp extends StatelessWidget {
  const VelumApp({super.key});

  @override
  Widget build(BuildContext context) {
    const seed = Colors.indigo;
    return MaterialApp(
      title: 'Velum',
      theme: ThemeData(colorSchemeSeed: seed, useMaterial3: true),
      darkTheme: ThemeData(colorSchemeSeed: seed, brightness: Brightness.dark, useMaterial3: true),
      home: const Boot(),
    );
  }
}

/// 等待核心就绪（核心由原生层在后台线程启动），然后进入主界面。
class Boot extends StatefulWidget {
  const Boot({super.key});

  @override
  State<Boot> createState() => _BootState();
}

class _BootState extends State<Boot> {
  VelumState? _state;
  String? _error;

  @override
  void initState() {
    super.initState();
    _wait();
  }

  Future<void> _wait() async {
    for (;;) {
      final c = await Native.core();
      if (c.running) {
        setState(() => _state = VelumState(Api(c.socket), c.home));
        return;
      }
      if (c.error != null) setState(() => _error = c.error);
      await Future<void>.delayed(const Duration(milliseconds: 300));
    }
  }

  @override
  void dispose() {
    _state?.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final st = _state;
    if (st != null) return Velum(state: st, child: const Shell());
    return Scaffold(
      body: Center(
        child: Padding(
          padding: const EdgeInsets.all(24),
          child: _error == null ? const CircularProgressIndicator() : Text('核心启动失败：$_error', textAlign: TextAlign.center),
        ),
      ),
    );
  }
}

/// 主框架：首页与设置；专业模式下另有连接、诊断。
class Shell extends StatefulWidget {
  const Shell({super.key});

  @override
  State<Shell> createState() => _ShellState();
}

class _ShellState extends State<Shell> {
  String _tab = 'home';

  @override
  Widget build(BuildContext context) {
    final pro = Velum.of(context).pro;
    final tabs = [
      (id: 'home', label: '首页', icon: Icons.shield_outlined, page: const HomePage()),
      if (pro) (id: 'conns', label: '连接', icon: Icons.swap_vert, page: const ConnectionsPage()),
      if (pro) (id: 'why', label: '诊断', icon: Icons.troubleshoot, page: const DiagnosePage()),
      (id: 'settings', label: '设置', icon: Icons.settings_outlined, page: const SettingsPage()),
    ];
    var index = tabs.indexWhere((t) => t.id == _tab);
    if (index < 0) index = 0;
    return Scaffold(
      appBar: AppBar(title: Text(index == 0 ? 'Velum' : tabs[index].label)),
      body: tabs[index].page,
      bottomNavigationBar: NavigationBar(
        selectedIndex: index,
        onDestinationSelected: (i) => setState(() => _tab = tabs[i].id),
        destinations: [for (final t in tabs) NavigationDestination(icon: Icon(t.icon), label: t.label)],
      ),
    );
  }
}
