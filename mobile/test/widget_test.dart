import 'package:flutter_test/flutter_test.dart';
import 'package:velum/health.dart';

void main() {
  test('health', () {
    expect(health(null).tone, Tone.red);
    expect(health({'running': true, 'subscription': {'source': ''}}).title, '还没有订阅');
    final on = {'running': true, 'tun_on': true, 'subscription': {'source': 'https://x'}};
    expect(health(on).tone, Tone.green);
    expect(health({...on, 'network': {'paused': '原因'}}).tone, Tone.amber);
  });
}
