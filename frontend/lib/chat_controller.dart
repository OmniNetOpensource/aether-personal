import 'dart:async';
import 'dart:convert';

import 'package:flutter/foundation.dart';
import 'package:http/http.dart' as http;
import 'package:shared_preferences/shared_preferences.dart';
import 'package:uuid/uuid.dart';
import 'package:web_socket_channel/web_socket_channel.dart';

enum Delivery { sending, failed, delivered, read }

class ChatMessage {
  const ChatMessage({
    required this.id,
    required this.text,
    required this.isUser,
    required this.createdAt,
    required this.order,
    this.position,
    this.clientId,
    this.delivery = Delivery.delivered,
  });

  final String id;
  final String text;
  final bool isUser;
  final DateTime createdAt;
  final int order;
  final String? position;
  final String? clientId;
  final Delivery delivery;

  ChatMessage withDelivery(Delivery value) => ChatMessage(
    id: id,
    text: text,
    isUser: isUser,
    createdAt: createdAt,
    order: order,
    position: position,
    clientId: clientId,
    delivery: value,
  );
}

class ApiError implements Exception {
  const ApiError(this.message, this.status);
  final String message;
  final int status;
  @override
  String toString() => message;
}

class ChatController extends ChangeNotifier {
  ChatController({http.Client? client, Uri? baseUri})
    : _client = client ?? http.Client(),
      baseUri = baseUri ?? defaultBaseUri;

  static Uri get defaultBaseUri {
    const configured = String.fromEnvironment('API_BASE_URL');
    if (configured.isNotEmpty) return Uri.parse(configured);
    return kIsWeb
        ? Uri.base.replace(path: '/', query: '', fragment: '')
        : Uri.parse('http://localhost:8080');
  }

  final http.Client _client;
  final Uri baseUri;
  late final SharedPreferencesAsync _preferences = SharedPreferencesAsync();
  final List<ChatMessage> _messages = [];
  final Map<String, Timer> _deliveryTimers = {};
  WebSocketChannel? _channel;
  StreamSubscription<dynamic>? _subscription;
  Timer? _reconnectTimer;
  Timer? _authTimer;
  String? _token;
  String? _lastPosition;
  String? _readUpTo;
  int _connection = 0;
  int _nextOrder = 0;
  int _reconnectAttempt = 0;
  bool _disposed = false;
  bool _historyLoaded = false;
  bool _connecting = false;

  bool initializing = true;
  bool connected = false;
  bool loadingHistory = false;
  bool hasMoreHistory = true;
  String email = '';
  String? error;

  bool get signedIn => _token != null;
  List<ChatMessage> get messages => List.unmodifiable(_messages);

  Uri _uri(String path, [Map<String, String>? query]) =>
      baseUri.resolve(path).replace(queryParameters: query);

  Future<void> initialize() async {
    try {
      _token = await _preferences.getString('aether_session');
      email = await _preferences.getString('aether_email') ?? '';
    } catch (_) {
      error = '无法读取登录信息，请重新登录';
    }
    initializing = false;
    _notify();
    if (signedIn) unawaited(_connect());
  }

  Future<http.Response> _request(
    String method,
    String path, {
    Map<String, dynamic>? body,
    Map<String, String>? query,
  }) async {
    final requestToken = _token;
    final request = http.Request(method, _uri(path, query));
    if (requestToken != null) {
      request.headers['Authorization'] = 'Bearer $requestToken';
    }
    if (body != null) {
      request.headers['Content-Type'] = 'application/json';
      request.body = jsonEncode(body);
    }
    final response = await _client
        .send(request)
        .then(http.Response.fromStream)
        .timeout(const Duration(seconds: 15));
    if (response.statusCode >= 400) {
      if (response.statusCode == 401 &&
          requestToken != null &&
          _token == requestToken) {
        await _clearSession();
      }
      throw ApiError(switch (response.statusCode) {
        401 => '登录已过期，请重新登录',
        429 => '请求过于频繁，请稍后再试',
        400 when response.body.contains('invalid or expired code') =>
          '验证码不正确或已过期',
        400 when response.body.contains('invalid email') => '请输入有效的邮箱地址',
        >= 500 => '服务暂时不可用，请稍后重试',
        _ => response.body.trim().isEmpty ? '请求失败，请重试' : response.body.trim(),
      }, response.statusCode);
    }
    return response;
  }

  Future<void> requestCode(String address) async {
    await _request('POST', '/auth/code', body: {'email': address.trim()});
  }

  Future<void> signIn(String address, String code) async {
    final response = await _request(
      'POST',
      '/auth/verify',
      body: {'email': address.trim(), 'code': code.trim()},
    );
    final data = jsonDecode(response.body) as Map<String, dynamic>;
    _token = data['token'] as String;
    email = address.trim().toLowerCase();
    await _preferences.setString('aether_session', _token!);
    await _preferences.setString('aether_email', email);
    error = null;
    _notify();
    unawaited(_connect());
  }

  Future<void> signOut() async {
    try {
      await _request('POST', '/auth/logout');
    } finally {
      await _clearSession();
    }
  }

  Future<void> _clearSession() async {
    _token = null;
    _connection++;
    _reconnectTimer?.cancel();
    _authTimer?.cancel();
    for (final timer in _deliveryTimers.values) {
      timer.cancel();
    }
    _deliveryTimers.clear();
    await _subscription?.cancel();
    unawaited(_channel?.sink.close());
    _channel = null;
    connected = false;
    _connecting = false;
    _lastPosition = null;
    _readUpTo = null;
    _historyLoaded = false;
    loadingHistory = false;
    hasMoreHistory = true;
    _messages.clear();
    await _preferences.remove('aether_session');
    _notify();
  }

  Future<void> _connect() async {
    if (_disposed || !signedIn || _connecting || connected) return;
    _connecting = true;
    final generation = ++_connection;
    _reconnectTimer?.cancel();
    try {
      final endpoint = _uri('/ws');
      final channel = WebSocketChannel.connect(
        endpoint.replace(scheme: endpoint.scheme == 'https' ? 'wss' : 'ws'),
      );
      _channel = channel;
      _subscription = channel.stream.listen(
        (frame) {
          if (generation != _connection || _disposed) return;
          try {
            _handleFrame(jsonDecode(frame as String) as Map<String, dynamic>);
          } catch (_) {
            _disconnect(generation);
          }
        },
        onError: (Object _) => _disconnect(generation),
        onDone: () {
          if (generation != _connection) return;
          if (channel.closeReason == 'unauthorized') {
            error = '登录已过期，请重新登录';
            unawaited(_clearSession());
          } else {
            _disconnect(generation);
          }
        },
      );
      await channel.ready.timeout(const Duration(seconds: 10));
      if (generation != _connection || _disposed || !signedIn) {
        await channel.sink.close();
        return;
      }
      channel.sink.add(
        jsonEncode({
          'type': 'auth',
          'token': _token,
          if (_lastPosition != null) 'last_position': _lastPosition,
        }),
      );
      _authTimer = Timer(
        const Duration(seconds: 10),
        () => _disconnect(generation),
      );
    } catch (_) {
      _disconnect(generation);
    }
  }

  void _handleFrame(Map<String, dynamic> frame) {
    switch (frame['type']) {
      case 'delivered':
        final clientId = frame['client_message_id'] as String;
        _deliveryTimers.remove(clientId)?.cancel();
        _setDelivery(clientId, Delivery.delivered);
      case 'messages':
        connected = true;
        _connecting = false;
        _reconnectAttempt = 0;
        _authTimer?.cancel();
        error = null;
        _merge(frame);
        if (!_historyLoaded && !loadingHistory) unawaited(loadHistory());
      default:
        return;
    }
    _notify();
  }

  void _disconnect(int generation) {
    if (generation != _connection || _disposed || !signedIn) return;
    _connection++;
    connected = false;
    _connecting = false;
    _authTimer?.cancel();
    unawaited(_subscription?.cancel());
    unawaited(_channel?.sink.close());
    for (var i = 0; i < _messages.length; i++) {
      if (_messages[i].delivery == Delivery.sending) {
        _messages[i] = _messages[i].withDelivery(Delivery.failed);
      }
    }
    for (final timer in _deliveryTimers.values) {
      timer.cancel();
    }
    _deliveryTimers.clear();
    final delay = [1, 2, 4, 8, 15][_reconnectAttempt.clamp(0, 4)];
    _reconnectAttempt++;
    _reconnectTimer = Timer(Duration(seconds: delay), _connect);
    _notify();
  }

  void reconnect() {
    _reconnectTimer?.cancel();
    unawaited(_connect());
  }

  Future<void> loadHistory({bool older = false}) async {
    if (loadingHistory || !signedIn || (older && !hasMoreHistory)) return;
    loadingHistory = true;
    _notify();
    final positions = _messages
        .where((message) => message.position != null)
        .map((message) => message.position!)
        .toList();
    final before = older && positions.isNotEmpty
        ? positions.reduce((a, b) => a.compareTo(b) < 0 ? a : b)
        : null;
    final session = _token;
    try {
      final response = await _request(
        'GET',
        '/messages',
        query: before == null ? null : {'before': before},
      );
      if (_token != session || _disposed) return;
      final page = jsonDecode(response.body) as Map<String, dynamic>;
      final rows = page['messages'] as List<dynamic>;
      _merge(page);
      _historyLoaded = true;
      hasMoreHistory = rows.isNotEmpty;
      error = null;
    } catch (failure) {
      if (_token == session || !signedIn) {
        error = failure is ApiError ? failure.message : '历史消息加载失败，点击重试';
      }
    } finally {
      if (_token == session) loadingHistory = false;
      _notify();
    }
  }

  void _merge(Map<String, dynamic> data) {
    final read = data['read_up_to'] as String?;
    if (read != null && (_readUpTo == null || read.compareTo(_readUpTo!) > 0)) {
      _readUpTo = read;
    }
    for (final raw in data['messages'] as List<dynamic>) {
      final row = raw as Map<String, dynamic>;
      final id = row['id'] as String;
      final clientId = row['client_message_id'] as String?;
      final position = row['position'] as String;
      if (_lastPosition == null || position.compareTo(_lastPosition!) > 0) {
        _lastPosition = position;
      }
      final index = _messages.indexWhere(
        (message) =>
            message.id == id ||
            (clientId != null && message.clientId == clientId),
      );
      if (clientId != null) _deliveryTimers.remove(clientId)?.cancel();
      final message = ChatMessage(
        id: id,
        text: row['text'] as String,
        isUser: row['sender'] == 'user',
        createdAt: DateTime.parse(row['created_at'] as String).toLocal(),
        order: index < 0 ? _nextOrder++ : _messages[index].order,
        position: position,
        clientId: clientId,
      );
      if (index < 0) {
        _messages.add(message);
      } else {
        _messages[index] = message;
      }
    }
    for (var i = 0; i < _messages.length; i++) {
      final message = _messages[i];
      if (message.isUser &&
          message.position != null &&
          _readUpTo != null &&
          message.position!.compareTo(_readUpTo!) <= 0) {
        _messages[i] = message.withDelivery(Delivery.read);
      }
    }
    _messages.sort((a, b) {
      final positionOrder = a.position != null && b.position != null
          ? a.position!.compareTo(b.position!)
          : a.createdAt.compareTo(b.createdAt);
      return positionOrder == 0 ? a.order.compareTo(b.order) : positionOrder;
    });
  }

  bool send(String text) {
    final trimmed = text.trim();
    if (trimmed.isEmpty || trimmed.runes.length > 32000 || !signedIn) {
      return false;
    }
    final id = const Uuid().v4();
    _messages.add(
      ChatMessage(
        id: id,
        clientId: id,
        text: trimmed,
        isUser: true,
        createdAt: DateTime.now(),
        order: _nextOrder++,
        delivery: Delivery.sending,
      ),
    );
    _send(id, trimmed);
    _notify();
    return true;
  }

  void retry(ChatMessage message) {
    if (message.clientId == null) return;
    _setDelivery(message.clientId!, Delivery.sending);
    _send(message.clientId!, message.text);
    _notify();
  }

  void _send(String clientId, String text) {
    if (!connected) {
      _setDelivery(clientId, Delivery.failed);
      reconnect();
      return;
    }
    try {
      _channel!.sink.add(
        jsonEncode({
          'type': 'send_message',
          'client_message_id': clientId,
          'text': text,
        }),
      );
      _deliveryTimers[clientId]?.cancel();
      _deliveryTimers[clientId] = Timer(const Duration(seconds: 15), () {
        _deliveryTimers.remove(clientId);
        _setDelivery(clientId, Delivery.failed);
        _notify();
      });
    } catch (_) {
      _setDelivery(clientId, Delivery.failed);
    }
  }

  void _setDelivery(String clientId, Delivery delivery) {
    final index = _messages.indexWhere(
      (message) => message.clientId == clientId,
    );
    if (index < 0 || _messages[index].delivery == Delivery.read) return;
    _messages[index] = _messages[index].withDelivery(delivery);
  }

  void _notify() {
    if (!_disposed) notifyListeners();
  }

  @override
  void dispose() {
    _disposed = true;
    _connection++;
    _reconnectTimer?.cancel();
    _authTimer?.cancel();
    for (final timer in _deliveryTimers.values) {
      timer.cancel();
    }
    unawaited(_subscription?.cancel());
    unawaited(_channel?.sink.close());
    _client.close();
    super.dispose();
  }
}
