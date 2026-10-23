import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart' show ScrollDirection;
import 'package:flutter/semantics.dart';
import 'package:flutter/services.dart';
import 'package:flutter_markdown_plus/flutter_markdown_plus.dart';
import 'package:markdown/markdown.dart' as md;
import 'package:url_launcher/url_launcher.dart';

import 'chat_controller.dart';

void main() {
  WidgetsFlutterBinding.ensureInitialized();
  runApp(const AetherApp());
}

const _accent = Color(0xFF536E89);
const _ink = Color(0xFF1D1D1F);
const _muted = Color(0xFF85858B);

class _ChineseSourceLinkSyntax extends md.AutolinkExtensionSyntax {
  @override
  bool tryMatch(md.InlineParser parser, [int? startMatchPos]) {
    if (parser.pos > 0 && parser.source[parser.pos - 1] == '：') {
      final match = pattern.matchAsPrefix(
        parser.source,
        startMatchPos ?? parser.pos,
      );
      if (match != null && match[1] != null) {
        parser.writeText();
        return onMatch(parser, match);
      }
    }
    return super.tryMatch(parser, startMatchPos);
  }
}

class AetherApp extends StatefulWidget {
  const AetherApp({super.key, this.controller});
  final ChatController? controller;

  @override
  State<AetherApp> createState() => _AetherAppState();
}

class _AetherAppState extends State<AetherApp> {
  late final ChatController controller;
  SemanticsHandle? _webSemantics;

  @override
  void initState() {
    super.initState();
    if (kIsWeb) _webSemantics = SemanticsBinding.instance.ensureSemantics();
    controller = widget.controller ?? ChatController();
    if (widget.controller == null) unawaited(controller.initialize());
  }

  @override
  void dispose() {
    _webSemantics?.dispose();
    if (widget.controller == null) controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => MaterialApp(
    title: 'Aether',
    debugShowCheckedModeBanner: false,
    theme: ThemeData(
      colorScheme: ColorScheme.fromSeed(
        seedColor: _accent,
        surface: Colors.white,
      ),
      useMaterial3: true,
      fontFamily: 'NotoSansSC',
      fontFamilyFallback: const [
        'PingFang SC',
        'Microsoft YaHei',
        'Noto Sans CJK SC',
      ],
      scaffoldBackgroundColor: const Color(0xFFF5F5F7),
      textTheme: const TextTheme(
        bodyMedium: TextStyle(color: _ink, fontSize: 15),
      ),
      inputDecorationTheme: InputDecorationTheme(
        filled: true,
        fillColor: const Color(0xFFF3F3F5),
        border: OutlineInputBorder(
          borderRadius: BorderRadius.circular(14),
          borderSide: BorderSide.none,
        ),
        contentPadding: const EdgeInsets.symmetric(
          horizontal: 18,
          vertical: 18,
        ),
      ),
    ),
    home: ListenableBuilder(
      listenable: controller,
      builder: (context, _) {
        if (controller.initializing) {
          return const Scaffold(
            body: Center(child: CircularProgressIndicator(color: _accent)),
          );
        }
        return controller.signedIn
            ? ChatPage(controller: controller)
            : LoginPage(controller: controller);
      },
    ),
  );
}

class LoginPage extends StatefulWidget {
  const LoginPage({super.key, required this.controller});
  final ChatController controller;
  @override
  State<LoginPage> createState() => _LoginPageState();
}

class _LoginPageState extends State<LoginPage> {
  final email = TextEditingController();
  final code = TextEditingController();
  bool sent = false;
  bool busy = false;
  String? error;

  @override
  void initState() {
    super.initState();
    email.text = widget.controller.email;
  }

  @override
  void dispose() {
    email.dispose();
    code.dispose();
    super.dispose();
  }

  Future<void> submit() async {
    if (busy) return;
    final address = email.text.trim();
    if (!address.contains('@') || !address.contains('.')) {
      setState(() => error = '请输入有效的邮箱地址');
      return;
    }
    if (sent && !RegExp(r'^\d{6}$').hasMatch(code.text.trim())) {
      setState(() => error = '请输入 6 位验证码');
      return;
    }
    setState(() {
      busy = true;
      error = null;
    });
    try {
      if (sent) {
        await widget.controller.signIn(address, code.text);
      } else {
        await widget.controller.requestCode(address);
        if (mounted) setState(() => sent = true);
      }
    } catch (failure) {
      if (mounted) {
        setState(
          () => error = failure is ApiError ? failure.message : '连接失败，请检查网络后重试',
        );
      }
    } finally {
      if (mounted) setState(() => busy = false);
    }
  }

  @override
  Widget build(BuildContext context) => Scaffold(
    body: SafeArea(
      child: Center(
        child: SingleChildScrollView(
          padding: const EdgeInsets.all(24),
          child: ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: 430),
            child: Container(
              padding: const EdgeInsets.all(36),
              decoration: BoxDecoration(
                color: Colors.white,
                borderRadius: BorderRadius.circular(28),
                boxShadow: const [
                  BoxShadow(
                    color: Color(0x0B18382F),
                    blurRadius: 40,
                    offset: Offset(0, 14),
                  ),
                ],
              ),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  const _AetherMark(size: 56),
                  const SizedBox(height: 28),
                  const Text(
                    '和 Aether 聊聊',
                    style: TextStyle(
                      fontSize: 28,
                      fontWeight: FontWeight.w600,
                      color: _ink,
                      letterSpacing: -0.8,
                    ),
                  ),
                  const SizedBox(height: 10),
                  Text(
                    sent ? '输入验证码，继续你的对话。' : '用邮箱登录，开启你们的对话。',
                    style: const TextStyle(color: _muted, height: 1.6),
                  ),
                  const SizedBox(height: 30),
                  TextField(
                    controller: email,
                    readOnly: sent,
                    keyboardType: TextInputType.emailAddress,
                    autofillHints: const [AutofillHints.email],
                    textInputAction: sent
                        ? TextInputAction.next
                        : TextInputAction.done,
                    decoration: const InputDecoration(
                      labelText: '邮箱地址',
                      hintText: 'you@example.com',
                    ),
                    onSubmitted: (_) {
                      if (!sent) submit();
                    },
                  ),
                  if (sent) ...[
                    const SizedBox(height: 16),
                    TextField(
                      controller: code,
                      autofocus: true,
                      keyboardType: TextInputType.number,
                      autofillHints: const [AutofillHints.oneTimeCode],
                      maxLength: 6,
                      inputFormatters: [FilteringTextInputFormatter.digitsOnly],
                      decoration: const InputDecoration(
                        labelText: '验证码',
                        hintText: '6 位数字',
                        counterText: '',
                      ),
                      onSubmitted: (_) => submit(),
                    ),
                  ],
                  if (error != null || widget.controller.error != null) ...[
                    const SizedBox(height: 16),
                    Text(
                      error ?? widget.controller.error!,
                      style: const TextStyle(
                        color: Color(0xFFAC3D35),
                        fontSize: 13,
                        height: 1.5,
                      ),
                    ),
                  ],
                  const SizedBox(height: 24),
                  SizedBox(
                    width: double.infinity,
                    height: 50,
                    child: FilledButton(
                      onPressed: busy ? null : submit,
                      style: FilledButton.styleFrom(
                        backgroundColor: _accent,
                        foregroundColor: Colors.white,
                        shape: RoundedRectangleBorder(
                          borderRadius: BorderRadius.circular(14),
                        ),
                      ),
                      child: busy
                          ? const SizedBox(
                              width: 20,
                              height: 20,
                              child: CircularProgressIndicator(
                                strokeWidth: 2,
                                color: Colors.white,
                              ),
                            )
                          : Text(
                              sent ? '登录' : '获取验证码',
                              style: const TextStyle(
                                fontWeight: FontWeight.w600,
                              ),
                            ),
                    ),
                  ),
                  if (sent)
                    Align(
                      alignment: Alignment.center,
                      child: TextButton(
                        onPressed: busy
                            ? null
                            : () => setState(() {
                                sent = false;
                                error = null;
                                code.clear();
                              }),
                        child: const Text('更换邮箱或重新获取'),
                      ),
                    ),
                ],
              ),
            ),
          ),
        ),
      ),
    ),
  );
}

class ChatPage extends StatefulWidget {
  const ChatPage({super.key, required this.controller});
  final ChatController controller;
  @override
  State<ChatPage> createState() => _ChatPageState();
}

class _ChatPageState extends State<ChatPage> {
  final input = TextEditingController();
  final inputFocus = FocusNode();
  final scroll = ScrollController();
  int _count = 0;
  bool _wasLoadingHistory = false;

  @override
  void initState() {
    super.initState();
    widget.controller.addListener(changed);
    scroll.addListener(_loadOlderWhenNeeded);
    if (widget.controller.messages.isNotEmpty) _scrollToEnd();
  }

  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    if (scroll.hasClients && scroll.position.extentAfter < 160) {
      _scrollToEnd();
    }
  }

  void changed() {
    final controller = widget.controller;
    final nearBottom =
        !scroll.hasClients ||
        scroll.offset >= scroll.position.maxScrollExtent - 160;
    final countChanged = controller.messages.length != _count;
    if (countChanged && nearBottom && !_wasLoadingHistory) _scrollToEnd();
    if (!_wasLoadingHistory && controller.loadingHistory) {
      _wasLoadingHistory = true;
    }
    if (!controller.loadingHistory && _wasLoadingHistory) {
      if (_count == 0) _scrollToEnd();
      _wasLoadingHistory = false;
    }
    _count = controller.messages.length;
  }

  void _scrollToEnd() => WidgetsBinding.instance.addPostFrameCallback((
    _,
  ) async {
    // Lazy rows refine the estimated extent as long Markdown replies enter view.
    for (var attempt = 0; attempt < 4; attempt++) {
      if (!mounted || !scroll.hasClients) return;
      await scroll.animateTo(
        scroll.position.maxScrollExtent,
        duration: Duration(milliseconds: attempt == 0 ? 180 : 80),
        curve: Curves.easeOut,
      );
      if (!mounted || !scroll.hasClients || scroll.position.extentAfter < 1) {
        return;
      }
    }
  });

  void _loadOlderWhenNeeded() {
    if (scroll.position.userScrollDirection == ScrollDirection.forward &&
        scroll.offset < 160 &&
        widget.controller.hasMoreHistory &&
        !widget.controller.loadingHistory) {
      unawaited(older());
    }
  }

  Future<void> older() async {
    final oldHeight = scroll.hasClients ? scroll.position.maxScrollExtent : 0.0;
    final oldOffset = scroll.hasClients ? scroll.offset : 0.0;
    await widget.controller.loadHistory(older: true);
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (mounted && scroll.hasClients) {
        scroll.jumpTo(
          (oldOffset + scroll.position.maxScrollExtent - oldHeight).clamp(
            0,
            scroll.position.maxScrollExtent,
          ),
        );
      }
    });
  }

  void send() {
    if (widget.controller.send(input.text)) {
      input.clear();
      inputFocus.requestFocus();
      _scrollToEnd();
    } else if (input.text.runes.length > 32000) {
      ScaffoldMessenger.of(context)
          .showSnackBar(const SnackBar(content: Text('消息不能超过 32,000 个字符')));
    }
  }

  @override
  void dispose() {
    widget.controller.removeListener(changed);
    input.dispose();
    inputFocus.dispose();
    scroll.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final controller = widget.controller;
    final messages = controller.messages;
    final narrow = MediaQuery.of(context).size.width < 700;
    final lastUserIndex = messages.lastIndexWhere((message) => message.isUser);
    return Scaffold(
      backgroundColor: Colors.white,
      body: SafeArea(
        child: Column(
          children: [
            Expanded(
              child: ListView.builder(
                controller: scroll,
                padding: EdgeInsets.fromLTRB(
                  narrow ? 16 : 32,
                  16,
                  narrow ? 16 : 32,
                  22,
                ),
                itemCount: messages.length,
                itemBuilder: (context, index) {
                  final message = messages[index];
                  final previous = index > 0 ? messages[index - 1] : null;
                  final next = index + 1 < messages.length
                      ? messages[index + 1]
                      : null;
                  final showDate =
                      previous == null ||
                      previous.createdAt.year != message.createdAt.year ||
                      previous.createdAt.month != message.createdAt.month ||
                      previous.createdAt.day != message.createdAt.day ||
                      message.createdAt.difference(previous.createdAt).abs() >=
                          const Duration(minutes: 30);
                  return Column(
                    children: [
                      if (showDate)
                        Padding(
                          padding: const EdgeInsets.only(top: 12, bottom: 24),
                          child: Text(
                            '${_dateLabel(message.createdAt)}  ${_messageTime(message.createdAt)}',
                            style: const TextStyle(fontSize: 11, color: _muted),
                          ),
                        ),
                      _MessageBubble(
                        message: message,
                        retry: () => controller.retry(message),
                        joinedPrevious:
                            previous != null &&
                            _sameMessageGroup(previous, message),
                        joinedNext:
                            next != null && _sameMessageGroup(message, next),
                        showDelivery: index == lastUserIndex,
                      ),
                    ],
                  );
                },
              ),
            ),
            Padding(
              padding: EdgeInsets.fromLTRB(
                narrow ? 14 : 26,
                10,
                narrow ? 14 : 26,
                narrow ? 14 : 20,
              ),
              child: Container(
                padding: const EdgeInsets.fromLTRB(4, 3, 5, 3),
                decoration: BoxDecoration(
                  color: Colors.white,
                  border: Border.all(color: const Color(0xFFD9D9DE)),
                  borderRadius: BorderRadius.circular(25),
                ),
                child: Row(
                  crossAxisAlignment: CrossAxisAlignment.end,
                  children: [
                    Expanded(
                      child: Focus(
                        onKeyEvent: (node, event) {
                          if (event is KeyDownEvent &&
                              event.logicalKey == LogicalKeyboardKey.enter &&
                              !HardwareKeyboard.instance.isShiftPressed &&
                              !HardwareKeyboard.instance.isControlPressed &&
                              !HardwareKeyboard.instance.isMetaPressed) {
                            if (input.value.composing.isValid &&
                                !input.value.composing.isCollapsed) {
                              return KeyEventResult.ignored;
                            }
                            send();
                            return KeyEventResult.handled;
                          }
                          return KeyEventResult.ignored;
                        },
                        child: TextField(
                          controller: input,
                          focusNode: inputFocus,
                          minLines: 1,
                          maxLines: 6,
                          keyboardType: TextInputType.multiline,
                          decoration: const InputDecoration(
                            hintText: '发消息给 Aether',
                            hintStyle: TextStyle(
                              color: Color(0xFFAEAEB4),
                              fontWeight: FontWeight.w400,
                            ),
                            labelText: '消息',
                            floatingLabelBehavior: FloatingLabelBehavior.never,
                            filled: false,
                            border: InputBorder.none,
                            enabledBorder: InputBorder.none,
                            focusedBorder: InputBorder.none,
                            contentPadding: EdgeInsets.symmetric(
                              horizontal: 14,
                              vertical: 10,
                            ),
                          ),
                          style: const TextStyle(
                            fontSize: 16,
                            color: _ink,
                            height: 1.4,
                          ),
                        ),
                      ),
                    ),
                    ValueListenableBuilder<TextEditingValue>(
                      valueListenable: input,
                      builder: (context, value, _) => SizedBox(
                        width: 42,
                        height: 42,
                        child: IconButton.filled(
                          tooltip: '发送消息',
                          onPressed: value.text.trim().isEmpty ? null : send,
                          style: IconButton.styleFrom(
                            padding: const EdgeInsets.all(7),
                            backgroundColor: _accent,
                            disabledBackgroundColor: const Color(0xFFE5E5EA),
                            foregroundColor: Colors.white,
                            disabledForegroundColor: const Color(0xFFAEAEB4),
                            shape: const CircleBorder(),
                          ),
                          icon: const Icon(
                            Icons.arrow_upward_rounded,
                            size: 22,
                          ),
                        ),
                      ),
                    ),
                  ],
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }
}

class _MessageBubble extends StatelessWidget {
  const _MessageBubble({
    required this.message,
    required this.retry,
    required this.joinedPrevious,
    required this.joinedNext,
    required this.showDelivery,
  });
  final ChatMessage message;
  final VoidCallback retry;
  final bool joinedPrevious;
  final bool joinedNext;
  final bool showDelivery;

  @override
  Widget build(BuildContext context) => LayoutBuilder(
    builder: (context, constraints) {
      final showStatus =
          message.isUser &&
          (showDelivery ||
              message.delivery == Delivery.failed ||
              message.delivery == Delivery.sending);
      return Align(
        alignment: message.isUser
            ? Alignment.centerRight
            : Alignment.centerLeft,
        child: Padding(
          padding: EdgeInsets.only(top: joinedPrevious ? 3 : 12),
          child: ConstrainedBox(
            constraints: BoxConstraints(
              maxWidth: (constraints.maxWidth * 0.84).clamp(0, 720).toDouble(),
            ),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              crossAxisAlignment: message.isUser
                  ? CrossAxisAlignment.end
                  : CrossAxisAlignment.start,
              children: [
                Container(
                  padding: const EdgeInsets.symmetric(
                    horizontal: 16,
                    vertical: 10,
                  ),
                  decoration: BoxDecoration(
                    color: message.isUser
                        ? const Color(0xFF64798D)
                        : const Color(0xFFE9E9EB),
                    borderRadius: BorderRadius.only(
                      topLeft: Radius.circular(
                        !message.isUser && joinedPrevious ? 8 : 21,
                      ),
                      topRight: Radius.circular(
                        message.isUser && joinedPrevious ? 8 : 21,
                      ),
                      bottomLeft: Radius.circular(
                        !message.isUser && !joinedNext ? 6 : 21,
                      ),
                      bottomRight: Radius.circular(
                        message.isUser && !joinedNext ? 6 : 21,
                      ),
                    ),
                  ),
                  child: message.isUser
                      ? SelectableText(
                          message.text,
                          semanticsLabel: '你：${message.text}',
                          style: const TextStyle(
                            fontSize: 16,
                            height: 1.5,
                            color: Colors.white,
                          ),
                        )
                      : Semantics(
                          label: 'Aether',
                          container: true,
                          child: MarkdownBody(
                            data: message.text,
                            inlineSyntaxes: [_ChineseSourceLinkSyntax()],
                            selectable: true,
                            softLineBreak: true,
                            styleSheet:
                                MarkdownStyleSheet.fromTheme(Theme.of(context))
                                    .copyWith(
                                      p: const TextStyle(
                                        fontSize: 16,
                                        height: 1.5,
                                        color: _ink,
                                      ),
                                      a: const TextStyle(
                                        color: _accent,
                                        decoration: TextDecoration.underline,
                                      ),
                                      code: const TextStyle(
                                        fontSize: 13,
                                        height: 1.5,
                                        fontFamily: 'monospace',
                                        fontFamilyFallback: ['NotoSansSC'],
                                        color: _ink,
                                        backgroundColor: Color(0xFFDDDDDF),
                                      ),
                                      blockSpacing: 8,
                                      listIndent: 21,
                                      h1: const TextStyle(
                                        fontSize: 20,
                                        height: 1.5,
                                        color: _ink,
                                        fontWeight: FontWeight.w600,
                                      ),
                                      h2: const TextStyle(
                                        fontSize: 18,
                                        height: 1.5,
                                        color: _ink,
                                        fontWeight: FontWeight.w600,
                                      ),
                                      h3: const TextStyle(
                                        fontSize: 16,
                                        height: 1.5,
                                        color: _ink,
                                        fontWeight: FontWeight.w600,
                                      ),
                                      codeblockDecoration: BoxDecoration(
                                        color: const Color(0xFFDDDDDF),
                                        borderRadius: BorderRadius.circular(8),
                                      ),
                                    ),
                            imageBuilder: (_, _, alt) => Text(
                              alt?.trim().isNotEmpty == true ? alt! : '图片链接',
                              style: const TextStyle(color: _muted),
                            ),
                            onTapLink: (_, href, _) async {
                              final uri = href == null
                                  ? null
                                  : Uri.tryParse(href);
                              if (uri == null ||
                                  (uri.scheme != 'http' &&
                                      uri.scheme != 'https') ||
                                  uri.host.isEmpty ||
                                  uri.userInfo.isNotEmpty) {
                                return;
                              }
                              try {
                                if (await launchUrl(
                                  uri,
                                  webOnlyWindowName: '_blank',
                                )) {
                                  return;
                                }
                              } catch (_) {
                                // The source stays selectable if the browser cannot open it.
                              }
                              if (context.mounted) {
                                ScaffoldMessenger.of(context).showSnackBar(
                                  const SnackBar(
                                    content: Text('链接暂时无法打开，可复制后查看'),
                                  ),
                                );
                              }
                            },
                          ),
                        ),
                ),
                if (showStatus)
                  if (message.delivery == Delivery.failed)
                    TextButton.icon(
                      onPressed: retry,
                      icon: const Icon(Icons.error_outline_rounded, size: 14),
                      label: const Text('发送失败，点击重发'),
                      style: TextButton.styleFrom(
                        foregroundColor: const Color(0xFFB33D38),
                        textStyle: const TextStyle(fontSize: 11),
                      ),
                    )
                  else
                    Padding(
                      padding: const EdgeInsets.only(top: 5, right: 5),
                      child: Text(switch (message.delivery) {
                        Delivery.sending => '发送中…',
                        Delivery.delivered => '已送达',
                        Delivery.read => '已读',
                        Delivery.failed => '发送失败',
                      }, style: const TextStyle(fontSize: 11, color: _muted)),
                    ),
              ],
            ),
          ),
        ),
      );
    },
  );
}

bool _sameMessageGroup(ChatMessage first, ChatMessage second) =>
    first.isUser == second.isUser &&
    first.createdAt.year == second.createdAt.year &&
    first.createdAt.month == second.createdAt.month &&
    first.createdAt.day == second.createdAt.day &&
    second.createdAt.difference(first.createdAt).abs() <
        const Duration(minutes: 2);

String _messageTime(DateTime date) =>
    '${date.hour.toString().padLeft(2, '0')}:${date.minute.toString().padLeft(2, '0')}';

class _AetherMark extends StatelessWidget {
  const _AetherMark({required this.size});
  final double size;
  @override
  Widget build(BuildContext context) => Container(
    width: size,
    height: size,
    decoration: BoxDecoration(
      gradient: const LinearGradient(
        begin: Alignment.topLeft,
        end: Alignment.bottomRight,
        colors: [Color(0xFF7890A5), Color(0xFF435F7A)],
      ),
      shape: BoxShape.circle,
    ),
    child: Center(
      child: Text(
        'a',
        style: TextStyle(
          color: Colors.white,
          fontSize: size * 0.67,
          height: 1.0,
          fontWeight: FontWeight.w400,
          letterSpacing: -1.5,
        ),
      ),
    ),
  );
}

String _dateLabel(DateTime date) {
  final now = DateTime.now();
  final today = DateTime(now.year, now.month, now.day);
  final day = DateTime(date.year, date.month, date.day);
  if (day == today) return '今天';
  if (day == today.subtract(const Duration(days: 1))) return '昨天';
  return '${date.year}年${date.month}月${date.day}日';
}
