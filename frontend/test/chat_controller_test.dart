import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:aether_personal/chat_controller.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences_platform_interface/in_memory_shared_preferences_async.dart';
import 'package:shared_preferences_platform_interface/shared_preferences_async_platform_interface.dart';

const firstPosition = '01900000-0000-7000-8000-000000000001';
const livePosition = '01900000-0000-7000-8000-000000000003';
const echoPosition = '01900000-0000-7000-8000-000000000004';
const replyPosition = '01900000-0000-7000-8000-000000000005';
const offlinePosition = '01900000-0000-7000-8000-000000000006';

Map<String, dynamic> row(
  String id,
  String position,
  String text, {
  String sender = 'main',
  String? clientId,
}) => {
  'id': id,
  'position': position,
  'text': text,
  'sender': sender,
  'client_message_id': ?clientId,
  'created_at': '2026-10-22T10:00:00Z',
};

Future<void> eventually(bool Function() condition) async {
  final limit = DateTime.now().add(const Duration(seconds: 6));
  while (!condition()) {
    if (DateTime.now().isAfter(limit)) fail('Condition was not reached');
    await Future<void>.delayed(const Duration(milliseconds: 10));
  }
}

void main() {
  late HttpServer server;
  late ChatController controller;
  final sockets = <WebSocket>[];
  final authFrames = <Map<String, dynamic>>[];
  final sentFrames = <Map<String, dynamic>>[];
  final requests = <String>[];
  Completer<void>? releaseHistory;
  var unauthorized = false;

  setUp(() async {
    SharedPreferencesAsyncPlatform.instance =
        InMemorySharedPreferencesAsync.empty();
    sockets.clear();
    authFrames.clear();
    sentFrames.clear();
    requests.clear();
    releaseHistory = null;
    unauthorized = false;
    server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    server.listen((request) async {
      requests.add(request.uri.path);
      if (request.uri.path == '/ws') {
        final socket = await WebSocketTransformer.upgrade(request);
        sockets.add(socket);
        socket.listen((data) {
          final frame = jsonDecode(data as String) as Map<String, dynamic>;
          if (frame['type'] == 'auth') {
            authFrames.add(frame);
            socket.add(
              jsonEncode({
                'type': 'messages',
                'messages': [],
                'read_up_to': null,
              }),
            );
            if (sockets.length > 1) {
              socket.add(
                jsonEncode({
                  'type': 'messages',
                  'messages': [
                    row('offline-reply', offlinePosition, '断线期间的回复'),
                  ],
                  'read_up_to': echoPosition,
                }),
              );
            }
          } else {
            sentFrames.add(frame);
          }
        });
        return;
      }
      request.response.headers.contentType = ContentType.json;
      switch (request.uri.path) {
        case '/auth/verify':
          request.response.write(jsonEncode({'token': 'local-test-session'}));
        case '/auth/code':
          request.response.statusCode = 429;
          request.response.write('please wait before requesting another code');
        case '/messages':
          expect(
            request.headers.value('Authorization'),
            'Bearer local-test-session',
          );
          if (releaseHistory != null) await releaseHistory!.future;
          if (unauthorized) {
            request.response.statusCode = 401;
            request.response.write('unauthorized');
          } else {
            request.response.write(
              jsonEncode({
                'messages': request.uri.queryParameters.containsKey('before')
                    ? []
                    : [row('history-reply', firstPosition, '历史消息')],
                'read_up_to': null,
              }),
            );
          }
        case '/auth/logout':
          request.response.statusCode = 204;
        default:
          request.response.statusCode = 404;
      }
      await request.response.close();
    });
    controller = ChatController(
      baseUri: Uri.parse('http://127.0.0.1:${server.port}'),
    );
    await controller.initialize();
  });

  tearDown(() async {
    controller.dispose();
    for (final socket in sockets) {
      await socket.close();
    }
    await server.close(force: true);
  });

  test(
    'live replies and delayed history merge without losing either',
    () async {
      releaseHistory = Completer<void>();
      await controller.signIn('me@example.com', '123456');
      await eventually(
        () => controller.connected && requests.contains('/messages'),
      );
      sockets.single.add(
        jsonEncode({
          'type': 'messages',
          'messages': [row('live-reply', livePosition, '即时回复')],
          'read_up_to': null,
        }),
      );
      await eventually(() => controller.messages.length == 1);
      releaseHistory!.complete();
      await eventually(() => controller.messages.length == 2);
      expect(controller.messages.map((message) => message.text), [
        '历史消息',
        '即时回复',
      ]);
      expect(requests.indexOf('/ws'), lessThan(requests.indexOf('/messages')));
    },
  );

  test(
    'ack, echo and read merge one user bubble; reconnect uses row position',
    () async {
      await controller.signIn('me@example.com', '123456');
      await eventually(
        () =>
            controller.connected &&
            !controller.loadingHistory &&
            controller.messages.isNotEmpty,
      );
      expect(controller.send('你好'), isTrue);
      await eventually(() => sentFrames.isNotEmpty);
      final clientId = sentFrames.single['client_message_id'] as String;
      final socket = sockets.single;
      socket.add(
        jsonEncode({'type': 'delivered', 'client_message_id': clientId}),
      );
      socket.add(
        jsonEncode({
          'type': 'messages',
          'messages': [
            row(
              'server-user-id',
              echoPosition,
              '你好',
              sender: 'user',
              clientId: clientId,
            ),
            row('tool-id-one', replyPosition, '第一条回复'),
            row('tool-id-two', replyPosition, '第二条回复'),
          ],
          'read_up_to': echoPosition,
        }),
      );
      await eventually(() => controller.messages.length == 4);
      expect(controller.messages.where((message) => message.isUser).length, 1);
      expect(
        controller.messages.firstWhere((message) => message.isUser).delivery,
        Delivery.read,
      );
      socket.add(
        jsonEncode({'type': 'delivered', 'client_message_id': clientId}),
      );
      await Future<void>.delayed(const Duration(milliseconds: 30));
      expect(
        controller.messages.firstWhere((message) => message.isUser).delivery,
        Delivery.read,
      );
      expect(
        controller.messages.map((message) => message.text).toList().sublist(2),
        ['第一条回复', '第二条回复'],
      );
      await socket.close();
      await eventually(
        () =>
            authFrames.length == 2 &&
            controller.messages.any((message) => message.id == 'offline-reply'),
      );
      expect(authFrames.last['last_position'], replyPosition);
      expect(authFrames.last['last_position'], isNot('tool-id-two'));
      await controller.loadHistory(older: true);
      expect(controller.hasMoreHistory, isFalse);
    },
  );

  test(
    'failed connection keeps a retryable message with the same client id',
    () async {
      await controller.signIn('me@example.com', '123456');
      await eventually(() => controller.connected);
      await sockets.single.close();
      await eventually(() => !controller.connected);
      controller.send('断网时发送');
      final failed = controller.messages.firstWhere(
        (message) => message.text == '断网时发送',
      );
      expect(failed.delivery, Delivery.failed);
      await eventually(() => controller.connected && sockets.length == 2);
      controller.retry(failed);
      await eventually(() => sentFrames.isNotEmpty);
      expect(sentFrames.single['client_message_id'], failed.clientId);
      expect(sentFrames.single['text'], failed.text);
    },
  );

  test('unauthorized history clears the saved login', () async {
    unauthorized = true;
    await controller.signIn('me@example.com', '123456');
    await eventually(() => !controller.signedIn);
    expect(controller.messages, isEmpty);
    expect(controller.error, contains('登录已过期'));
  });

  test(
    'a new controller restores its saved session and reloads server messages',
    () async {
      await controller.signIn('me@example.com', '123456');
      await eventually(
        () => controller.connected && controller.messages.isNotEmpty,
      );
      controller.dispose();
      controller = ChatController(
        baseUri: Uri.parse('http://127.0.0.1:${server.port}'),
      );
      await controller.initialize();
      await eventually(
        () =>
            controller.connected &&
            controller.messages.any((message) => message.id == 'history-reply'),
      );
      expect(controller.signedIn, isTrue);
      expect(controller.email, 'me@example.com');
      expect(authFrames.last['token'], 'local-test-session');
    },
  );

  test(
    'plain text API failure becomes a useful error rather than JSON decode',
    () async {
      await expectLater(
        controller.requestCode('me@example.com'),
        throwsA(
          isA<ApiError>().having(
            (error) => error.message,
            'message',
            contains('稍后'),
          ),
        ),
      );
    },
  );
}
