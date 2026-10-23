import 'package:aether_personal/chat_controller.dart';
import 'package:aether_personal/main.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:url_launcher_platform_interface/url_launcher_platform_interface.dart';

class VisibleChat extends ChatController {
  VisibleChat({
    this.userText = '帮我看一下今天的安排',
    this.mainText = '可以，把你想安排的事情告诉我。',
    this.history,
  }) {
    initializing = false;
    connected = true;
    hasMoreHistory = false;
  }
  final String userText;
  final String mainText;
  final List<ChatMessage>? history;
  String? sent;
  String? retried;
  int historyLoads = 0;
  @override
  bool get signedIn => true;
  @override
  List<ChatMessage> get messages =>
      history ??
      [
        ChatMessage(
          id: 'user',
          text: userText,
          isUser: true,
          createdAt: DateTime(2026, 10, 22, 9),
          order: 0,
          delivery: Delivery.read,
        ),
        ChatMessage(
          id: 'main',
          text: mainText,
          isUser: false,
          createdAt: DateTime(2026, 10, 22, 9, 1),
          order: 1,
        ),
      ];
  @override
  bool send(String text) {
    sent = text;
    return true;
  }

  @override
  void retry(ChatMessage message) => retried = message.id;

  @override
  Future<void> loadHistory({bool older = false}) async {
    if (older) historyLoads++;
    hasMoreHistory = false;
  }
}

class RecordingLauncher extends UrlLauncherPlatform {
  final List<String> openedUrls = [];
  final List<LaunchOptions> launchOptions = [];

  @override
  get linkDelegate => null;

  @override
  Future<bool> launchUrl(String url, LaunchOptions options) async {
    openedUrls.add(url);
    launchOptions.add(options);
    return true;
  }
}

void main() {
  testWidgets('chat resizes with rich messages and keeps the draft usable', (
    tester,
  ) async {
    tester.view.devicePixelRatio = 1;
    tester.platformDispatcher.textScaleFactorTestValue = 1.3;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    addTearDown(tester.platformDispatcher.clearTextScaleFactorTestValue);
    final controller = VisibleChat(
      userText: '用户输入的很长一段文字。' * 8,
      mainText:
          '## 资料比较\n\n'
          '| 方案 | 优点 | 注意事项 |\n'
          '| --- | --- | --- |\n'
          '| 直接执行 | 简单任务响应更快 | 复杂任务可继续委派 |\n\n'
          'https://example.com/a/very/long/path/that/needs/to/wrap/on/a/small/screen\n\n'
          '```sh\nflutter build web --dart-define=API_BASE_URL=https://example.com/very/long/path\n```',
    );
    addTearDown(controller.dispose);
    tester.view.physicalSize = const Size(390, 844);
    await tester.pumpWidget(AetherApp(controller: controller));
    await tester.pumpAndSettle();
    await tester.enterText(find.byType(TextField), '保留草稿\n继续输入');
    for (final size in const [
      Size(320, 568),
      Size(390, 844),
      Size(844, 390),
      Size(768, 1024),
      Size(1440, 900),
    ]) {
      tester.view.physicalSize = size;
      await tester.pumpAndSettle();
      expect(tester.takeException(), isNull, reason: '$size');
      final field = tester.getRect(find.byType(TextField));
      final send = tester.getRect(find.byTooltip('发送消息'));
      expect(field.left, greaterThanOrEqualTo(0));
      expect(field.bottom, lessThanOrEqualTo(size.height));
      expect(send.right, lessThanOrEqualTo(size.width));
      expect(send.bottom, lessThanOrEqualTo(size.height));
      expect(find.byTooltip('发送消息').hitTestable(), findsOneWidget);
    }
    await tester.tap(find.byTooltip('发送消息'));
    expect(controller.sent, '保留草稿\n继续输入');
  });

  testWidgets('scrolling to older messages loads history without a button', (
    tester,
  ) async {
    tester.view.physicalSize = const Size(390, 844);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    final controller = VisibleChat(
      history: [
        for (var index = 0; index < 15; index++)
          ChatMessage(
            id: 'message-$index',
            text: '一段消息内容。' * 12,
            isUser: index.isEven,
            createdAt: DateTime(2026, 10, 23, 9, index),
            order: index,
          ),
      ],
    )..hasMoreHistory = true;
    addTearDown(controller.dispose);
    await tester.pumpWidget(AetherApp(controller: controller));
    await tester.pumpAndSettle();
    expect(controller.historyLoads, 0);
    await tester.drag(find.byType(ListView), const Offset(0, 5000));
    await tester.pumpAndSettle();
    expect(controller.historyLoads, 1);
    expect(find.text('加载更早的消息'), findsNothing);
    expect(tester.takeException(), isNull);
  });

  testWidgets('opening a long conversation shows the latest message', (
    tester,
  ) async {
    tester.view.physicalSize = const Size(390, 844);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    addTearDown(tester.view.resetViewInsets);
    final controller = VisibleChat(
      history: [
        for (var index = 0; index < 8; index++)
          ChatMessage(
            id: 'history-$index',
            text: '历史资料。' * 120,
            isUser: false,
            createdAt: DateTime(2026, 10, 23, 9, index),
            order: index,
          ),
        ChatMessage(
          id: 'latest',
          text: '最新的一条消息',
          isUser: false,
          createdAt: DateTime(2026, 10, 23, 9, 9),
          order: 8,
        ),
      ],
    );
    addTearDown(controller.dispose);
    await tester.pumpWidget(AetherApp(controller: controller));
    await tester.pumpAndSettle();
    expect(find.text('最新的一条消息').hitTestable(), findsOneWidget);
    for (final size in const [
      Size(1440, 900),
      Size(320, 568),
      Size(844, 390),
    ]) {
      tester.view.physicalSize = size;
      await tester.pumpAndSettle();
      expect(
        find.text('最新的一条消息').hitTestable(),
        findsOneWidget,
        reason: 'Keep the latest message visible after resizing to $size',
      );
    }
    tester.view.viewInsets = const FakeViewPadding(bottom: 220);
    await tester.pumpAndSettle();
    expect(find.text('最新的一条消息').hitTestable(), findsOneWidget);
    tester.view.viewInsets = FakeViewPadding.zero;
    await tester.pumpAndSettle();
    await tester.drag(find.byType(ListView), const Offset(0, 900));
    await tester.pumpAndSettle();
    tester.view.physicalSize = const Size(390, 844);
    await tester.pumpAndSettle();
    expect(find.text('最新的一条消息').hitTestable(), findsNothing);
    expect(tester.takeException(), isNull);
  });

  testWidgets(
    'latest receipt stays visible and older failed messages can retry',
    (tester) async {
      tester.view.physicalSize = const Size(390, 844);
      tester.view.devicePixelRatio = 1;
      addTearDown(tester.view.resetPhysicalSize);
      addTearDown(tester.view.resetDevicePixelRatio);
      final controller = VisibleChat(
        history: [
          ChatMessage(
            id: 'old',
            text: '前一条消息',
            isUser: true,
            createdAt: DateTime(2026, 10, 23, 9),
            order: 0,
            delivery: Delivery.read,
          ),
          ChatMessage(
            id: 'failed',
            text: '这条需要重发',
            isUser: true,
            createdAt: DateTime(2026, 10, 23, 9, 1),
            order: 1,
            delivery: Delivery.failed,
          ),
          ChatMessage(
            id: 'latest',
            text: '最新的消息',
            isUser: true,
            createdAt: DateTime(2026, 10, 23, 9, 2),
            order: 2,
            delivery: Delivery.delivered,
          ),
        ],
      );
      addTearDown(controller.dispose);
      await tester.pumpWidget(AetherApp(controller: controller));
      await tester.pumpAndSettle();
      expect(find.text('已送达'), findsOneWidget);
      expect(find.text('已读'), findsNothing);
      await tester.tap(find.text('发送失败，点击重发'));
      expect(controller.retried, 'failed');
      expect(tester.takeException(), isNull);
    },
  );

  testWidgets('small screen and open keyboard leave the composer usable', (
    tester,
  ) async {
    tester.view.physicalSize = const Size(320, 568);
    tester.view.devicePixelRatio = 1;
    tester.view.viewInsets = const FakeViewPadding(bottom: 260);
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    addTearDown(tester.view.resetViewInsets);
    final controller = VisibleChat();
    addTearDown(controller.dispose);
    await tester.pumpWidget(AetherApp(controller: controller));
    await tester.pumpAndSettle();
    await tester.enterText(find.byType(TextField), '第一行\n第二行');
    await tester.pump();
    await tester.tap(find.byTooltip('发送消息'));
    await tester.pump();
    expect(controller.sent, '第一行\n第二行');
    expect(tester.takeException(), isNull);
  });
  testWidgets('narrow chat displays only Main flow and sends the typed text', (
    tester,
  ) async {
    tester.view.physicalSize = const Size(390, 844);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    final controller = VisibleChat();
    addTearDown(controller.dispose);
    await tester.pumpWidget(AetherApp(controller: controller));
    await tester.pumpAndSettle();
    expect(find.text('Aether'), findsNothing);
    expect(find.byTooltip('会话菜单'), findsNothing);
    expect(find.text('你的私人助手'), findsNothing);
    expect(find.text('加载更早的消息'), findsNothing);
    expect(find.text('帮我看一下今天的安排'), findsOneWidget);
    expect(find.text('可以，把你想安排的事情告诉我。'), findsOneWidget);
    expect(find.textContaining('Subagent'), findsNothing);
    expect(find.textContaining('任务列表'), findsNothing);
    await tester.enterText(find.byType(TextField), '查一下 Flutter 官方文档');
    await tester.pump();
    await tester.tap(find.byTooltip('发送消息'));
    await tester.pump();
    expect(controller.sent, '查一下 Flutter 官方文档');
    expect(tester.takeException(), isNull);
  });

  testWidgets('Main renders selectable Markdown without changing user text', (
    tester,
  ) async {
    tester.view.physicalSize = const Size(390, 844);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    final controller = VisibleChat(
      userText: '**用户原文**',
      mainText:
          '**可读结论**\n\n- 第一点\n- 第二点\n\n'
          '```sh\nflutter build web --no-web-resources-cdn --dart-define=API_BASE_URL=https://example.com/very/long/path\n```',
    );
    addTearDown(controller.dispose);
    await tester.pumpWidget(AetherApp(controller: controller));
    await tester.pumpAndSettle();

    expect(find.text('**用户原文**'), findsOneWidget);
    expect(find.text('**可读结论**'), findsNothing);
    expect(find.text('可读结论'), findsOneWidget);
    expect(find.text('第一点'), findsOneWidget);
    expect(find.text('第二点'), findsOneWidget);
    expect(
      find.byWidgetPredicate(
        (widget) =>
            widget is SelectableText &&
            widget.textSpan?.toPlainText() == '可读结论',
      ),
      findsOneWidget,
    );
    expect(tester.takeException(), isNull);
  });

  testWidgets('sources open HTTP links and ignore other schemes and images', (
    tester,
  ) async {
    final previousLauncher = UrlLauncherPlatform.instance;
    final launcher = RecordingLauncher();
    UrlLauncherPlatform.instance = launcher;
    addTearDown(() => UrlLauncherPlatform.instance = previousLauncher);
    final controller = VisibleChat(
      mainText:
          '[官方文档](https://docs.flutter.dev/platform-integration/web/setup)\n\n'
          'https://flutter.dev\n\n'
          '来源：https://docs.flutter.dev/deployment/web\n\n'
          '[本地文件](file:///tmp/example)\n\n'
          '![示例图片](https://example.com/remote.png)',
    );
    addTearDown(controller.dispose);
    await tester.pumpWidget(AetherApp(controller: controller));
    await tester.pumpAndSettle();

    expect(find.text('示例图片'), findsOneWidget);
    expect(find.byType(Image), findsNothing);
    await tester.tap(find.text('官方文档'));
    await tester.pump();
    await tester.tap(find.text('https://flutter.dev'));
    await tester.pump();
    final sourceText = tester
        .state<EditableTextState>(
          find.text('来源：https://docs.flutter.dev/deployment/web'),
        )
        .renderEditable;
    final sourceLinkPosition = sourceText.getLocalRectForCaret(
      const TextPosition(offset: 15),
    );
    await tester.tapAt(sourceText.localToGlobal(sourceLinkPosition.center));
    await tester.pump();
    await tester.tap(find.text('本地文件'));
    await tester.pump();
    expect(launcher.openedUrls, [
      'https://docs.flutter.dev/platform-integration/web/setup',
      'https://flutter.dev',
      'https://docs.flutter.dev/deployment/web',
    ]);
    expect(
      launcher.launchOptions.map((options) => options.webOnlyWindowName),
      everyElement('_blank'),
    );
    expect(tester.takeException(), isNull);
  });
}
